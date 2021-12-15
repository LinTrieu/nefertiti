package command

import (
	"encoding/json"
	"fmt"
	"log"
	"math"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/lintrieu/nefertiti/aggregation"
	"github.com/lintrieu/nefertiti/errors"
	"github.com/lintrieu/nefertiti/exchanges"
	"github.com/lintrieu/nefertiti/flag"
	"github.com/lintrieu/nefertiti/model"
	"github.com/lintrieu/nefertiti/multiplier"
	"github.com/lintrieu/nefertiti/notify"
	"github.com/lintrieu/nefertiti/precision"
	"github.com/lintrieu/nefertiti/pricing"
	"github.com/lintrieu/nefertiti/signals"
)

type (
	BuyCommand struct {
		*CommandMeta
	}
)

func report(err error,
	market string,
	channel model.Channel,
	service model.Notify,
	exchange model.Exchange,
) {
	pc, file, line, _ := runtime.Caller(1)
	prefix := errors.FormatCaller(pc, file, line)

	var suffix string
	if market != "" {
		suffix = fmt.Sprintf("%s Market: %v.", suffix, market)
	}
	if channel != nil {
		suffix = fmt.Sprintf("%s Channel: %s.", suffix, channel.GetName())
	}

	msg := fmt.Sprintf("%s %v%s", prefix, err, suffix)
	_, ok := err.(*errors.Error)
	if ok && flag.Debug() {
		log.Printf("[ERROR] %s", err.(*errors.Error).ErrorStack(prefix, suffix))
	} else {
		log.Printf("[ERROR] %s", msg)
	}

	if service != nil {
		err := service.SendMessage(msg, (exchange.GetInfo().Name + " - ERROR"), model.ONCE_PER_MINUTE)
		if err != nil {
			log.Printf("[ERROR] %v", err)
		}
	}
}

func buyEvery(
	d time.Duration,
	client interface{},
	exchange model.Exchange,
	markets []string,
	hold model.Markets,
	agg float64,
	size float64,
	dip float64,
	pip float64,
	mult multiplier.Mult,
	dist int64,
	top int64,
	max float64,
	min float64,
	price float64,
	btcVolumeMin,
	deviation float64,
	service model.Notify,
	strict bool,
	sandbox bool,
	debug bool,
) {
	for range time.Tick(d) {
		market, err := buy(client, exchange, markets, hold, agg, size, dip, pip, mult, dist, top, max, min, price, btcVolumeMin, deviation, service, strict, sandbox, false, debug)
		if err != nil {
			report(err, market, nil, service, exchange)
		}
	}
}

func buy(
	client interface{},
	exchange model.Exchange,
	markets []string,
	hold model.Markets,
	agg float64,
	size float64,
	dip float64,
	pip float64,
	mult multiplier.Mult,
	dist int64,
	top int64,
	max float64,
	min float64,
	price float64,
	btcVolumeMin,
	deviation float64,
	service model.Notify,
	strict bool,
	sandbox bool,
	test bool,
	debug bool,
) (string, error) { // -> (market, error)
	var err error

	// true if we're told to open buys for every market, otherwise false.
	wildcard := len(markets) == 1 && markets[0] == "all"

	var (
		available  []model.Market // all available markets
		enumerable []string       // the markets we enumerate/buy
	)

	if available, err = exchange.GetMarkets(!wildcard, sandbox, flag.Get("ignore").Split()); err != nil {
		return "", err
	}

	if !wildcard {
		enumerable = markets
	} else {
		quote := flag.Get("quote").String()
		if quote == "" {
			return "", errors.New("missing argument: quote")
		}
		for _, market := range available {
			if strings.EqualFold(market.Quote, quote) {
				enumerable = append(enumerable, market.Name)
			}
		}
	}

	for _, market := range enumerable {
		// "algo" orders are stop-loss, take-profit, and OCO (aka one-cancels-the-other) orders
		if hasAlgoOrder, _ := exchange.HasAlgoOrder(client, market); hasAlgoOrder {
			log.Printf("[INFO] Ignoring %s because you have at least one \"algo\" order open on this market.\n", market)
			continue
		}

		var (
			ticker float64
			stats  *model.Stats // 24-hour statistics
			avg    float64      // 24-hour average
		)

		if ticker, err = exchange.GetTicker(client, market); err != nil {
			return market, err
		}

		if stats, err = exchange.Get24h(client, market); err != nil {
			return market, err
		}

		if btcVolumeMin > 0 && stats.BtcVolume > 0 && stats.BtcVolume < btcVolumeMin {
			log.Printf("[INFO] Ignoring %s because volume %.2f is lower than %.2f BTC\n", market, stats.BtcVolume, btcVolumeMin)
			continue
		}

		if avg, err = stats.Avg(exchange, sandbox); err != nil {
			return market, err
		}

		var (
			magg float64
			mdip float64
			mpip float64
			mmin float64
			mmax float64
		)

		magg = agg
		mdip = dip
		mpip = pip
		mmax = max

		hasOpenSell := 0
		// ignore supports where the price is higher than BUY order(s) that were (a) filled and (b) not been sold (yet)
		if !test {
			var opened model.Orders
			if opened, err = exchange.GetOpened(client, market); err != nil {
				return market, err
			}
			for _, order := range opened {
				if order.Side == model.SELL {
					hasOpenSell++
				}
			}
			// step 1: loop through the filled BUY orders
			var closed model.Orders
			if closed, err = exchange.GetClosed(client, market); err != nil {
				return market, err
			}
			for _, fill := range closed {
				if fill.Side == model.BUY {
					// step 2: has this filled BUY order NOT been sold?
					var prec int
					if prec, err = exchange.GetPricePrec(client, market); err != nil {
						return market, err
					}
					if opened.IndexByPrice(model.SELL, market, pricing.Multiply(fill.Price, mult, prec)) > -1 {
						if mmax == 0 || mmax >= fill.Price {
							mmax = fill.Price
						}
					}
				}
			}
		}

		if magg == 0 {
			if magg, mdip, mpip, err = aggregation.GetEx(exchange, client, market, ticker, avg, dip, pip, mmax, min, int(top), strict); err != nil {
				if errors.Is(err, aggregation.EOrderBookTooThin) && (len(enumerable) > 1 || flag.Get("ignore").Contains("error")) {
					report(err, market, nil, service, exchange)
					continue
				} else {
					return market, err
				}
			}
		}

		var (
			book1 interface{}
			book2 model.Book
		)

		if book1, err = exchange.GetBook(client, market, model.BOOK_SIDE_BIDS); err != nil {
			return market, err
		}

		if book2, err = exchange.Aggregate(client, book1, market, magg); err != nil {
			return market, err
		}

		// ignore orders that are more expensive than ticker
		i := 0
		for i < len(book2) {
			if book2[i].Price > ticker {
				book2 = append(book2[:i], book2[i+1:]...)
			} else {
				i++
			}
		}

		// ignore orders that are cheaper than ticker minus 30%
		mmin = min
		if mmin == 0 && mpip < 100 {
			mmin = ticker - ((mpip / 100) * ticker)
		}
		if mmin > 0 {
			i = 0
			for i < len(book2) {
				if book2[i].Price < mmin {
					book2 = append(book2[:i], book2[i+1:]...)
				} else {
					i++
				}
			}
		}

		// ignore orders that are more expensive than 24h average minus 5%
		if mdip > 0 {
			i = 0
			for i < len(book2) {
				if book2[i].Price > (avg - ((mdip / 100) * avg)) {
					book2 = append(book2[:i], book2[i+1:]...)
				} else {
					i++
				}
			}
		}

		// ignore BUY orders that are more expensive than max (optional)
		if mmax > 0 {
			i = 0
			for i < len(book2) {
				if book2[i].Price >= mmax {
					book2 = append(book2[:i], book2[i+1:]...)
				} else {
					i++
				}
			}
		}

		// sort the order book by size (highest order size first)
		sort.Slice(book2, func(i1, i2 int) bool {
			return book2[i1].Size > book2[i2].Size
		})

		// we need at least one support
		if len(book2) == 0 {
			if len(enumerable) > 1 || flag.Get("ignore").Contains("error") {
				report(aggregation.EOrderBookTooThin, market, nil, service, exchange)
				continue
			} else {
				return market, aggregation.EOrderBookTooThin
			}
		}
		// distance between the buy orders must be at least 2%
		if dist > 0 {
			if len(book2) > 1 {
				var hi, lo, delta float64
				cnt := math.Min(float64(len(book2)), float64(top))
			outer:
				for i1 := 0; i1 < int(cnt); i1++ {
					for i2 := 0; i2 < int(cnt); i2++ {
						if i2 != i1 {
							hi = book2[i1].Price
							lo = book2[i2].Price
							if hi < lo {
								hi = book2[i2].Price
								lo = book2[i1].Price
							}
							delta = ((hi - lo) / lo) * 100
							if delta < float64(dist) {
								msg := fmt.Sprintf("Distance between your %s orders is %.2f percent (too low, less than %d percent)", market, delta, dist)
								if agg == 0 {
									log.Printf("[WARN] %s\n", msg)
									break outer
								} else {
									return market, errors.New(msg + ". Please update your aggregation.")
								}
							}
						}
					}
				}
			}
		}

		var prec int
		if prec, err = exchange.GetSizePrec(client, market); err != nil {
			return market, err
		}

		var base string
		if base, err = model.GetBaseCurr(available, market); err != nil {
			return market, err
		}

		for i := 0; i < len(book2); i++ {
			book2[i].Size = size

			// if we have an arg named --price, then we'll calculate the desired size here
			if price != 0 {
				book2[i].Size = precision.Round((price / book2[i].Price), prec)
			}

			// the more non-sold sell orders we have, the bigger the new buy order size
			if flag.Dca() {
				book2[i].Size = precision.Round((book2[i].Size * (1 + (float64(hasOpenSell) * 0.2))), prec)
			}

			// for BTC and ETH, there is a minimum size (otherwise, we would never be hodl'ing)
			units := model.GetSizeMin(hold.HasMarket(market), base)
			if book2[i].Size < units {
				return market, errors.Errorf("Cannot buy %s. Size is too low. You must buy at least %f units.", market, units)
			}
		}

		// cancel your open buy order(s), then place the top X buy orders
		if !test {
			if len(book2) < int(top) {
				err = exchange.Buy(client, true, market, book2.Calls(), deviation, model.LIMIT)
			} else {
				err = exchange.Buy(client, true, market, book2[:top].Calls(), deviation, model.LIMIT)
			}
			if err != nil {
				if len(enumerable) > 1 || flag.Get("ignore").Contains("error") {
					report(err, market, nil, service, exchange)
					continue
				} else {
					return market, err
				}
			}
		}

		var out []byte
		if len(book2) < int(top) {
			out, err = json.Marshal(book2)
		} else {
			out, err = json.Marshal(book2[:top])
		}
		if err != nil {
			return market, err
		}
		log.Println(string(out))
	}

	return "", nil
}

func buySignalsEvery(
	d time.Duration,
	channel model.Channel,
	client interface{},
	exchange model.Exchange,
	quote model.Assets,
	price float64,
	valid time.Duration,
	calls model.Calls,
	min float64,
	btcVolumeMin,
	deviation float64,
	service model.Notify,
	sandbox bool,
	debug bool,
) {
	var err error
	for range time.Tick(d) {
		calls, err = buySignals(channel, client, exchange, quote, price, valid, calls, min, btcVolumeMin, deviation, service, sandbox, false, debug)
		if err != nil {
			report(err, "", channel, service, exchange)
		}
	}
}

func buySignals(
	channel model.Channel,
	client interface{},
	exchange model.Exchange,
	quote model.Assets,
	price float64,
	valid time.Duration,
	old model.Calls,
	min float64,
	btcVolumeMin,
	deviation float64,
	service model.Notify,
	sandbox bool,
	test bool,
	debug bool,
) (model.Calls, error) {
	var (
		err error
		new model.Calls
	)

	if quote.IsEmpty() {
		return old, errors.New("missing argument: quote")
	}

	var all []model.Market
	if all, err = exchange.GetMarkets(true, sandbox, flag.Get("ignore").Split()); err != nil {
		return old, err
	}

	var markets []string
	if markets, err = channel.GetMarkets(exchange, quote, btcVolumeMin, valid, sandbox, debug, flag.Get("ignore").Split()); err != nil {
		return old, err
	}

	// --- BEGIN --- svanas 2018-12-06 --- allow for signals to buy new listings ---
	for _, market := range markets {
		if !model.HasMarket(all, market) {
			if all, err = exchange.GetMarkets(false, sandbox, flag.Get("ignore").Split()); err != nil {
				return old, err
			}
			break
		}
	}
	// ---- END ---- svanas 2018-12-06 ---------------------------------------------

	for _, market := range markets {
		if model.HasMarket(all, market) {
			if flag.Get("ignore").Contains("leveraged") {
				var base string
				if base, err = model.GetBaseCurr(all, market); err == nil {
					if exchange.IsLeveragedToken(base) {
						log.Printf("[INFO] Ignoring %s because %s is a leveraged token.\n", market, base)
						continue
					}
				}
			}

			var ticker float64
			if ticker, err = exchange.GetTicker(client, market); err != nil {
				return old, err
			}

			var prec int
			if prec, err = exchange.GetSizePrec(client, market); err != nil {
				return old, err
			}

			var calls model.Calls
			if calls, err = channel.GetCalls(exchange, market, sandbox, debug); err != nil {
				return old, err
			}

			for i := range calls {
				calls[i].Size = precision.Round(price/ticker, prec)

				if flag.Dca() {
					hasOpenSell := 0
					var opened model.Orders
					if opened, err = exchange.GetOpened(client, market); err != nil {
						return old, err
					}
					for _, order := range opened {
						if order.Side == model.SELL {
							hasOpenSell++
						}
					}
					calls[i].Size = precision.Round((calls[i].Size * (1 + (float64(hasOpenSell) * 0.2))), prec)
				}

				if !calls[i].Skip {
					if calls[i].Corrupt(channel.GetOrderType()) {
						log.Printf("[INFO] Ignoring %s because the signal appears to be corrupt.\n", calls[i].Market)
						calls[i].Skip = true
					}
				}
			}

			if min > 0 {
				for i := range calls {
					if !calls[i].Skip {
						limit := calls[i].Price
						if limit == 0 {
							limit = ticker
						}
						if limit < min {
							log.Printf("[INFO] Ignoring %s because price %.8f is lower than %.8f\n", calls[i].Market, calls[i].Price, min)
							calls[i].Skip = true
						}
					}
				}
			}

			if btcVolumeMin > 0 {
				for i := range calls {
					if !calls[i].Skip {
						stats, err := exchange.Get24h(client, calls[i].Market)
						if err != nil {
							return old, err
						}
						if stats.BtcVolume > 0 && stats.BtcVolume < btcVolumeMin {
							log.Printf("[INFO] Ignoring %s because volume %.2f is lower than %.2f %s\n", calls[i].Market, stats.BtcVolume, btcVolumeMin, quote)
							calls[i].Skip = true
						}
					}
				}
			}

			if len(calls) > 0 {
				new = append(new, calls...)
				if !test {
					// do not (re)open buy orders for signals that we already know about
					if old != nil {
						for i := range calls {
							if !calls[i].Skip {
								n := -1
								if channel.GetOrderType() == model.MARKET {
									n = old.IndexByMarket(calls[i].Market)
								} else {
									n = old.IndexByMarketPrice(calls[i].Market, calls[i].Price)
								}
								if n > -1 {
									calls[i].Skip = true
								}
							}
						}
					}
					if calls.HasBuy() {
						// cancel your open buy order(s), then place the new buy orders
						err = exchange.Buy(client, false, market, calls, deviation, channel.GetOrderType())
						if err != nil {
							report(err, market, channel, service, exchange)
						}
					}
				}
			}
		}
	}

	//lint:ignore S1031 unnecessary nil check around range
	if old != nil {
		// cancel open orders that are removed from the channel
		for _, entry := range old {
			n := 0
			if channel.GetOrderType() == model.MARKET {
				n = new.IndexByMarket(entry.Market)
			} else {
				n = new.IndexByMarketPrice(entry.Market, entry.Price)
			}
			if n == -1 {
				var data []byte
				if data, err = json.Marshal(entry); err == nil {
					log.Println("[CANCELLED] " + string(data))
				}
				if err = exchange.Cancel(client, entry.Market, model.BUY); err != nil {
					return new, err
				}
			}
		}
	}

	// echo the new book to the console
	if len(new) > 0 {
		var out []byte
		if out, err = json.Marshal(new); err != nil {
			return new, err
		}
		log.Println(string(out))
	} else {
		log.Println("[INFO] No new signals available.")
	}

	return new, nil
}

func (c *BuyCommand) Run(args []string) int {
	var (
		err error
		flg *flag.Flag
	)

	var exchange model.Exchange
	if exchange, err = exchanges.GetExchange(); err != nil {
		return c.ReturnError(err)
	}

	test := flag.Exists("test")

	var client interface{}
	if client, err = exchange.GetClient(model.PRIVATE, flag.Sandbox()); err != nil {
		return c.ReturnError(err)
	}

	var service model.Notify = nil
	if !test {
		if service, err = notify.New().Init(flag.Interactive(), true); err != nil {
			return c.ReturnError(err)
		}
	}

	var min float64 = 0
	if min, err = flag.Min(); err != nil {
		return c.ReturnError(err)
	}

	// --volume=x
	var btcVolumeMin float64 = 0
	flg = flag.Get("volume")
	if flg.Exists {
		if btcVolumeMin, err = flg.Float64(); err != nil {
			return c.ReturnError(errors.Errorf("volume %v is invalid", flg))
		}
	}

	// --devn=x
	var deviation float64 = 1.0
	flg = flag.Get("devn")
	if flg.Exists {
		if deviation, err = flg.Float64(); err != nil {
			return c.ReturnError(errors.Errorf("devn %v is invalid", flg))
		}
	}

	flg = flag.Get("signals")
	if flg.Exists {
		var channel model.Channel
		if channel, err = signals.New().FindByName(flg.String()); err != nil {
			if channel == nil {
				return c.ReturnError(err)
			} else {
				return c.ReturnError(errors.Errorf("%v. Channel: %s", err, channel.GetName()))
			}
		}
		if channel == nil {
			return c.ReturnError(errors.Errorf("signals %v does not exist", flg))
		}
		// --price=x
		flg = flag.Get("price")
		if !flg.Exists {
			return c.ReturnError(errors.New("missing argument: price"))
		}
		var price float64
		if price, err = flg.Float64(); err != nil {
			return c.ReturnError(errors.Errorf("price %v is invalid", flg))
		}
		// --valid=x
		var duration2 time.Duration
		if duration2, err = channel.GetValidity(); err != nil {
			return c.ReturnError(err)
		}
		flg = flag.Get("valid")
		if flg.Exists {
			var valid float64 = 1
			if valid, err = flg.Float64(); err != nil {
				return c.ReturnError(errors.Errorf("valid %v is incorrect", flg))
			}
			duration2 = time.Duration(valid * float64(time.Hour))
		}
		// initial run starts here
		var calls model.Calls
		if calls, err = buySignals(channel, client, exchange, flag.Get("quote").Split(), price, duration2, nil, min, btcVolumeMin, deviation, service, flag.Sandbox(), test, flag.Debug()); err != nil {
			if flag.Get("ignore").Contains("error") {
				log.Printf("[ERROR] %v\n", err)
			} else {
				return c.ReturnError(err)
			}
		}
		// iterations start here
		if !test {
			flg = flag.Get("repeat")
			if flg.Exists {
				var repeat float64 = 1
				if repeat, err = flg.Float64(); err != nil {
					return c.ReturnError(errors.Errorf("repeat %v is invalid", flg))
				}
				duration1 := time.Duration(repeat * float64(time.Hour))
				if duration1 > 0 {
					limit := channel.GetRateLimit()
					if limit > 0 {
						if duration1 < limit {
							if flag.Exists("backdoor") {
								// bypass the rate limit
							} else {
								return c.ReturnError(errors.Errorf("repeat %v is invalid. min value is %g", flg, (float64(limit) / float64(time.Hour))))
							}
						}
					}
					msg := fmt.Sprintf("Listening to %s...", channel.GetName())
					log.Println("[INFO] " + msg)
					if service != nil {
						service.SendMessage(msg, (exchange.GetInfo().Name + " - INFO"), model.ALWAYS)
					}
					if err = c.ReturnSuccess(); err != nil {
						return c.ReturnError(err)
					}
					buySignalsEvery(duration1, channel, client, exchange, flag.Get("quote").Split(), price, duration2, calls, min, btcVolumeMin, deviation, service, flag.Sandbox(), flag.Debug())
				}
			}
		}
		return 0
	}

	var all []model.Market
	if all, err = exchange.GetMarkets(true, flag.Sandbox(), flag.Get("ignore").Split()); err != nil {
		return c.ReturnError(err)
	}

	flg = flag.Get("market")
	if !flg.Exists {
		return c.ReturnError(errors.New("missing argument: market"))
	}
	splitted := flg.Split()
	if len(splitted) > 1 || (len(splitted) == 1 && splitted[0] != "all") {
		for _, market := range splitted {
			if !model.HasMarket(all, market) {
				return c.ReturnError(errors.Errorf("market %s does not exist", market))
			}
		}
	}

	hold := flag.Get("hold").Split()
	if len(hold) > 0 && hold[0] != "" {
		for _, market := range hold {
			if market != "" && !model.HasMarket(all, market) {
				return c.ReturnError(fmt.Errorf("market %s does not exist", market))
			}
		}
	}

	var agg float64 = 0
	flg = flag.Get("agg")
	if flg.Exists {
		if agg, err = flg.Float64(); err != nil {
			return c.ReturnError(errors.Errorf("agg %v is invalid", flg))
		}
	}

	var (
		size  float64 = 0
		price float64 = 0
	)
	// if we have an arg named --size, then that one will take precedence
	flg = flag.Get("size")
	if flg.Exists {
		if size, err = flg.Float64(); err != nil {
			return c.ReturnError(errors.Errorf("size %v is invalid", flg))
		}
	} else {
		// if we have an arg named --price, then we'll calculate the desired size later
		flg = flag.Get("price")
		if !flg.Exists {
			return c.ReturnError(errors.New("missing argument: size"))
		} else {
			if price, err = flg.Float64(); err != nil {
				return c.ReturnError(errors.Errorf("price %v is invalid", flg))
			}
		}
	}

	var dip float64 = 5
	if dip, err = flag.Dip(); err != nil {
		return c.ReturnError(err)
	}

	var pip float64 = 30
	if pip, err = flag.Pip(); err != nil {
		return c.ReturnError(err)
	}

	var mult multiplier.Mult
	if mult, err = multiplier.Get(multiplier.FIVE_PERCENT); err != nil {
		return c.ReturnError(err)
	}

	var dist int64 = 2
	flg = flag.Get("dist")
	if flg.Exists {
		if dist, err = flg.Int64(); err != nil {
			return c.ReturnError(errors.Errorf("dist %v is invalid", flg))
		}
	}

	var top int64 = 2
	flg = flag.Get("top")
	if flg.Exists {
		if top, err = flg.Int64(); err != nil {
			return c.ReturnError(errors.Errorf("top %v is invalid", flg))
		}
	}

	var max float64 = 0
	if max, err = flag.Max(); err != nil {
		return c.ReturnError(err)
	}

	if _, err = buy(client, exchange, splitted, hold, agg, size, dip, pip, mult, dist, top, max, min, price, btcVolumeMin, deviation, service, flag.Strict(), flag.Sandbox(), test, flag.Debug()); err != nil {
		if flag.Get("ignore").Contains("error") {
			log.Printf("[ERROR] %v\n", err)
		} else {
			return c.ReturnError(err)
		}
	}

	if !test {
		var repeat float64 = 1
		flg = flag.Get("repeat")
		if flg.Exists {
			if repeat, err = flg.Float64(); err != nil {
				return c.ReturnError(errors.Errorf("repeat %v is invalid", flg))
			}
			if err = c.ReturnSuccess(); err != nil {
				return c.ReturnError(err)
			}
			buyEvery(time.Duration(repeat*float64(time.Hour)), client, exchange, splitted, hold, agg, size, dip, pip, mult, dist, top, max, min, price, btcVolumeMin, deviation, service, flag.Strict(), flag.Sandbox(), flag.Debug())
		}
	}

	return 0
}

func (c *BuyCommand) Help() string {
	text := `
Usage: ./nefertiti buy [options]

The buy command opens new limit buy orders on the specified exchange/market.

Options:
  --exchange = name, for example: Bittrex
  --market   = a valid market pair.
  --size     = amount of cryptocurrency to buy per order. please note --size is
               mutually exclusive with --price, eg. the price in quote currency
               you will want to pay for an order.
  --agg      = aggregate public order book to nearest multiple of agg.
               (optional)
  --dip      = percentage that will kick the bot into action.
               (optional, defaults to 5%)
  --pip      = range in where the market is suspected to move up and down.
               the bot will ignore supports outside of this range.
               (optional, defaults to 30%)
  --dist     = distribution/distance between your orders.
               (optional, defaults to 2%)
  --top      = number of orders to place in your book.
               (optional, defaults to 2)
  --max      = maximum price that you will want to pay for the coins.
               (optional)
  --min      = minimum price that you will want to pay for the coins.
               (optional)
  --volume   = minimum BTC volume over the last 24 hours.
               optional, for example: --volume=10			   
  --dca      = if included, then slowly but surely, the bot will proportionally
               increase your stack while lowering your average buying price.
               (optional)
  --test     = if included, merely reports what it would do.
               (optional, defaults to false)
  --repeat   = if included, repeats this command every X hours.
               (optional, defaults to false)

Alternative Strategy:
  The trading bot can listen to signals (for example: Telegram bots) as an
  alternative to the built-in strategy. Please refer to the below options.

Alternative Strategy Options:
  --exchange = name, for example: Bittrex
  --signals  = provider, for example: MiningHamster
  --price    = price (in quote currency) that you will want to pay for an order
  --quote    = currency that is used as the reference, for example: BTC or USDT
  --min      = minimum price for a unit of quote currency.
               optional, for example: 0.00000050
  --volume   = minimum BTC volume over the last 24 hours.
               optional, for example: --volume=10
  --devn     = buy price deviation. this multiplier is applied to the suggested
               price from the signal, to calculate your actual limit price.
               optional, for example: --devn=1.01
  --valid    = if included, specifies the time (in hours, defaults to 1 hour)
               that the signal is "active". after this timeout elapses, the
               bot will cancel the (non-filled) limit buy order(s) associated
               with the signal.
               (optional, defaults to 1 hour)
  --repeat   = if included, repeats this command every X hours.
               (optional, defaults to false)
`
	return strings.TrimSpace(text)
}

func (c *BuyCommand) Synopsis() string {
	return "Opens new limit buy orders on the specified exchange/market."
}
