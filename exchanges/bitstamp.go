//lint:file-ignore ST1006 receiver name should be a reflection of its identity; don't use generic names such as "this" or "self"
package exchanges

import (
	"encoding/json"
	"fmt"
	"log"
	"runtime"
	"strconv"
	"strings"
	"time"

	filemutex "github.com/alexflint/go-filemutex"
	"github.com/lintrieu/nefertiti/aggregation"
	exchange "github.com/lintrieu/nefertiti/bitstamp"
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
	bitstampMutex *filemutex.FileMutex
)

const (
	bitstampSessionFile = "bitstamp.time"
	bitstampSessionLock = "bitstamp.lock"
)

func init() {
	exchange.BeforeRequest = func(method, path string) error {
		var err error

		if bitstampMutex == nil {
			if bitstampMutex, err = filemutex.New(session.GetSessionFile(bitstampSessionLock)); err != nil {
				return err
			}
		}

		if err = bitstampMutex.Lock(); err != nil {
			return err
		}

		var lastRequest *time.Time
		if lastRequest, err = session.GetLastRequest(bitstampSessionFile); err != nil {
			return err
		}

		if lastRequest != nil {
			elapsed := time.Since(*lastRequest)
			if elapsed.Seconds() < (float64(1) / exchange.RequestsPerSecond) {
				sleep := time.Duration((float64(time.Second) / exchange.RequestsPerSecond) - float64(elapsed))
				if flag.Debug() {
					log.Printf("[DEBUG] sleeping %f seconds\n", sleep.Seconds())
				}
				time.Sleep(sleep)
			}
		}

		if flag.Debug() {
			log.Printf("[DEBUG] %s %s\n", method, path)
		}

		return nil
	}
	exchange.AfterRequest = func() {
		defer func() {
			bitstampMutex.Unlock()
		}()
		session.SetLastRequest(bitstampSessionFile, time.Now())
	}
}

type Bitstamp struct {
	*model.ExchangeInfo
}

func (self *Bitstamp) info(msg string, level int64, service model.Notify) {
	pc, file, line, _ := runtime.Caller(1)
	log.Printf("[INFO] %s %s", errors.FormatCaller(pc, file, line), msg)
	if service != nil {
		if notify.CanSend(level, notify.INFO) {
			err := service.SendMessage(msg, "Bitstamp - INFO", model.ALWAYS)
			if err != nil {
				log.Printf("[ERROR] %v", err)
			}
		}
	}
}

func (self *Bitstamp) error(err error, level int64, service model.Notify) {
	pc, file, line, _ := runtime.Caller(1)
	prefix := errors.FormatCaller(pc, file, line)

	msg := fmt.Sprintf("%s %v", prefix, err)
	_, ok := err.(*errors.Error)
	if ok && flag.Debug() {
		log.Printf("[ERROR] %s", err.(*errors.Error).ErrorStack(prefix, ""))
	} else {
		log.Printf("[ERROR] %s", msg)
	}

	if service != nil {
		if notify.CanSend(level, notify.ERROR) {
			err := service.SendMessage(msg, "Bitstamp - ERROR", model.ONCE_PER_MINUTE)
			if err != nil {
				log.Printf("[ERROR] %v", err)
			}
		}
	}
}

func (self *Bitstamp) GetInfo() *model.ExchangeInfo {
	return self.ExchangeInfo
}

func (self *Bitstamp) GetClient(permission model.Permission, sandbox bool) (interface{}, error) {
	if permission != model.PRIVATE {
		return exchange.New("", ""), nil
	}

	var (
		err       error
		apiKey    string
		apiSecret string
	)
	if apiKey, apiSecret, err = promptForApiKeys("Bitstamp"); err != nil {
		return nil, err
	}

	return exchange.New(apiKey, apiSecret), nil
}

func (self *Bitstamp) getMarket(client *exchange.Client, name string) (*exchange.Market, error) {
	cached := true
	for {
		markets, err := exchange.GetMarkets(client, cached)
		if err != nil {
			return nil, err
		}
		for _, market := range markets {
			if market.Name == name {
				return &market, nil
			}
		}
		if !cached {
			return nil, errors.Errorf("market %s does not exist", name)
		}
		cached = false
	}
}

func (self *Bitstamp) GetMarkets(cached, sandbox bool, blacklist []string) ([]model.Market, error) {
	var out []model.Market

	markets, err := exchange.GetMarkets(exchange.New("", ""), cached)

	if err != nil {
		return nil, err
	}

	for _, market := range markets {
		if market.Enabled && func() bool {
			for _, ignore := range blacklist {
				if strings.EqualFold(market.Name, ignore) {
					return false
				}
			}
			return true
		}() {
			out = append(out, model.Market{
				Name:  market.Name,
				Base:  market.Base,
				Quote: market.Quote,
			})
		}
	}

	return out, nil
}

func (self *Bitstamp) FormatMarket(base, quote string) string {
	return strings.ToLower(base + quote)
}

// listens to the open orders, look for cancelled orders, send a notification.
func (self *Bitstamp) listen(
	client *exchange.Client,
	service model.Notify,
	level int64,
	old exchange.Orders,
	transactions exchange.Transactions,
) (exchange.Orders, error) {
	var err error

	// get my new open orders
	var new exchange.Orders
	if new, err = client.GetOpenOrders(); err != nil {
		return old, err
	}

	// look for cancelled orders
	for _, order := range old {
		if new.IndexById(order.Id) == -1 {
			// if this order has NOT been FILLED, then it has been cancelled.
			if transactions.IndexByOrderId(order.Id) == -1 {
				var data []byte
				if data, err = json.Marshal(order); err != nil {
					return new, errors.Wrap(err, 1)
				}

				log.Println("[CANCELLED] " + string(data))

				side := order.Side()
				if side != "" && service != nil {
					if notify.CanSend(level, notify.CANCELLED) {
						if err = service.SendMessage(order, fmt.Sprintf("Bitstamp - Done %s (Reason: Cancelled)", strings.Title(side)), model.ALWAYS); err != nil {
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
				return new, errors.Wrap(err, 1)
			}

			log.Println("[OPEN] " + string(data))

			side := order.Side()
			if side != "" && service != nil {
				if notify.CanSend(level, notify.OPENED) || (level == notify.LEVEL_DEFAULT && side == exchange.SELL) {
					if err = service.SendMessage(order, ("Bitstamp - Open " + strings.Title(side)), model.ALWAYS); err != nil {
						log.Printf("[ERROR] %v", err)
					}
				}
			}
		}
	}

	return new, nil
}

// listens to the transaction history, look for newly filled orders, automatically place new LIMIT SELL orders.
func (self *Bitstamp) sell(
	client *exchange.Client,
	mult multiplier.Mult,
	hold, earn model.Markets,
	service model.Notify,
	twitter *notify.TwitterKeys,
	level int64,
	old exchange.Transactions,
	sandbox bool,
) (exchange.Transactions, error) {
	var (
		err     error
		markets []model.Market
	)

	if markets, err = self.GetMarkets(false, sandbox, nil); err != nil {
		return old, err
	}

	// get my new transaction history
	var new exchange.Transactions
	for _, market := range markets {
	outer:
		for {
			var txn exchange.Transactions
			if txn, err = client.GetUserTransactions(market.Name); err != nil {
				return old, err
			}
			if len(old) == 0 {
				new = append(new, txn...)
				break
			} else {
				this := txn.GetOrders()
				prev := old.GetOrdersEx(client, market.Name)
				if len(this) >= len(prev) {
					if len(prev) == 0 {
						new = append(new, txn...)
						break
					} else {
						for _, order := range prev {
							if this.IndexByOrderId(order.OrderId()) > -1 {
								new = append(new, txn...)
								break outer
							}
						}
					}
				}
				log.Printf("[WARN] user_transactions/%s returned %d orders, expected at least %d.", market.Name, len(this), len(prev))
			}
		}
	}

	if len(old) > 0 {
		prev := old.GetOrders()
		this := new.GetOrders()
		if len(this) >= len(prev) {
			if len(prev) == 0 {
				goto WeAreGood
			} else {
				for _, order := range prev {
					if this.IndexByOrderId(order.OrderId()) > -1 {
						goto WeAreGood
					}
				}
			}
		}
		goto WhatTheFuck
	WhatTheFuck:
		return old, errors.Errorf("/user_transactions returned %d orders, expected at least %d.", len(this), len(prev))
	WeAreGood:
		// nothing to see here, carry on
	}

	// make a list of newly filled orders
	var orders []exchange.Transaction
	for _, transaction := range new {
		if transaction.OrderId() != "" {
			if old.IndexByOrderId(transaction.OrderId()) == -1 {
				orders = append(orders, transaction)
			}
		}
	}

	// send notification(s)
	for _, order := range orders {
		var data []byte
		if data, err = json.Marshal(order); err != nil {
			self.error(err, level, service)
		} else {
			log.Println("[FILLED] " + string(data))
			if notify.CanSend(level, notify.FILLED) {
				var side string
				if side, err = order.Side(client); err != nil {
					self.error(err, level, service)
				} else {
					if service != nil {
						if err = service.SendMessage(order, fmt.Sprintf("Bitstamp - Done %s (Reason: Filled %f qty)", strings.Title(side), order.Amount(client)), model.ALWAYS); err != nil {
							log.Printf("[ERROR] %v", err)
						}
					}
					if twitter != nil {
						notify.Tweet(twitter, fmt.Sprintf("Done %s. %s priced at %f #Bitstamp", strings.Title(side), model.TweetMarket(markets, order.Market(client)), order.Price(client)))
					}
				}
			}
		}
	}

	// has a buy order been filled? then place a sell order
	for i := 0; i < len(orders); i++ {
		this, _ := orders[i].Side(client)
		if this == exchange.BUY {
			qty := orders[i].Amount(client)

			// add up amount(s), hereby preventing a problem with partial matches
			n := i + 1
			for n < len(orders) {
				that, _ := orders[n].Side(client)
				if orders[n].Market(client) == orders[i].Market(client) && (that == this) && orders[n].Price(client) == orders[i].Price(client) {
					qty = qty + orders[n].Amount(client)
					orders = append(orders[:n], orders[n+1:]...)
				} else {
					n++
				}
			}

			var sp int
			if sp, err = self.GetSizePrec(client, orders[i].Market(client)); err != nil {
				return old, err
			} else {
				qty = precision.Floor(qty, sp)
			}

			// get base currency and desired size, calculate price, place sell order
			var (
				base  string
				quote string
			)
			base, quote, err = model.ParseMarket(markets, orders[i].Market(client))
			if err == nil {
				qty = self.GetMaxSize(client, base, quote, hold.HasMarket(orders[i].Market(client)), earn.HasMarket(orders[i].Market(client)), qty, mult)
				if qty > 0 {
					var pp int
					if pp, err = self.GetPricePrec(client, orders[i].Market(client)); err == nil {
						attempts := 0
						for {
							_, err = client.SellLimitOrder(
								orders[i].Market(client),
								qty,
								pricing.Multiply(orders[i].Price(client), mult, pp),
							)
							if err != nil && strings.Contains(err.Error(), "Order could not be placed") {
								attempts++
								if attempts >= 10 {
									break
								}
							} else {
								break
							}
						}
					}
				}
			}

			if err != nil {
				var data []byte
				if data, _ = json.Marshal(orders[i]); data == nil {
					self.error(err, level, service)
				} else {
					self.error(errors.Append(err, "\t", string(data)), level, service)
				}
			}
		}
	}

	return new, nil
}

func (self *Bitstamp) Sell(
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
	)
	if apiKey, apiSecret, err = promptForApiKeys("Bitstamp"); err != nil {
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

	client := exchange.New(apiKey, apiSecret)

	// get my open orders
	var open []exchange.Order
	if open, err = client.GetOpenOrders(); err != nil {
		return err
	}

	// get my transaction history
	var (
		markets      []exchange.Market
		transactions exchange.Transactions
	)
	if markets, err = exchange.GetMarkets(client, true); err != nil {
		return err
	}
	for _, market := range markets {
		var txn exchange.Transactions
		if txn, err = client.GetUserTransactions(market.Name); err != nil {
			return err
		}
		transactions = append(transactions, txn...)
	}

	if err = success(service); err != nil {
		return err
	}

	// we won't be re-buying *unless* your most recent (non-sold) sell is older than 14 days
	reboughtAt := time.Now()
	const rebuyAfterDays = 14

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
		// listens to the transaction history, look for newly filled orders, automatically place new LIMIT SELL orders.
		if transactions, err = self.sell(client, mult, hold, earn, service, twitter, level, transactions, sandbox); err != nil {
			self.error(err, level, service)
		} else
		// listens to the open orders, look for cancelled orders, send a notification.
		if open, err = self.listen(client, service, level, open, transactions); err != nil {
			self.error(err, level, service)
		} else
		// follow up on the "aggressive" strategy
		if flag.Dca() && time.Since(reboughtAt).Minutes() > 60 {
			var markets []exchange.Market
			if markets, err = exchange.GetMarkets(client, true); err != nil {
				self.error(err, level, service)
			} else {
				for _, market := range markets {
					youngest := time.Time{} // January 1, year 1, 00:00:00.000000000 UTC

					for _, order := range open {
						side := model.NewOrderSide(order.Side())
						if side == model.SELL {
							if order.MarketEx() == market.Name {
								createdAt := order.GetDateTimeEx()
								if youngest.IsZero() || youngest.Before(createdAt) {
									youngest = createdAt
								}
							}
						}
					}

					if !youngest.IsZero() && time.Since(youngest).Hours() > 24*rebuyAfterDays {
						// did we recently sell an "aggressive" order on this market? then prevent us from buying this pump.
						var closed model.Orders
						if closed, err = self.GetClosed(client, market.Name); err != nil {
							self.error(err, level, service)
						} else {
							if time.Since(closed.Youngest(model.SELL, time.Now())).Hours() < 24*rebuyAfterDays {
								// continue
							} else {
								self.info(fmt.Sprintf(
									"Re-buying %s because your latest activity on this market (at %s) is older than %d days.",
									market.Name, youngest.Format(time.RFC1123), rebuyAfterDays,
								), level, service)
								var ticker float64
								if ticker, err = self.GetTicker(client, market.Name); err != nil {
									self.error(err, level, service)
								} else {
									var precSize int
									if precSize, err = self.GetSizePrec(client, market.Name); err != nil {
										self.error(err, level, service)
									} else {
										for {
											var qty float64
											if qty, err = exchange.GetMinOrderSize(client, market.Name, ticker, precSize); err != nil {
												self.error(err, level, service)
											} else {
												if hold.HasMarket(market.Name) {
													qty = qty * 5
												}
												if _, err = client.BuyMarketOrder(market.Name, precision.Round(qty, precSize)); err != nil {
													// --- BEGIN --- svanas 2020-09-15 --- error: Minimum order size is ... -----------
													if strings.Contains(err.Error(), "Minimum order size") {
														lower, _ := strconv.ParseFloat(precision.Format(precSize), 64)
														ticker = ticker - lower
														continue
													}
													// ---- END ---- svanas 2020-09-15 ------------------------------------------------
													self.error(err, level, service)
												}
											}
											break
										}
									}
								}
							}
						}
					}
				}
			}
			reboughtAt = time.Now()
		}
	}
}

func (self *Bitstamp) Order(
	client interface{},
	side model.OrderSide,
	market string,
	size float64,
	price float64,
	kind model.OrderType,
	metadata string,
) (oid []byte, raw []byte, err error) {
	bitstamp, ok := client.(*exchange.Client)
	if !ok {
		return nil, nil, errors.New("invalid argument: client")
	}

	var order *exchange.Order
	if side == model.BUY {
		if order, err = bitstamp.BuyLimitOrder(market, size, price); err != nil {
			return nil, nil, err
		}
	} else if side == model.SELL {
		if order, err = bitstamp.SellLimitOrder(market, size, price); err != nil {
			return nil, nil, err
		}
	}

	var out []byte
	if out, err = json.Marshal(order); err != nil {
		return nil, nil, errors.Wrap(err, 1)
	}

	return []byte(order.Id), out, nil
}

func (self *Bitstamp) StopLoss(client interface{}, market string, size float64, price float64, kind model.OrderType, metadata string) ([]byte, error) {
	return nil, errors.New("not implemented")
}

func (self *Bitstamp) OCO(client interface{}, market string, size float64, price, stop float64, metadata string) ([]byte, error) {
	return nil, errors.New("not implemented")
}

func (self *Bitstamp) GetClosed(client interface{}, market string) (model.Orders, error) {
	var err error

	bitstamp, ok := client.(*exchange.Client)
	if !ok {
		return nil, errors.New("invalid argument: client")
	}

	var transactions []exchange.Transaction
	if transactions, err = bitstamp.GetUserTransactions(market); err != nil {
		return nil, err
	}

	var out model.Orders
	for _, transaction := range transactions {
		if transaction.OrderId() != "" {
			side, _ := transaction.Side(bitstamp)
			out = append(out, model.Order{
				Side:      model.NewOrderSide(side),
				Market:    transaction.Market(bitstamp),
				Size:      transaction.Amount(bitstamp),
				Price:     transaction.Price(bitstamp),
				CreatedAt: transaction.DateTime(),
			})
		}
	}

	return out, nil
}

func (self *Bitstamp) GetOpened(client interface{}, market string) (model.Orders, error) {
	var err error

	bitstamp, ok := client.(*exchange.Client)
	if !ok {
		return nil, errors.New("invalid argument: client")
	}

	var orders []exchange.Order
	if orders, err = bitstamp.GetOpenOrdersEx(market); err != nil {
		return nil, err
	}

	var out model.Orders
	for _, order := range orders {
		out = append(out, model.Order{
			Side:   model.NewOrderSide(order.Side()),
			Market: market,
			Size:   order.Amount,
			Price:  order.Price,
		})
	}

	return out, nil
}

func (self *Bitstamp) GetBook(client interface{}, market string, side model.BookSide) (interface{}, error) {
	var err error

	bitstamp, ok := client.(*exchange.Client)
	if !ok {
		return nil, errors.New("invalid argument: client")
	}

	var book *exchange.OrderBook
	if book, err = bitstamp.OrderBook(market); err != nil {
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

func (self *Bitstamp) Aggregate(client, book interface{}, market string, agg float64) (model.Book, error) {
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
			entry =
				&model.Buy{
					Market: market,
					Price:  price,
					Size:   e.Size(),
				}
			out = append(out, *entry)
		}
	}

	return out, nil
}

func (self *Bitstamp) GetTicker(client interface{}, market string) (float64, error) {
	bitstamp, ok := client.(*exchange.Client)
	if !ok {
		return 0, errors.New("invalid argument: client")
	}

	ticker, err := bitstamp.Ticker(market)
	if err != nil {
		return 0, err
	}

	return ticker.Last, nil
}

func (self *Bitstamp) Get24h(client interface{}, market string) (*model.Stats, error) {
	bitstamp, ok := client.(*exchange.Client)
	if !ok {
		return nil, errors.New("invalid argument: client")
	}

	ticker, err := bitstamp.Ticker(market)
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

func (self *Bitstamp) GetPricePrec(client interface{}, marketName string) (int, error) {
	bitstamp, ok := client.(*exchange.Client)
	if !ok {
		return 0, errors.New("invalid argument: client")
	}
	market, err := self.getMarket(bitstamp, marketName)
	if err != nil {
		return 0, err
	}
	return market.PricePrec, nil
}

func (self *Bitstamp) GetSizePrec(client interface{}, marketName string) (int, error) {
	bitstamp, ok := client.(*exchange.Client)
	if !ok {
		return 0, errors.New("invalid argument: client")
	}
	market, err := self.getMarket(bitstamp, marketName)
	if err != nil {
		return 0, err
	}
	return market.SizePrec, nil
}

func (self *Bitstamp) GetMaxSize(client interface{}, base, quote string, hold, earn bool, def float64, mult multiplier.Mult) float64 {
	market := self.FormatMarket(base, quote)

	fn := func() int {
		prec, err := self.GetSizePrec(client, market)
		if err != nil {
			return 0
		}
		return prec
	}

	out := model.GetSizeMax(hold, earn, def, mult, fn)

	if hold {
		ticker, err := self.GetTicker(client, market)
		if err == nil {
			bitstamp, ok := client.(*exchange.Client)
			if ok {
				min, err := exchange.GetMinimumOrder(bitstamp, market)
				if err == nil {
					if (out * ticker) >= min {
						// we are good
					} else {
						stats, err := self.Get24h(client, market)
						if err == nil {
							qty := (min / stats.Low)
							if qty > out {
								out = precision.Round(qty, fn())
							}
						}
					}
				}
			}
		}
	}

	return out
}

func (self *Bitstamp) Cancel(client interface{}, market string, side model.OrderSide) error {
	var err error

	bitstamp, ok := client.(*exchange.Client)
	if !ok {
		return errors.New("invalid argument: client")
	}

	var orders []exchange.Order
	if orders, err = bitstamp.GetOpenOrdersEx(market); err != nil {
		return err
	}

	for _, order := range orders {
		if ((side == model.BUY) && (order.Side() == exchange.BUY)) || ((side == model.SELL) && (order.Side() == exchange.SELL)) {
			if err = bitstamp.CancelOrder(order.Id); err != nil {
				return err
			}
		}
	}

	return nil
}

func (self *Bitstamp) Buy(client interface{}, cancel bool, market string, calls model.Calls, deviation float64, kind model.OrderType) error {
	var err error

	bitstamp, ok := client.(*exchange.Client)
	if !ok {
		return errors.New("invalid argument: client")
	}

	// step #1: delete the buy order(s) that are open in your book
	if cancel {
		var orders []exchange.Order
		if orders, err = bitstamp.GetOpenOrdersEx(market); err != nil {
			return err
		}
		for _, order := range orders {
			side := order.Side()
			if side == exchange.BUY {
				// do not cancel orders that we're about to re-place
				index := calls.IndexByPrice(order.Price)
				if index > -1 && order.Amount == calls[index].Size {
					calls[index].Skip = true
				} else {
					if err = bitstamp.CancelOrder(order.Id); err != nil {
						return err
					}
				}
			}
		}
	}

	// step 2: open the top X buy orders
	for _, call := range calls {
		if !call.Skip {
			var (
				min   float64
				qty   float64 = call.Size
				limit float64 = call.Price
			)
			if deviation != 1.0 {
				kind, limit = call.Deviate(self, client, kind, deviation)
			}
			// --- BEGIN --- svanas 2020-01-06 --- Minimum order size is 25.0 EUR ---
			if min, err = exchange.GetMinimumOrder(bitstamp, market); err != nil {
				return err
			}
			if min > 0 {
				if limit == 0 {
					if limit, err = self.GetTicker(client, market); err != nil {
						return err
					}
				}
				if (qty * limit) < min {
					var prec int
					if prec, err = self.GetSizePrec(client, market); err != nil {
						return err
					}
					qty = precision.Ceil((min / limit), prec)
				}
			}
			// ---- END ---- svanas 2020-01-06 --------------------------------------
			attempts := 0
			for {
				_, err = bitstamp.BuyLimitOrder(market, qty, limit)
				if err == nil {
					break
				} else {
					if strings.Contains(err.Error(), "Order could not be placed") {
						attempts++
						if attempts >= 10 {
							self.error(err, notify.LEVEL_DEFAULT, nil)
							break
						}
					} else {
						return err
					}
				}
			}
		}
	}

	return nil
}

func (self *Bitstamp) IsLeveragedToken(name string) bool {
	return false
}

func (self *Bitstamp) HasAlgoOrder(client interface{}, market string) (bool, error) {
	return false, nil
}

func newBitstamp() model.Exchange {
	return &Bitstamp{
		ExchangeInfo: &model.ExchangeInfo{
			Code: "BITS",
			Name: "Bitstamp",
			URL:  "https://www.bitstamp.net",
			REST: model.Endpoint{
				URI: exchange.Endpoint,
			},
			Country: "Luxembourg",
		},
	}
}
