//lint:file-ignore ST1006 receiver name should be a reflection of its identity; don't use generic names such as "this" or "self"
package exchanges

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"runtime"
	"strconv"
	"strings"
	"time"

	filemutex "github.com/alexflint/go-filemutex"
	ws "github.com/gorilla/websocket"
	"github.com/lintrieu/nefertiti/aggregation"
	"github.com/lintrieu/nefertiti/errors"
	"github.com/lintrieu/nefertiti/flag"
	"github.com/lintrieu/nefertiti/gdax"
	"github.com/lintrieu/nefertiti/model"
	"github.com/lintrieu/nefertiti/multiplier"
	"github.com/lintrieu/nefertiti/notify"
	"github.com/lintrieu/nefertiti/precision"
	"github.com/lintrieu/nefertiti/pricing"
	"github.com/lintrieu/nefertiti/session"
	exchange "github.com/svanas/go-coinbasepro"
)

var (
	gdaxMutex *filemutex.FileMutex
)

const (
	gdaxSessionFile = "gdax.time"
	gdaxSessionLock = "gdax.lock"
)

func init() {
	exchange.BeforeRequest = func(client *exchange.Client, method, endpoint string) error {
		var err error

		if gdaxMutex == nil {
			if gdaxMutex, err = filemutex.New(session.GetSessionFile(gdaxSessionLock)); err != nil {
				return err
			}
		}

		if err = gdaxMutex.Lock(); err != nil {
			return err
		}

		var lastRequest *time.Time
		if lastRequest, err = session.GetLastRequest(gdaxSessionFile); err != nil {
			return err
		}

		if lastRequest != nil {
			elapsed := time.Since(*lastRequest)
			if elapsed.Seconds() < (float64(1) / exchange.RequestsPerSecond) {
				sleep := time.Duration((float64(time.Second) / exchange.RequestsPerSecond)) - elapsed
				if flag.Debug() {
					log.Printf("[DEBUG] sleeping %f seconds", sleep.Seconds())
				}
				time.Sleep(sleep)
			}
		}

		if flag.Debug() {
			log.Printf("[DEBUG] %s %s", method, endpoint)
		}

		return nil
	}
	exchange.AfterRequest = func() {
		defer func() {
			gdaxMutex.Unlock()
		}()
		session.SetLastRequest(gdaxSessionFile, time.Now())
	}
}

func canNotify(level int64, msg *gdax.Message) bool {
	switch level {
	case notify.LEVEL_NOTHING:
		return false
	case notify.LEVEL_ERRORS:
		return msg.GetType() == gdax.MESSAGE_ERROR
	case notify.LEVEL_VERBOSE:
		return true
	}
	mt := msg.GetType()
	return (mt == gdax.MESSAGE_ERROR) ||
		(mt == gdax.MESSAGE_ACTIVATE) ||
		(mt == gdax.MESSAGE_OPEN && model.NewOrderSide(msg.Side) == model.SELL) ||
		(mt == gdax.MESSAGE_DONE && msg.GetReason() == gdax.REASON_FILLED)
}

type Gdax struct {
	*model.ExchangeInfo
	products []exchange.Product
}

func (self *Gdax) error(err error, level int64, service model.Notify) {
	pc, file, line, _ := runtime.Caller(1)
	str := err.Error()
	log.Printf("[ERROR] %s %s", errors.FormatCaller(pc, file, line), strings.Replace(str, "\n\n", " ", -1))
	if service != nil {
		if notify.CanSend(level, notify.ERROR) {
			service.SendMessage(str, "Coinbase Pro - ERROR", model.ONCE_PER_MINUTE)
		}
	}
}

func (self *Gdax) getMinOrderSize(client *gdax.Client, market string) (float64, error) {
	cached := true
	for {
		products, err := self.getProducts(client, cached)

		if err != nil {
			return 0, err
		}

		for _, product := range products {
			if product.ID == market {
				out, err := gdax.GetMinOrderSize(&product)
				if err != nil {
					return 0, err
				} else {
					return out, nil
				}
			}
		}

		if cached {
			cached = false
		} else {
			return 0, errors.Errorf("market %s does not exist", market)
		}
	}
}

func (self *Gdax) GetInfo() *model.ExchangeInfo {
	return self.ExchangeInfo
}

func (self *Gdax) getClient(apiKey, apiSecret, apiPassphrase string, sandbox bool) *gdax.Client {
	client := gdax.New(sandbox)

	client.UpdateConfig(&exchange.ClientConfig{
		Key:        apiKey,
		Passphrase: apiPassphrase,
		Secret:     apiSecret,
	})

	return client
}

func (self *Gdax) GetClient(permission model.Permission, sandbox bool) (interface{}, error) {
	if permission != model.PRIVATE {
		return gdax.New(sandbox), nil
	}

	var (
		err           error
		apiKey        string
		apiSecret     string
		apiPassphrase string
	)
	if apiKey, apiSecret, apiPassphrase, err = promptForApiKeysEx("Coinbase Pro"); err != nil {
		return nil, err
	}

	return self.getClient(apiKey, apiSecret, apiPassphrase, sandbox), nil
}

func (self *Gdax) getProducts(client interface{}, cached bool) ([]exchange.Product, error) {
	if self.products == nil || !cached {
		gdaxClient, ok := client.(*gdax.Client)
		if !ok {
			return nil, errors.New("invalid argument: client")
		}
		var err error
		if self.products, err = gdaxClient.GetProducts(); err != nil {
			return nil, err
		}
	}
	return self.products, nil
}

func (self *Gdax) GetMarkets(cached, sandbox bool, blacklist []string) ([]model.Market, error) {
	var out []model.Market

	products, err := self.getProducts(gdax.New(sandbox), cached)

	if err != nil {
		return nil, err
	}

	for _, product := range products {
		if func() bool {
			for _, ignore := range blacklist {
				if strings.EqualFold(product.ID, ignore) {
					return false
				}
			}
			return true
		}() {
			out = append(out, model.Market{
				Name:  product.ID,
				Base:  product.BaseCurrency,
				Quote: product.QuoteCurrency,
			})
		}
	}

	return out, nil
}

func (self *Gdax) FormatMarket(base, quote string) string {
	return fmt.Sprintf("%s-%s", base, quote)
}

func (self *Gdax) sell(
	hold, earn model.Markets,
	apiSecret string,
	apiKey string,
	apiPassphrase string,
	gdaxUserId string,
	service model.Notify,
	twitter *notify.TwitterKeys,
	sandbox bool,
	debug bool,
	success model.OnSuccess,
) error {
	var err error

	URI := self.ExchangeInfo.WebSocket.URI
	if sandbox {
		URI = self.ExchangeInfo.WebSocket.Sandbox
	}

	var level int64 = notify.LEVEL_DEFAULT
	if level, err = notify.Level(); err != nil {
		return err
	}

	var markets []model.Market
	if markets, err = self.GetMarkets(true, sandbox, nil); err != nil {
		return err
	}

	var (
		conn *ws.Conn
		init func() (*ws.Conn, error)
	)

	init = func() (out *ws.Conn, err error) {
		if out, _, err = ws.DefaultDialer.Dial(URI, nil); err != nil {
			return nil, errors.Wrap(err, 1)
		}
		var sub *gdaxSubscribePrivate
		if sub, err = self.newSubscribePrivate(apiSecret, apiKey, apiPassphrase, sandbox); err != nil {
			return nil, err
		}
		if err = out.WriteJSON(sub); err != nil {
			return nil, errors.Wrap(err, 1)
		}
		log.Printf("[INFO] Listening to %s (user: %s)\n", URI, gdaxUserId)
		return out, nil
	}

	if conn, err = init(); err != nil {
		return err
	}

	if err = success(service); err != nil {
		return err
	}

	lastInterval := time.Now()
	const intervalMinutes = 1

	for {
		var data []byte
		_, data, err = conn.ReadMessage()
		if err != nil {
			// do we have a close error?
			ce, ok := err.(*ws.CloseError)
			if ok && ce.Code != ws.CloseNormalClosure {
				// restart the connection on abnormal closure
				log.Printf("[ERROR] %v", err)
				time.Sleep(5 * time.Second)
				conn, err = init()
				if err != nil {
					self.error(err, level, service)
					return err
				}
			} else {
				// read: connection reset by peer?
				if strings.Contains(err.Error(), "connection reset by peer") || strings.Contains(err.Error(), "operation timed out") {
					for {
						log.Printf("[ERROR] %v", err)
						time.Sleep(5 * time.Second)
						conn, err = init()
						if err == nil {
							break
						} else {
							if !strings.Contains(err.Error(), "connection reset by peer") && !strings.Contains(err.Error(), "operation timed out") {
								self.error(err, level, service)
								return err
							}
						}
					}
				} else {
					// exit the websocket loop
					self.error(err, level, service)
					return err
				}
			}
		} else {
			msg := gdax.Message{}
			if err = json.Unmarshal(data, &msg); err != nil {
				self.error(errors.Errorf("%s. Message: %s", err.Error(), string(data)), level, service)
				return err
			}
			if msg.GetType() == gdax.MESSAGE_ERROR {
				self.error(errors.Errorf("%s. Reason: %s", msg.Message.Message, msg.Reason), level, service)
				return err
			}
			if msg.UserID == gdaxUserId {
				log.Printf("[INFO] %s", string(data))
				mt := msg.GetType()
				// send a notification if type != ["received"|"match"]
				if mt != gdax.MESSAGE_RECEIVED && mt != gdax.MESSAGE_MATCH {
					if canNotify(level, &msg) {
						if service != nil {
							if err = service.SendMessage(msg, msg.Title(), model.ALWAYS); err != nil {
								log.Printf("[ERROR] %v", err)
							}
						}
						if twitter != nil {
							if mt == gdax.MESSAGE_DONE && msg.GetReason() == gdax.REASON_FILLED {
								notify.Tweet(twitter, fmt.Sprintf("Done %s. %s priced at %s #CoinbasePro", strings.Title(msg.Side), model.TweetMarket(markets, msg.ProductID), msg.Price))
							}
						}
					}
				}
				// has a buy order been filled? then place a sell order
				if mt == gdax.MESSAGE_DONE {
					mr := msg.GetReason()
					if mr == gdax.REASON_FILLED {
						side := model.NewOrderSide(msg.Side)
						if side == model.BUY {
							client := self.getClient(apiKey, apiSecret, apiPassphrase, sandbox)

							price := gdax.ParseFloat(msg.Price)
							if price == 0 {
								if price, err = self.GetTicker(client, msg.ProductID); err != nil {
									self.error(err, level, service)
								}
							}

							var old *gdax.Order
							if old, err = client.GetOrder(msg.OrderID); err != nil {
								self.error(errors.Wrap(err, 1), level, service)
							} else {
								qty := old.GetSize()
								if qty == 0 {
									if qty, err = self.getMinOrderSize(client, msg.ProductID); err != nil {
										self.error(err, level, service)
									}
									qty = qty * 5
								}

								var (
									base  string
									quote string
								)
								if base, quote, err = model.ParseMarket(markets, msg.ProductID); err != nil {
									if markets, err = self.GetMarkets(false, sandbox, nil); err == nil {
										base, quote, err = model.ParseMarket(markets, msg.ProductID)
									}
									if err != nil {
										self.error(err, level, service)
									}
								}

								var prec int
								if prec, err = self.GetPricePrec(client, msg.ProductID); err != nil {
									self.error(err, level, service)
								}

								var mult multiplier.Mult
								if mult, err = multiplier.Get(multiplier.FIVE_PERCENT); err != nil {
									self.error(err, level, service)
								}

								// by default, we will sell at a 5% profit

								order := (&gdax.Order{
									Order: &exchange.Order{
										Type:      model.OrderTypeString[model.LIMIT],
										Side:      model.OrderSideString[model.SELL],
										ProductID: msg.ProductID,
									},
								}).
									SetSize(self.GetMaxSize(client, base, quote, hold.HasMarket(msg.ProductID), earn.HasMarket(msg.ProductID), qty, mult)).
									SetPrice(pricing.Multiply(price, mult, prec))

								// log the newly created SELL order
								var raw []byte
								if raw, err = json.Marshal(order); err == nil {
									log.Println("[INFO] " + string(raw))
									if service != nil {
										if notify.CanSend(level, notify.INFO) {
											service.SendMessage(order, "Coinbase Pro - New Sell", model.ALWAYS)
										}
									}
								}

								if _, err = client.CreateOrder(order); err != nil {
									self.error(errors.Wrap(err, 1), level, service)
								}
							}
						}
					}
				}
			}
		}

		if time.Since(lastInterval).Minutes() > intervalMinutes {
			var (
				cursor *exchange.Cursor
				orders []gdax.Order
				client *gdax.Client = self.getClient(apiKey, apiSecret, apiPassphrase, sandbox)
			)

			// follow up on the "aggressive" strategy
			if flag.Dca() {
				// we won't be re-buying *unless* your most recent (non-sold) sell is older than 14 days
				const rebuyAfterDays = 14
				var open []gdax.Order

				cursor = client.ListOrders(exchange.ListOrdersParams{Status: "open"})
				for cursor.HasMore {
					if err = cursor.NextPage(&orders); err != nil {
						break
					} else {
						open = append(open, orders...)
					}
				}

				if err != nil {
					self.error(errors.Wrap(err, 1), level, service)
				} else {
					var products []exchange.Product
					if products, err = self.getProducts(client, true); err != nil {
						self.error(err, level, service)
					} else {
						for _, product := range products {
							if product.LimitOnly {
								// ignore this market because it is limit-only
							} else {
								youngest := time.Time{} // January 1, year 1, 00:00:00.000000000 UTC

								for _, order := range open {
									side := model.NewOrderSide(order.Side)
									if side == model.SELL {
										if order.ProductID == product.ID {
											createdAt := order.CreatedAt.Time()
											if youngest.IsZero() || youngest.Before(createdAt) {
												youngest = createdAt
											}
										}
									}
								}

								if !youngest.IsZero() && time.Since(youngest).Hours() > 24*rebuyAfterDays {
									// did we recently sell an "aggressive" order on this market? then prevent us from buying this pump.
									var closed model.Orders
									if closed, err = self.GetClosed(client, product.ID); err != nil {
										self.error(err, level, service)
									} else {
										if time.Since(closed.Youngest(model.SELL, time.Now())).Hours() < 24*rebuyAfterDays {
											// continue
										} else {
											msg := fmt.Sprintf(
												"Re-buying %s because your latest activity on this market (at %s) is older than %d days.",
												product.ID, youngest.Format(time.RFC1123), rebuyAfterDays,
											)

											log.Println("[INFO] " + msg)
											if service != nil {
												if notify.CanSend(level, notify.INFO) {
													service.SendMessage(msg, "Coinbase Pro - INFO", model.ALWAYS)
												}
											}

											var qty float64
											if qty, err = gdax.GetMinOrderSize(&product); err != nil {
												self.error(err, level, service)
											} else {
												if hold.HasMarket(product.ID) {
													qty = qty * 5
												}

												order := (&gdax.Order{
													Order: &exchange.Order{
														Type:      model.OrderTypeString[model.MARKET],
														Side:      model.OrderSideString[model.BUY],
														ProductID: product.ID,
													},
												}).SetSize(qty)

												// log the newly created BUY order
												var raw []byte
												if raw, err = json.Marshal(order); err == nil {
													log.Println("[INFO] " + string(raw))
													if service != nil {
														if notify.CanSend(level, notify.INFO) {
															service.SendMessage(order, "Coinbase Pro - New Buy", model.ALWAYS)
														}
													}
												}

												if _, err = client.CreateOrder(order); err != nil {
													self.error(errors.Wrap(err, 1), level, service)
												}
											}
										}
									}
								}
							}
						}
					}
				}
			}
			lastInterval = time.Now()
		}
	}
}

func (self *Gdax) Sell(
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
		err           error
		apiSecret     string
		apiKey        string
		apiPassphrase string
		apiUserId     string
	)
	if apiKey, apiSecret, apiPassphrase, err = promptForApiKeysEx("Coinbase Pro"); err != nil {
		return err
	}

	apiUserId = flag.Get("api-user-id").String()
	if apiUserId == "" {
		// get the GDAX user ID
		client := self.getClient(apiKey, apiSecret, apiPassphrase, sandbox)
		var me *gdax.Me
		if me, err = client.GetMe(); err != nil {
			return errors.Wrap(err, 1)
		}
		apiUserId = me.ID
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

	return self.sell(hold, earn, apiSecret, apiKey, apiPassphrase, apiUserId, service, twitter, sandbox, debug, success)
}

func (self *Gdax) Order(
	client interface{},
	side model.OrderSide,
	market string,
	size float64,
	price float64,
	kind model.OrderType,
	metadata string,
) (oid []byte, raw []byte, err error) {
	gdaxClient, ok := client.(*gdax.Client)
	if !ok {
		return nil, nil, errors.New("invalid argument: client")
	}

	order := (&gdax.Order{
		Order: &exchange.Order{
			Type:      model.OrderTypeString[model.LIMIT],
			Side:      model.OrderSideString[side],
			ProductID: market,
		},
	}).SetSize(size).SetPrice(price)

	var saved *gdax.Order
	if saved, err = gdaxClient.CreateOrder(order); err != nil {
		return nil, nil, errors.Wrap(err, 1)
	}

	var out []byte
	if out, err = json.Marshal(saved); err != nil {
		return nil, nil, errors.Wrap(err, 1)
	}

	return []byte(saved.ID), out, nil
}

func (self *Gdax) StopLoss(client interface{}, market string, size float64, price float64, kind model.OrderType, metadata string) ([]byte, error) {
	var err error

	gdaxClient, ok := client.(*gdax.Client)
	if !ok {
		return nil, errors.New("invalid argument: client")
	}

	order := (&gdax.Order{
		Order: &exchange.Order{
			Type:      model.OrderTypeString[kind],
			Side:      model.OrderSideString[model.SELL],
			ProductID: market,
			Stop:      "loss",
		},
	}).SetSize(size).SetStopPrice(price)

	if kind == model.LIMIT {
		var prec int
		if prec, err = self.GetPricePrec(client, order.ProductID); err != nil {
			return nil, err
		}
		limit := price
		for {
			limit = limit * 0.99
			if precision.Round(limit, prec) < price {
				break
			}
		}
		order.SetPrice(precision.Round(limit, prec))
	}

	var saved *gdax.Order
	if saved, err = gdaxClient.CreateOrder(order); err != nil {
		return nil, errors.Wrap(err, 1)
	}

	var out []byte
	if out, err = json.Marshal(saved); err != nil {
		return nil, errors.Wrap(err, 1)
	}

	return out, nil
}

func (self *Gdax) OCO(client interface{}, market string, size float64, price, stop float64, metadata string) ([]byte, error) {
	return nil, errors.New("not implemented")
}

func (self *Gdax) GetClosed(client interface{}, market string) (model.Orders, error) {
	gdaxClient, ok := client.(*gdax.Client)
	if !ok {
		return nil, errors.New("invalid argument: client")
	}

	cursor := gdaxClient.ListFills(exchange.ListFillsParams{
		ProductID: market,
	})

	var (
		err   error
		out   model.Orders
		fills []exchange.Fill
	)
	for cursor.HasMore {
		if err = cursor.NextPage(&fills); err != nil {
			return nil, errors.Wrap(err, 1)
		}
		for _, fill := range fills {
			out = append(out, model.Order{
				Side:      model.NewOrderSide(fill.Side),
				Market:    fill.ProductID,
				Size:      gdax.ParseFloat(fill.Size),
				Price:     gdax.ParseFloat(fill.Price),
				CreatedAt: fill.CreatedAt.Time(),
			})
		}
	}

	return out, nil
}

func (self *Gdax) GetOpened(client interface{}, market string) (model.Orders, error) {
	gdaxClient, ok := client.(*gdax.Client)
	if !ok {
		return nil, errors.New("invalid argument: client")
	}

	cursor := gdaxClient.ListOrders(exchange.ListOrdersParams{
		Status: "open",
	})

	var (
		err    error
		out    model.Orders
		orders []gdax.Order
	)
	for cursor.HasMore {
		if err = cursor.NextPage(&orders); err != nil {
			return nil, errors.Wrap(err, 1)
		}
		for _, order := range orders {
			if order.ProductID == market {
				out = append(out, model.Order{
					Side:      model.NewOrderSide(order.Side),
					Market:    order.ProductID,
					Size:      order.GetSize(),
					Price:     order.GetPrice(),
					CreatedAt: order.CreatedAt.Time(),
				})
			}
		}
	}

	return out, nil
}

func (self *Gdax) GetBook(client interface{}, market string, side model.BookSide) (interface{}, error) {
	gdaxClient, ok := client.(*gdax.Client)
	if !ok {
		return nil, errors.New("invalid argument: client")
	}

	var (
		err  error
		book exchange.Book
	)
	if book, err = gdaxClient.GetBook(market, 3); err != nil {
		return nil, errors.Wrap(err, 1)
	}

	var out []exchange.BookEntry
	if side == model.BOOK_SIDE_ASKS {
		out = book.Asks
	} else {
		out = book.Bids
	}

	return out, nil
}

func (self *Gdax) Aggregate(client, book interface{}, market string, agg float64) (model.Book, error) {
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
		price := precision.Round(aggregation.Round(gdax.ParseFloat(e.Price), agg), prec)
		entry := out.EntryByPrice(price)
		if entry != nil {
			entry.Size = entry.Size + gdax.ParseFloat(e.Size)
		} else {
			entry = &model.Buy{
				Market: market,
				Price:  price,
				Size:   gdax.ParseFloat(e.Size),
			}
			out = append(out, *entry)
		}

	}

	return out, nil
}

func (self *Gdax) GetTicker(client interface{}, market string) (float64, error) {
	gdaxClient, ok := client.(*gdax.Client)
	if !ok {
		return 0, errors.New("invalid argument: client")
	}

	var (
		err    error
		ticker exchange.Ticker
	)
	if ticker, err = gdaxClient.GetTicker(market); err != nil {
		return 0, err
	}

	return gdax.ParseFloat(ticker.Price), nil
}

func (self *Gdax) Get24h(client interface{}, market string) (*model.Stats, error) {
	gdaxClient, ok := client.(*gdax.Client)
	if !ok {
		return nil, errors.New("invalid argument: client")
	}

	var (
		err       error
		gdaxStats exchange.Stats
	)
	if gdaxStats, err = gdaxClient.GetStats(market); err != nil {
		return nil, errors.Wrap(err, 1)
	}

	return &model.Stats{
		Market: market,
		High:   gdax.ParseFloat(gdaxStats.High),
		Low:    gdax.ParseFloat(gdaxStats.Low),
		BtcVolume: func(stats1 *exchange.Stats) float64 {
			products, err := self.getProducts(gdaxClient, true)
			if err == nil {
				for _, product := range products {
					if product.ID == market {
						if strings.EqualFold(product.BaseCurrency, model.BTC) {
							return gdax.ParseFloat(stats1.Volume)
						}
						stats2, err := gdaxClient.GetStats(self.FormatMarket(product.BaseCurrency, model.BTC))
						if err == nil {
							return gdax.ParseFloat(stats1.Volume) * gdax.ParseFloat(stats2.Last)
						}
					}
				}
			}
			return 0
		}(&gdaxStats),
	}, nil
}

func (self *Gdax) GetPricePrec(client interface{}, market string) (int, error) {
	products, err := self.getProducts(client, true)
	if err != nil {
		return 8, err
	}
	for _, p := range products {
		if p.ID == market {
			return precision.Parse(p.QuoteIncrement, 8), nil
		}
	}
	return 8, errors.Errorf("market %s not found", market)
}

func (self *Gdax) GetSizePrec(client interface{}, market string) (int, error) {
	products, err := self.getProducts(client, true)
	if err != nil {
		return 0, err
	}
	for _, p := range products {
		if p.ID == market {
			return precision.Parse(p.BaseIncrement, 0), nil
		}
	}
	return 0, errors.Errorf("market %s not found", market)
}

func (self *Gdax) GetMaxSize(client interface{}, base, quote string, hold, earn bool, def float64, mult multiplier.Mult) float64 {
	market := self.FormatMarket(base, quote)

	out := model.GetSizeMax(hold, earn, def, mult, func() int {
		prec, err := self.GetSizePrec(client, market)
		if err != nil {
			return 0
		}
		return prec
	})

	if hold {
		gdaxClient, ok := client.(*gdax.Client)
		if ok {
			min, err := self.getMinOrderSize(gdaxClient, market)
			if err == nil {
				if min > out {
					out = min
				}
			}
		}
	}

	return out
}

func (self *Gdax) Cancel(client interface{}, market string, side model.OrderSide) error {
	gdaxClient, ok := client.(*gdax.Client)
	if !ok {
		return errors.New("invalid argument: client")
	}

	cursor := gdaxClient.ListOrders(exchange.ListOrdersParams{
		Status: "open",
	})

	var (
		err    error
		orders []exchange.Order
	)
	for cursor.HasMore {
		if err = cursor.NextPage(&orders); err != nil {
			return errors.Wrap(err, 1)
		}
		for _, order := range orders {
			if order.ProductID == market {
				if ((side == model.BUY) && (order.Side == model.OrderSideString[model.BUY])) || ((side == model.SELL) && (order.Side == model.OrderSideString[model.SELL])) {
					if err = gdaxClient.CancelOrder(order.ID); err != nil {
						return errors.Wrap(err, 1)
					}
				}
			}
		}
	}

	return nil
}

func (self *Gdax) Buy(client interface{}, cancel bool, market string, calls model.Calls, deviation float64, kind model.OrderType) error {
	var err error

	gdaxClient, ok := client.(*gdax.Client)
	if !ok {
		return errors.New("invalid argument: client")
	}

	// step #1: delete the buy order(s) that are open in your book
	if cancel {
		cursor := gdaxClient.ListOrders(exchange.ListOrdersParams{
			Status: "open",
		})
		var orders []gdax.Order
		for cursor.HasMore {
			if err = cursor.NextPage(&orders); err != nil {
				return errors.Wrap(err, 1)
			}
			for _, order := range orders {
				if order.ProductID == market {
					if order.Side == model.OrderSideString[model.BUY] {
						// do not cancel orders that we're about to re-place
						index := calls.IndexByPrice(order.GetPrice())
						if index > -1 && order.GetSize() == calls[index].Size {
							calls[index].Skip = true
						} else {
							if err = gdaxClient.CancelOrder(order.ID); err != nil {
								return errors.Wrap(err, 1)
							}
						}
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
			order := (&gdax.Order{
				Order: &exchange.Order{
					Type:      model.OrderTypeString[model.LIMIT],
					Side:      model.OrderSideString[model.BUY],
					ProductID: market,
				},
			}).SetSize(call.Size).SetPrice(limit)
			if _, err = gdaxClient.CreateOrder(order); err != nil {
				var raw []byte
				if raw, _ = json.Marshal(order); raw == nil {
					return errors.Wrap(err, 1)
				} else {
					return errors.Wrap(errors.Append(err, "\t", string(raw)), 1)
				}
			}
		}
	}

	return nil
}

func (self *Gdax) IsLeveragedToken(name string) bool {
	return false
}

func (self *Gdax) HasAlgoOrder(client interface{}, market string) (bool, error) {
	return false, nil
}

func newGdax() model.Exchange {
	return &Gdax{
		ExchangeInfo: &model.ExchangeInfo{
			Code: "GDAX",
			Name: "Coinbase Pro",
			URL:  "https://pro.coinbase.com",
			REST: model.Endpoint{
				URI:     gdax.BASE_URL,
				Sandbox: gdax.BASE_URL_SANDBOX,
			},
			WebSocket: model.Endpoint{
				URI:     "wss://ws-feed.pro.coinbase.com",
				Sandbox: "wss://ws-feed-public.sandbox.pro.coinbase.com",
			},
			Country: "USA",
		},
	}
}

type gdaxSubscribePublic struct {
	Type       string   `json:"type"`
	ProductIDs []string `json:"product_ids"`
	Channels   []string `json:"channels"`
}

func (self *Gdax) newSubscribePublic(sandbox bool) (*gdaxSubscribePublic, error) {
	out := gdaxSubscribePublic{
		Type:       "subscribe",
		ProductIDs: []string{},
		Channels:   []string{"user", "heartbeat"},
	}
	products, err := self.getProducts(gdax.New(sandbox), false)
	if err != nil {
		return nil, err
	}
	for _, product := range products {
		out.ProductIDs = append(out.ProductIDs, product.ID)
	}
	return &out, nil
}

type gdaxSubscribePrivate struct {
	*gdaxSubscribePublic
	Signature  string `json:"signature"`
	Key        string `json:"key"`
	Passphrase string `json:"passphrase"`
	Timestamp  string `json:"timestamp"`
}

func (self *Gdax) newSubscribePrivate(secret, key, passphrase string, sandbox bool) (*gdaxSubscribePrivate, error) {
	timestamp := strconv.FormatInt(time.Now().Unix(), 10)

	message := fmt.Sprintf("%s%s%s%s", timestamp, "GET", "/users/self/verify", "")

	signature, err := gdaxGenerateSignature(message, secret)
	if err != nil {
		return nil, err
	}

	public, err := self.newSubscribePublic(sandbox)
	if err != nil {
		return nil, err
	}

	return &gdaxSubscribePrivate{
		gdaxSubscribePublic: public,
		Signature:           signature,
		Key:                 key,
		Passphrase:          passphrase,
		Timestamp:           timestamp,
	}, nil
}

func gdaxGenerateSignature(message, secret string) (string, error) {
	key, err := base64.StdEncoding.DecodeString(secret)
	if err != nil {
		return "", errors.Wrap(err, 1)
	}

	signature := hmac.New(sha256.New, key)
	_, err = signature.Write([]byte(message))
	if err != nil {
		return "", errors.Wrap(err, 1)
	}

	return base64.StdEncoding.EncodeToString(signature.Sum(nil)), nil
}
