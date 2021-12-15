package aggregation

import (
	"math"

	"github.com/lintrieu/nefertiti/errors"
	"github.com/lintrieu/nefertiti/model"
	"github.com/lintrieu/nefertiti/precision"
)

var (
	EOrderBookTooThin = errors.New("Cannot find any supports. Order book is too thin. Please reconsider this market.")
)

// rounds [input] to to nearest multiple of [agg]
func Round(input, agg float64) float64 {
	return float64(int64(((input / agg) + 0.5))) * agg
}

// returns (agg, dip, pip, error)
func Get(
	exchange model.Exchange,
	market string,
	dip, pip float64,
	max, min float64,
	top int,
	strict, sandbox bool,
) (float64, float64, float64, error) {
	var (
		err    error
		client interface{}
		ticker float64
		stats  *model.Stats // 24-hour statistics
		avg    float64      // 24-hour average
	)

	if client, err = exchange.GetClient(model.BOOK, sandbox); err != nil {
		return 0, dip, pip, err
	}

	if ticker, err = exchange.GetTicker(client, market); err != nil {
		return 0, dip, pip, err
	}

	if stats, err = exchange.Get24h(client, market); err != nil {
		return 0, dip, pip, err
	}

	if avg, err = stats.Avg(exchange, sandbox); err != nil {
		return 0, dip, pip, err
	}

	return GetEx(exchange, client, market, ticker, avg, dip, pip, max, min, top, strict)
}

// returns (agg, dip, pip, error)
func GetEx(
	exchange model.Exchange,
	client interface{},
	market string,
	ticker float64,
	avg float64,
	dip, pip float64,
	max, min float64,
	top int,
	strict bool,
) (float64, float64, float64, error) {
	var (
		err  error
		out  float64
		book interface{} // bids
	)

	Max := func(a, b int) int {
		if a > b {
			return a
		}
		return b
	}

	if book, err = exchange.GetBook(client, market, model.BOOK_SIDE_BIDS); err != nil {
		return 0, dip, pip, err
	}

	for cnt := Max(top, 4); cnt >= Max(top, 2); cnt-- {
		if out, err = get(exchange, client, market, ticker, avg, book, dip, pip, max, min, cnt); err == nil {
			return out, dip, pip, err
		}
	}

	if !strict {
		x := math.Round(dip)
		y := math.Round(pip)
		// if we cannot find any supports, upper your pip setting one percentage at a time until (a) we can, or (b) 50%
		for y < 50 {
			y++
			for cnt := Max(top, 4); cnt >= Max(top, 2); cnt-- {
				if out, err = get(exchange, client, market, ticker, avg, book, x, y, max, min, cnt); err == nil {
					return out, x, y, err
				}
			}
		}
		// if we cannot find any supports, lower your dip setting one percentage at a time until (a) we can, or (b) 0%
		for x > 0 {
			x--
			for cnt := Max(top, 4); cnt >= Max(top, 2); cnt-- {
				if out, err = get(exchange, client, market, ticker, avg, book, x, y, max, min, cnt); err == nil {
					return out, x, y, err
				}
			}
		}
		// if we cannot find any supports, upper your pip setting one percentage at a time until (a) we can, or (b) 100%
		for y < 100 {
			y++
			for cnt := Max(top, 4); cnt >= Max(top, 2); cnt-- {
				if out, err = get(exchange, client, market, ticker, avg, book, x, y, max, min, cnt); err == nil {
					return out, x, y, err
				}
			}
		}
	}

	if err != nil {
		return 0, dip, pip, err
	} else {
		return 0, dip, pip, EOrderBookTooThin
	}
}

func get(
	exchange model.Exchange,
	client interface{},
	market string,
	ticker float64,
	avg float64,
	book1 interface{},
	dip, pip float64,
	max, min float64,
	cnt int,
) (float64, error) {
	var (
		err error
		agg float64 = 500
	)

	var steps = [...]float64{
		0.5, // 250
		0.4, // 100
		0.5, // 50
		0.5, // 25
		0.8, // 20
		0.5, // 10
		0.5, // 5
	}

	for {
		for _, step := range steps {
			agg = precision.Round((agg * step), 8)

			var book2 model.Book
			if book2, err = exchange.Aggregate(client, book1, market, agg); err != nil {
				return 0, err
			}

			// ignore orders that are more expense than ticker
			i := 0
			for i < len(book2) {
				if book2[i].Price > ticker {
					book2 = append(book2[:i], book2[i+1:]...)
				} else {
					i++
				}
			}

			// ignore orders that are cheaper than ticker minus 30%
			if min == 0 && pip < 100 {
				min = ticker - ((pip / 100) * ticker)
			}
			if min > 0 {
				i = 0
				for i < len(book2) {
					if book2[i].Price < min {
						book2 = append(book2[:i], book2[i+1:]...)
					} else {
						i++
					}
				}
			}

			// ignore orders that are more expensive than 24h average minus 5%
			if dip > 0 {
				i = 0
				for i < len(book2) {
					if book2[i].Price > (avg - ((dip / 100) * avg)) {
						book2 = append(book2[:i], book2[i+1:]...)
					} else {
						i++
					}
				}
			}

			// ignore BUY orders that are more expensive than max (optional)
			if max > 0 {
				i = 0
				for i < len(book2) {
					if book2[i].Price >= max {
						book2 = append(book2[:i], book2[i+1:]...)
					} else {
						i++
					}
				}
			}

			// we need at least 2 supports
			if len(book2) >= cnt {
				return agg, nil
			}

			// since we're rounding to 8 decimals, prevent this func from getting stuck in an infinite loop
			if agg <= 0.00000001 {
				if len(book2) > 0 {
					return agg, nil
				} else {
					return 0, EOrderBookTooThin
				}
			}
		}
	}
}
