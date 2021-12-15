//lint:file-ignore ST1006 receiver name should be a reflection of its identity; don't use generic names such as "this" or "self"
package exchanges

import (
	"encoding/json"
	"fmt"
	"log"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	filemutex "github.com/alexflint/go-filemutex"
	"github.com/lintrieu/nefertiti/aggregation"
	exchange "github.com/lintrieu/nefertiti/cexio"
	"github.com/lintrieu/nefertiti/errors"
	"github.com/lintrieu/nefertiti/flag"
	"github.com/lintrieu/nefertiti/model"
	"github.com/lintrieu/nefertiti/multiplier"
	"github.com/lintrieu/nefertiti/notify"
	"github.com/lintrieu/nefertiti/precision"
	"github.com/lintrieu/nefertiti/pricing"
	"github.com/lintrieu/nefertiti/session"
)

var (
	cexioMutex *filemutex.FileMutex
)

const (
	cexioSessionFile = "cexio.time"
	cexioSessionLock = "cexio.lock"
)

func init() {
	exchange.BeforeRequest = func(path string) error {
		var err error

		if cexioMutex == nil {
			if cexioMutex, err = filemutex.New(session.GetSessionFile(cexioSessionLock)); err != nil {
				return err
			}
		}

		if err = cexioMutex.Lock(); err != nil {
			return err
		}

		var lastRequest *time.Time
		if lastRequest, err = session.GetLastRequest(cexioSessionFile); err != nil {
			return err
		}

		if lastRequest != nil {
			elapsed := time.Since(*lastRequest)
			if elapsed.Seconds() < (float64(1) / float64(exchange.RequestsPerSecond)) {
				sleep := time.Duration((float64(time.Second) / float64(exchange.RequestsPerSecond))) - elapsed
				if flag.Debug() {
					log.Printf("[DEBUG] sleeping %f seconds", sleep.Seconds())
				}
				time.Sleep(sleep)
			}
		}

		if flag.Debug() {
			log.Println("[DEBUG] GET " + path)
		}

		return nil
	}
	exchange.AfterRequest = func() {
		defer func() {
			cexioMutex.Unlock()
		}()
		session.SetLastRequest(cexioSessionFile, time.Now())
	}
}

type CexIo struct {
	*model.ExchangeInfo
}

//lint:ignore U1000 func is unused
func (self *CexIo) info(msg string, level int64, service model.Notify) {
	_, file, line, _ := runtime.Caller(1)
	log.Printf("[INFO] %s:%d %s", filepath.Base(file), line, msg)
	if service != nil {
		if notify.CanSend(level, notify.INFO) {
			service.SendMessage(msg, "CEX.IO - INFO", model.ALWAYS)
		}
	}
}

func (self *CexIo) error(err error, level int64, service model.Notify) {
	_, file, line, _ := runtime.Caller(1)
	str := err.Error()
	log.Printf("[ERROR] %s:%d %s", filepath.Base(file), line, strings.Replace(str, "\n\n", " ", -1))
	if service != nil {
		if notify.CanSend(level, notify.ERROR) {
			service.SendMessage(str, "CEX.IO - ERROR", model.ONCE_PER_MINUTE)
		}
	}
}

func (self *CexIo) encodePair(symbol1, symbol2 string) (string, error) {
	if symbol1 == "" {
		return "", errors.New("symbol1 is empty")
	}
	if symbol2 == "" {
		return "", errors.New("symbol2 is empty")
	}
	return symbol1 + "-" + symbol2, nil
}

func (self *CexIo) decodePair(pair string) (symbol1, symbol2 string, err error) {
	symbols := strings.Split(pair, "-")
	if len(symbols) > 1 {
		return symbols[0], symbols[1], nil
	}
	return "", "", errors.New("Invalid market pair: " + pair)
}

func (self *CexIo) GetInfo() *model.ExchangeInfo {
	return self.ExchangeInfo
}

func (self *CexIo) GetClient(permission model.Permission, sandbox bool) (interface{}, error) {
	if permission != model.PRIVATE {
		return exchange.New("", "", ""), nil
	}

	var (
		err       error
		apiKey    string
		apiSecret string
		userName  string
	)
	if apiKey, apiSecret, userName, err = promptForApiKeysEx("CEX.IO"); err != nil {
		return nil, err
	}

	return exchange.New(apiKey, apiSecret, userName), nil
}

func (self *CexIo) GetMarkets(cached, sandbox bool, ignore []string) ([]model.Market, error) {
	var (
		err error
		out []model.Market
	)

	client := exchange.New("", "", "")

	var pairs []exchange.Pair
	if pairs, err = client.CurrencyLimits(); err != nil {
		return nil, err
	}

	for _, pair := range pairs {
		var market string
		if market, err = self.encodePair(pair.Symbol1, pair.Symbol2); err == nil {
			out = append(out, model.Market{
				Name:  market,
				Base:  pair.Symbol1,
				Quote: pair.Symbol2,
			})
		}
	}

	return out, nil
}

func (self *CexIo) FormatMarket(base, quote string) string {
	return fmt.Sprintf("%s-%s", base, quote)
}

// listens to the open orders, look for cancelled orders, send a notification.
func (self *CexIo) listen(
	client *exchange.Client,
	service model.Notify,
	level int64,
	old exchange.Orders,
) (exchange.Orders, error) {
	var err error

	// get my new open orders
	var new exchange.Orders
	if new, err = client.OpenOrdersAll(); err != nil {
		return old, err
	}

	// look for cancelled orders
	for _, order := range old {
		if new.IndexById(order.Id) == -1 {
			// get my archived orders
			var archive exchange.Orders
			if archive, err = client.ArchivedOrders(order.Symbol1, order.Symbol2); err != nil {
				return new, err
			}
			// if this order has NOT been FILLED, then it has been cancelled.
			if archive.IndexById(order.Id) == -1 {
				var data []byte
				if data, err = json.Marshal(order); err != nil {
					return new, err
				}

				log.Println("[CANCELLED] " + string(data))

				side := order.Side()
				if side != exchange.SIDE_UNKNOWN {
					if service != nil && notify.CanSend(level, notify.CANCELLED) {
						if err = service.SendMessage(order, fmt.Sprintf("CEX.IO - Done %s (Reason: Cancelled)", strings.Title(order.Type)), model.ALWAYS); err != nil {
							log.Printf("[ERROR] %v", err)
						}
					}
				}
			}
		}
	}

	// look for new orders
	for _, order := range new {
		if old.IndexById(order.Id) == -1 {
			var data []byte
			if data, err = json.Marshal(order); err != nil {
				return new, err
			}

			log.Println("[OPEN] " + string(data))

			if service != nil {
				side := order.Side()
				if side != exchange.SIDE_UNKNOWN {
					if notify.CanSend(level, notify.OPENED) || (level == notify.LEVEL_DEFAULT && side == exchange.SELL) {
						if err = service.SendMessage(order, ("CEX.IO - Open " + strings.Title(order.Type)), model.ALWAYS); err != nil {
							log.Printf("[ERROR] %v", err)
						}
					}
				}
			}
		}
	}

	return new, nil
}

// listens to the archived orders, look for newly filled orders, automatically place new LIMIT SELL orders.
func (self *CexIo) sell(
	client *exchange.Client,
	mult multiplier.Mult,
	hold, earn model.Markets,
	service model.Notify,
	twitter *notify.TwitterKeys,
	level int64,
	old exchange.Orders,
	sandbox bool,
) (exchange.Orders, error) {
	var err error

	var markets []model.Market
	if markets, err = self.GetMarkets(true, sandbox, nil); err != nil {
		return old, err
	}

	// get my new archived orders
	var new []exchange.Order
	if new, err = client.ArchivedOrdersAll(); err != nil {
		return old, err
	}

	// look for filled orders
	for _, order := range new {
		if old.IndexById(order.Id) == -1 {
			var data []byte
			if data, err = json.Marshal(order); err != nil {
				return new, err
			}

			log.Println("[FILLED] " + string(data))

			side := order.Side()
			if side != exchange.SIDE_UNKNOWN {
				if notify.CanSend(level, notify.FILLED) {
					if service != nil {
						if err = service.SendMessage(order, fmt.Sprintf("CEX.IO - Done %s (Reason: Filled %f qty)", strings.Title(order.Type), order.Amount), model.ALWAYS); err != nil {
							log.Printf("[ERROR] %v", err)
						}
					}
					if twitter != nil {
						notify.Tweet(twitter, fmt.Sprintf("Done %s. $%s-%s priced at %f #CEXIO", strings.Title(order.Type), order.Symbol1, order.Symbol2, order.Price))
					}
				}
				// has a buy order been filled? then place a sell order
				if side == exchange.BUY {
					var market string
					if market, err = self.encodePair(order.Symbol1, order.Symbol2); err == nil {
						var (
							base  string
							quote string
						)
						base, quote, err = model.ParseMarket(markets, market)
						if err == nil {
							qty := self.GetMaxSize(client, base, quote, hold.HasMarket(market), earn.HasMarket(market), order.Amount, mult)
							if qty > 0 {
								var prec int
								if prec, err = self.GetPricePrec(client, market); err == nil {
									_, err = client.PlaceOrder(
										order.Symbol1, order.Symbol2, exchange.SELL,
										qty,
										pricing.Multiply(order.Price, mult, prec),
									)
								}
							}
						}
					}
					if err != nil {
						return new, errors.New(err.Error() + "\n\n" + string(data))
					}
				}
			}
		}
	}

	return new, nil
}

func (self *CexIo) Sell(
	strategy model.Strategy,
	hold, earn model.Markets,
	sandbox, tweet, debug bool,
	success model.OnSuccess,
) error {
	if strategy == model.STRATEGY_STANDARD {
		// we are OK
	} else {
		return errors.New("strategy not implemented")
	}

	var (
		err       error
		apiKey    string
		apiSecret string
		userName  string
	)
	if apiKey, apiSecret, userName, err = promptForApiKeysEx("CEX.IO"); err != nil {
		return err
	}

	var service model.Notify = nil
	if service, err = notify.New().Init(flag.Interactive(), true); err != nil {
		return err
	}

	var twitter *notify.TwitterKeys = nil
	if tweet {
		if twitter, err = notify.TwitterPromptForKeys(flag.Interactive()); err != nil {
			return err
		}
	}

	client := exchange.New(apiKey, apiSecret, userName)

	// get my open orders
	var open []exchange.Order
	if open, err = client.OpenOrdersAll(); err != nil {
		return err
	}

	// get my archived orders
	var archive []exchange.Order
	if archive, err = client.ArchivedOrdersAll(); err != nil {
		return err
	}

	if err = success(service); err != nil {
		return err
	}

	for {
		// read the dynamic settings
		var (
			level int64 = notify.LEVEL_DEFAULT
			mult  multiplier.Mult
		)
		if level, err = notify.Level(); err != nil {
			self.error(err, level, service)
		} else if mult, err = multiplier.Get(multiplier.FIVE_PERCENT); err != nil {
			self.error(err, level, service)
		} else
		// listen to the archived orders, look for newly filled orders, automatically place new LIMIT SELL orders.
		if archive, err = self.sell(client, mult, hold, earn, service, twitter, level, archive, sandbox); err != nil {
			self.error(err, level, service)
		} else
		// listen to the open orders, look for cancelled orders, send a notification.
		if open, err = self.listen(client, service, level, open); err != nil {
			self.error(err, level, service)
		}
	}
}

func (self *CexIo) Order(
	client interface{},
	side model.OrderSide,
	market string,
	size float64,
	price float64,
	kind model.OrderType,
	metadata string,
) (oid []byte, raw []byte, err error) {
	cexio, ok := client.(*exchange.Client)
	if !ok {
		return nil, nil, errors.New("invalid argument: client")
	}

	var symbol1 string
	var symbol2 string
	if symbol1, symbol2, err = self.decodePair(market); err != nil {
		return nil, nil, err
	}

	var order *exchange.Order
	if side == model.BUY {
		if order, err = cexio.PlaceOrder(symbol1, symbol2, exchange.BUY, size, price); err != nil {
			return nil, nil, err
		}
	} else if side == model.SELL {
		if order, err = cexio.PlaceOrder(symbol1, symbol2, exchange.SELL, size, price); err != nil {
			return nil, nil, err
		}
	}

	var out []byte
	if out, err = json.Marshal(order); err != nil {
		return nil, nil, err
	}

	return []byte(order.Id), out, nil
}

func (self *CexIo) StopLoss(client interface{}, market string, size float64, price float64, kind model.OrderType, metadata string) ([]byte, error) {
	return nil, errors.New("not implemented")
}

func (self *CexIo) OCO(client interface{}, market string, size float64, price, stop float64, metadata string) ([]byte, error) {
	return nil, errors.New("not implemented")
}

func (self *CexIo) GetClosed(client interface{}, market string) (model.Orders, error) {
	var err error

	cexio, ok := client.(*exchange.Client)
	if !ok {
		return nil, errors.New("invalid argument: client")
	}

	var symbol1 string
	var symbol2 string
	if symbol1, symbol2, err = self.decodePair(market); err != nil {
		return nil, err
	}

	var orders []exchange.Order
	if orders, err = cexio.ArchivedOrders(symbol1, symbol2); err != nil {
		return nil, err
	}

	var out model.Orders
	for _, order := range orders {
		out = append(out, model.Order{
			Side:   model.NewOrderSide(order.Type),
			Market: market,
			Size:   order.Amount,
			Price:  order.Price,
		})
	}

	return out, nil
}

func (self *CexIo) GetOpened(client interface{}, market string) (model.Orders, error) {
	var err error

	cexio, ok := client.(*exchange.Client)
	if !ok {
		return nil, errors.New("invalid argument: client")
	}

	var symbol1 string
	var symbol2 string
	if symbol1, symbol2, err = self.decodePair(market); err != nil {
		return nil, err
	}

	var orders []exchange.Order
	if orders, err = cexio.OpenOrders(symbol1, symbol2); err != nil {
		return nil, err
	}

	var out model.Orders
	for _, order := range orders {
		out = append(out, model.Order{
			Side:   model.NewOrderSide(order.Type),
			Market: market,
			Size:   order.Amount,
			Price:  order.Price,
		})
	}

	return out, nil
}

func (self *CexIo) GetBook(client interface{}, market string, side model.BookSide) (interface{}, error) {
	var err error

	cexio, ok := client.(*exchange.Client)
	if !ok {
		return nil, errors.New("invalid argument: client")
	}

	var symbol1 string
	var symbol2 string
	if symbol1, symbol2, err = self.decodePair(market); err != nil {
		return nil, err
	}

	var book *exchange.OrderBook
	if book, err = cexio.OrderBook(symbol1, symbol2); err != nil {
		return nil, err
	}

	var out []exchange.BookEntry
	if side == model.BOOK_SIDE_ASKS {
		out = book.Asks
	} else {
		out = book.Bids
	}

	return out, nil
}

func (self *CexIo) Aggregate(client, book interface{}, market string, agg float64) (model.Book, error) {
	bids, ok := book.([]exchange.BookEntry)
	if !ok {
		return nil, errors.New("invalid argument: book")
	}

	prec, err := self.GetPricePrec(client, market)
	if err != nil {
		return nil, err
	}

	var out model.Book
	for _, e := range bids {
		price := precision.Round(aggregation.Round(e.Price(), agg), prec)
		entry := out.EntryByPrice(price)
		if entry != nil {
			entry.Size = entry.Size + e.Size()
		} else {
			entry = &model.Buy{
				Market: market,
				Price:  price,
				Size:   e.Size(),
			}
			out = append(out, *entry)
		}

	}

	return out, nil
}

func (self *CexIo) GetTicker(client interface{}, market string) (float64, error) {
	cexio, ok := client.(*exchange.Client)
	if !ok {
		return 0, errors.New("invalid argument: client")
	}

	symbol1, symbol2, err := self.decodePair(market)
	if err != nil {
		return 0, err
	}

	ticker, err := cexio.Ticker(symbol1, symbol2)
	if err != nil {
		return 0, err
	}

	return ticker.Last, nil
}

func (self *CexIo) Get24h(client interface{}, market string) (*model.Stats, error) {
	cexio, ok := client.(*exchange.Client)
	if !ok {
		return nil, errors.New("invalid argument: client")
	}

	symbol1, symbol2, err := self.decodePair(market)
	if err != nil {
		return nil, err
	}

	ticker, err := cexio.Ticker(symbol1, symbol2)
	if err != nil {
		return nil, err
	}

	return &model.Stats{
		Market:    market,
		High:      ticker.High,
		Low:       ticker.Low,
		BtcVolume: 0,
	}, nil
}

// see: https://blog.cex.io/news/precision-and-minimum-order-size-change-for-certain-trading-pairs-20957
func (self *CexIo) GetPricePrec(client interface{}, market string) (int, error) {
	if out, ok := func() map[string]int {
		return map[string]int{
			"ADA-EUR":  6,
			"ADA-GBP":  6,
			"ADA-USD":  6,
			"ADA-USDT": 6,
			"ATOM-EUR": 4,
			"ATOM-GBP": 4,
			"ATOM-USD": 4,
			"BAT-EUR":  5,
			"BAT-GBP":  5,
			"BAT-USD":  5,
			"BCH-BTC":  6,
			"BCH-EUR":  2,
			"BCH-GBP":  2,
			"BCH-USD":  2,
			"BTC-EUR":  1,
			"BTC-GBP":  1,
			"BTC-RUB":  1,
			"BTC-USD":  1,
			"BTC-USDC": 1,
			"BTC-USDT": 1,
			"BTG-BTC":  6,
			"BTG-EUR":  3,
			"BTG-USD":  3,
			"BTT-BTC":  8,
			"BTT-EUR":  7,
			"BTT-USD":  7,
			"DASH-BTC": 6,
			"DASH-EUR": 3,
			"DASH-USD": 3,
			"ETH-BTC":  6,
			"ETH-EUR":  2,
			"ETH-GBP":  2,
			"ETH-USD":  2,
			"ETH-USDT": 2,
			"GAS-EUR":  4,
			"GAS-GBP":  4,
			"GAS-USD":  4,
			"GUSD-EUR": 4,
			"GUSD-USD": 4,
			"LTC-BTC":  8,
			"LTC-EUR":  3,
			"LTC-GBP":  3,
			"LTC-USD":  3,
			"LTC-USDT": 3,
			"MHC-BTC":  8,
			"MHC-ETH":  8,
			"MHC-EUR":  4,
			"MHC-GBP":  4,
			"MHC-USD":  4,
			"NEO-EUR":  4,
			"NEO-GBP":  4,
			"NEO-USD":  4,
			"OMG-BTC":  8,
			"OMG-EUR":  5,
			"OMG-USD":  5,
			"ONG-BTC":  8,
			"ONG-EUR":  5,
			"ONG-USD":  5,
			"ONT-BTC":  8,
			"ONT-EUR":  4,
			"ONT-USD":  4,
			"TRX-BTC":  8,
			"TRX-EUR":  6,
			"TRX-USD":  6,
			"USDC-USD": 4,
			"XLM-BTC":  8,
			"XLM-EUR":  5,
			"XLM-USD":  5,
			"XRP-BTC":  8,
			"XRP-EUR":  5,
			"XRP-GBP":  5,
			"XRP-USD":  5,
			"XRP-USDT": 5,
			"XTZ-EUR":  4,
			"XTZ-GBP":  4,
			"XTZ-USD":  4,
		}
	}()[market]; ok {
		return out, nil
	}
	return 0, nil
}

func (self *CexIo) GetSizePrec(client interface{}, market string) (int, error) {
	return 8, nil
}

func (self *CexIo) GetMaxSize(client interface{}, base, quote string, hold, earn bool, def float64, mult multiplier.Mult) float64 {
	return model.GetSizeMax(hold, earn, def, mult, func() int {
		prec, err := self.GetSizePrec(client, self.FormatMarket(base, quote))
		if err != nil {
			return 0
		}
		return prec
	})
}

func (self *CexIo) Cancel(client interface{}, market string, side model.OrderSide) error {
	var err error

	cexio, ok := client.(*exchange.Client)
	if !ok {
		return errors.New("invalid argument: client")
	}

	var symbol1 string
	var symbol2 string
	if symbol1, symbol2, err = self.decodePair(market); err != nil {
		return err
	}

	var orders []exchange.Order
	if orders, err = cexio.OpenOrders(symbol1, symbol2); err != nil {
		return err
	}

	for _, order := range orders {
		if ((side == model.BUY) && (order.Side() == exchange.BUY)) || ((side == model.SELL) && (order.Side() == exchange.SELL)) {
			if err = cexio.CancelOrder(order.Id); err != nil {
				return err
			}
		}
	}

	return nil
}

func (self *CexIo) Buy(client interface{}, cancel bool, market string, calls model.Calls, deviation float64, kind model.OrderType) error {
	var err error

	cexio, ok := client.(*exchange.Client)
	if !ok {
		return errors.New("invalid argument: client")
	}

	var symbol1 string
	var symbol2 string
	if symbol1, symbol2, err = self.decodePair(market); err != nil {
		return err
	}

	// step #1: delete the buy order(s) that are open in your book
	if cancel {
		var orders []exchange.Order
		if orders, err = cexio.OpenOrders(symbol1, symbol2); err != nil {
			return err
		}
		for _, order := range orders {
			side := order.Side()
			if side == exchange.BUY {
				// do not cancel orders that we're about to re-place
				index := calls.IndexByPrice(order.Price)
				if index > -1 {
					calls[index].Skip = true
				} else {
					if err = cexio.CancelOrder(order.Id); err != nil {
						return err
					}
				}
			}
		}
	}

	// step 2: open the top X buy orders
	for _, call := range calls {
		if !call.Skip {
			limit := call.Price
			if deviation != 1.0 {
				kind, limit = call.Deviate(self, client, kind, deviation)
			}
			if _, err = cexio.PlaceOrder(symbol1, symbol2, exchange.BUY, call.Size, limit); err != nil {
				return err
			}
		}
	}

	return nil
}

func (self *CexIo) IsLeveragedToken(name string) bool {
	return false
}

func (self *CexIo) HasAlgoOrder(client interface{}, market string) (bool, error) {
	return false, nil
}

func newCexIo() model.Exchange {
	return &CexIo{
		ExchangeInfo: &model.ExchangeInfo{
			Code: "CXIO",
			Name: "CEX.IO",
			URL:  "https://cex.io",
			REST: model.Endpoint{
				URI: exchange.Endpoint,
			},
			Country: "United Kingdom",
		},
	}
}
