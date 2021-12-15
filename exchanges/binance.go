//lint:file-ignore ST1006 receiver name should be a reflection of its identity; don't use generic names such as "this" or "self"
package exchanges

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"

	exchange "github.com/adshao/go-binance/v2"
	filemutex "github.com/alexflint/go-filemutex"
	"github.com/lintrieu/nefertiti/aggregation"
	"github.com/lintrieu/nefertiti/binance"
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
	binanceMutex *filemutex.FileMutex
)

const (
	binanceSessionFile = "binance.time"
	binanceSessionLock = "binance.lock"
)

//-------------------- globals -------------------

func init() {
	binance.BeforeRequest = func(client *binance.Client, weight int) error {
		var err error

		if binanceMutex == nil {
			if binanceMutex, err = filemutex.New(session.GetSessionFile(binanceSessionLock)); err != nil {
				return err
			}
		}

		if err = binanceMutex.Lock(); err != nil {
			return err
		}

		var lastRequest *time.Time
		if lastRequest, err = session.GetLastRequest(binanceSessionFile); err != nil {
			return err
		}

		if lastRequest != nil {
			var rps float64
			if rps, err = binance.GetRequestsPerSecond(client, weight); err != nil {
				return err
			}
			elapsed := time.Since(*lastRequest)
			if elapsed.Seconds() < (float64(1) / rps) {
				sleep := time.Duration((float64(time.Second) / rps)) - elapsed
				if flag.Debug() {
					log.Printf("[DEBUG] sleeping %f seconds", sleep.Seconds())
				}
				time.Sleep(sleep)
			}
		}

		return nil
	}
	binance.AfterRequest = func() {
		defer func() {
			binanceMutex.Unlock()
		}()
		session.SetLastRequest(binanceSessionFile, time.Now())
	}
}

func isBinanceError(err error) (*binance.BinanceError, bool) {
	wrapped, ok := err.(*errors.Error)
	if ok {
		return isBinanceError(wrapped.Err)
	}
	return binance.IsBinanceError(err)
}

func binanceOrderSide(order *binance.Order) model.OrderSide {
	if order.Side == exchange.SideTypeSell {
		return model.SELL
	}
	if order.Side == exchange.SideTypeBuy {
		return model.BUY
	}
	return model.ORDER_SIDE_NONE
}

//lint:ignore U1000 func is unused
func binanceOrderType(order *binance.Order) model.OrderType {
	if order.Type == exchange.OrderTypeLimit || order.Type == exchange.OrderTypeStopLossLimit {
		return model.LIMIT
	}
	if order.Type == exchange.OrderTypeMarket || order.Type == exchange.OrderTypeStopLoss {
		return model.MARKET
	}
	return model.ORDER_TYPE_NONE
}

func binanceOrderIndex(orders []binance.Order, orderID int64) int {
	for i, o := range orders {
		if o.OrderID == orderID {
			return i
		}
	}
	return -1
}

func binanceOrderToString(order *binance.Order) ([]byte, error) {
	var (
		err error
		out []byte
	)
	if out, err = json.Marshal(order); err != nil {
		return nil, errors.Wrap(err, 1)
	}
	return out, nil
}

//lint:ignore U1000 func is unused
func binanceOrderIsOCO(orders []binance.Order, order1 *binance.Order) bool {
	if order1.Type == exchange.OrderTypeStopLoss || order1.Type == exchange.OrderTypeStopLossLimit || order1.Type == exchange.OrderTypeLimitMaker {
		for _, order2 := range orders {
			if order2.OrderID != order1.OrderID {
				if order2.Side == order1.Side && order2.Symbol == order1.Symbol && order2.OrigQuantity == order1.OrigQuantity {
					if order2.Type == exchange.OrderTypeStopLoss || order2.Type == exchange.OrderTypeStopLossLimit {
						return order1.Type == exchange.OrderTypeLimitMaker
					}
					if order2.Type == exchange.OrderTypeLimitMaker {
						return order1.Type == exchange.OrderTypeStopLoss || order1.Type == exchange.OrderTypeStopLossLimit
					}
				}
			}
		}
	}
	return false
}

//-------------------- Binance -------------------

type Binance struct {
	*model.ExchangeInfo
}

//-------------------- private -------------------

func (self *Binance) baseURL(sandbox bool) string {
	if sandbox {
		return self.ExchangeInfo.REST.Sandbox
	}

	output := self.ExchangeInfo.REST.URI

	if output != binance.BASE_URL_US {
		arg := flag.Get("cluster")
		if arg.Exists {
			if cluster, err := arg.Int64(); err == nil {
				switch cluster {
				case 1:
					output = binance.BASE_URL_1
				case 2:
					output = binance.BASE_URL_2
				case 3:
					output = binance.BASE_URL_3
				}
			}
		} else {
			client := &http.Client{Timeout: time.Second}

			endpoints := map[string]time.Duration{
				binance.BASE_URL:   0,
				binance.BASE_URL_1: 0,
				binance.BASE_URL_2: 0,
				binance.BASE_URL_3: 0,
			}

			for endpoint := range endpoints {
				if req, err := http.NewRequest(http.MethodHead, endpoint, nil); err == nil {
					start := time.Now()
					if response, err := client.Do(req); err == nil {
						response.Body.Close()
						endpoints[endpoint] = time.Since(start)
					}
				}
			}

			lowest := endpoints[binance.BASE_URL]
			for endpoint, duration := range endpoints {
				if duration > 0 && duration < lowest {
					lowest = duration
					output = endpoint
				}
			}
		}
	}

	return output
}

// send a warning to StdOut
func (self *Binance) warn(err error) {
	pc, file, line, _ := runtime.Caller(1)
	log.Printf("[WARN] %s %v",
		errors.FormatCaller(pc, file, line), err,
	)
}

// send an error to StdOut
func (self *Binance) error(err error) {
	pc, file, line, _ := runtime.Caller(1)
	log.Printf("[ERROR] %s %v",
		errors.FormatCaller(pc, file, line), err,
	)
}

// send an error to StdOut *and* a notification to Pushover/Telegram
func (self *Binance) notify(err error, level int64, service model.Notify) {
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
			// --- BEGIN --- svanas 2020-09-12 --- do not push -1001 internal error ----
			//			binanceError, ok := isBinanceError(err)
			//			if ok {
			//				if binanceError.Code == -1001 {
			//					return
			//				}
			//			}
			// ---- END ---- svanas 2020-09-12 -----------------------------------------
			err := service.SendMessage(msg, "Binance - ERROR", model.ONCE_PER_MINUTE)
			if err != nil {
				self.error(err)
			}
		}
	}
}

func (self *Binance) newClientOrderID(metadata string) string {
	if metadata != "" {
		metadata = strings.Replace(metadata, ".", "_", -1)
		if out, err := binance.NewClientOrderMetadata(metadata); err == nil {
			return out
		}
	}
	return binance.NewClientOrderID()
}

// minimum notional value (aka price * quantity) allowed for an order
func (self *Binance) getMinTrade(client *binance.Client, market string, cached bool) (float64, error) {
	precs, err := binance.GetPrecs(client, cached)
	if err != nil {
		return 0, errors.Wrap(err, 1)
	}
	prec := precs.PrecFromSymbol(market)
	if prec != nil {
		return prec.Min, nil
	}
	return 0, nil
}

//-------------------- public --------------------

func (self *Binance) GetInfo() *model.ExchangeInfo {
	return self.ExchangeInfo
}

func (self *Binance) GetClient(permission model.Permission, sandbox bool) (interface{}, error) {
	if permission != model.PRIVATE {
		return binance.New(self.baseURL(sandbox), "", ""), nil
	}

	apiKey, apiSecret, err := promptForApiKeys("Binance")
	if err != nil {
		return nil, err
	}

	return binance.New(self.baseURL(sandbox), apiKey, apiSecret), nil
}

func (self *Binance) GetMarkets(cached, sandbox bool, blacklist []string) ([]model.Market, error) {
	var out []model.Market

	precs, err := binance.GetPrecs(binance.New(self.baseURL(sandbox), "", ""), cached)

	if err != nil {
		return nil, errors.Wrap(err, 1)
	}

	for _, prec := range precs {
		if prec.Symbol.Status == string(exchange.SymbolStatusTypeTrading) && func() bool {
			for _, ignore := range blacklist {
				if strings.EqualFold(prec.Symbol.Symbol, ignore) {
					return false
				}
			}
			return true
		}() {
			out = append(out, model.Market{
				Name:  prec.Symbol.Symbol,
				Base:  prec.Symbol.BaseAsset,
				Quote: prec.Symbol.QuoteAsset,
			})
		}
	}

	return out, nil
}

func (self *Binance) getMarketsEx(cached, sandbox bool, ignore, quotes []string) ([]model.Market, error) {
	markets, err := self.GetMarkets(cached, sandbox, ignore)

	if err != nil {
		return nil, err
	}

	if len(quotes) == 0 {
		return markets, err
	}

	var out []model.Market
	for _, market := range markets {
		for _, quote := range quotes {
			if strings.EqualFold(market.Quote, quote) {
				out = append(out, market)
			}
		}
	}

	return out, nil
}

func (self *Binance) FormatMarket(base, quote string) string {
	return strings.ToUpper(base + quote)
}

// listens to the open orders, send a notification on newly opened orders.
func (self *Binance) listen(client *binance.Client, service model.Notify, level int64, old []binance.Order) ([]binance.Order, error) {
	var err error

	// get my open orders
	var new []binance.Order
	if new, err = client.OpenOrders(); err != nil {
		return old, errors.Wrap(err, 1)
	}

	// look for new orders
	for _, order := range new {
		if binanceOrderIndex(old, order.OrderID) == -1 {
			var data []byte
			if data, err = binanceOrderToString(&order); err != nil {
				return new, err
			}

			log.Println("[OPEN] " + string(data))

			if service != nil {
				side := binanceOrderSide(&order)
				if side != model.ORDER_SIDE_NONE {
					if notify.CanSend(level, notify.OPENED) || (level == notify.LEVEL_DEFAULT && side == model.SELL) {
						if err = service.SendMessage(order, ("Binance - Open " + model.FormatOrderSide(side)), model.ALWAYS); err != nil {
							self.error(err)
						}
					}
				}
			}
		}
	}

	return new, nil
}

// listens to the filled orders, look for newly filled orders, automatically place new sell orders.
func (self *Binance) sell(
	client *binance.Client,
	strategy model.Strategy,
	quotes []string,
	mult, stop multiplier.Mult,
	hold, earn model.Markets,
	service model.Notify,
	twitter *notify.TwitterKeys,
	level int64,
	old []binance.Order,
	sandbox bool,
	debug bool,
) ([]binance.Order, error) {
	var err error

	// get my filled orders
	var (
		new     []binance.Order
		markets []model.Market
	)
	if markets, err = self.getMarketsEx(true, sandbox, nil, quotes); err != nil {
		return old, err
	}
	for _, market := range markets {
		var orders []binance.Order
		if orders, err = client.Orders(market.Name); err != nil {
			return old, errors.Wrap(err, 1)
		}
		for _, order := range orders {
			// get the orders that got filled during the last 24 hours
			if order.Status == exchange.OrderStatusTypeFilled && time.Since(order.UpdatedAt()).Hours() < 24 {
				new = append(new, order)
			}
		}
	}

	// look for newly filled orders
	for _, order := range new {
		if binanceOrderIndex(old, order.OrderID) == -1 {
			var data []byte
			if data, err = binanceOrderToString(&order); err != nil {
				return new, err
			}

			log.Println("[FILLED] " + string(data))

			side := binanceOrderSide(&order)
			if side != model.ORDER_SIDE_NONE {
				// send notification(s)
				if notify.CanSend(level, notify.FILLED) {
					if service != nil {
						title := fmt.Sprintf("Binance - Done %s", model.FormatOrderSide(side))
						if side == model.SELL {
							if strategy == model.STRATEGY_STOP_LOSS && (order.Type == exchange.OrderTypeStopLoss || order.Type == exchange.OrderTypeStopLossLimit) {
								title = fmt.Sprintf("%s %s", title, multiplier.Format(stop))
							} else {
								title = fmt.Sprintf("%s %s", title, multiplier.Format(mult))
							}
						}
						if err = service.SendMessage(order, title, model.ALWAYS); err != nil {
							self.error(err)
						}
					}
					if twitter != nil {
						notify.Tweet(twitter, fmt.Sprintf("Done %s. %s priced at %s #Binance", model.FormatOrderSide(side), model.TweetMarket(markets, order.Symbol), order.Price))
					}
				}

				// has a stop loss been filled? then place a buy order double the order size *** if --dca is included ***
				if side == model.SELL {
					if strategy == model.STRATEGY_STOP_LOSS {
						if order.Type == exchange.OrderTypeStopLoss || order.Type == exchange.OrderTypeStopLossLimit {
							if flag.Dca() {
								var prec int
								if prec, err = self.GetSizePrec(client, order.Symbol); err == nil {
									size := 2.2 * order.GetSize()
									_, _, err = self.Order(client,
										model.BUY,
										order.Symbol,
										precision.Round(size, prec),
										0, model.MARKET, "",
									)
								}
								if err != nil {
									return new, errors.Append(err, "\t", string(data))
								}
							}
						}
					}
				}

				// has a buy order been filled? then place a sell order
				if side == model.BUY {
					var (
						temp string
						call *model.Call
					)
					temp = session.GetTempFileName(order.ClientOrderID, ".binance")
					if call, err = model.File2Call(temp); err == nil {
						defer func() {
							os.Remove(temp)
						}()
					}
					// --- BEGIN --- svanas 2018-05-10 --- <APIError> code=-1013, msg=Invalid price.
					bought := order.GetPrice()
					if bought == 0 {
						if call != nil {
							bought = call.Price
						}
						if bought == 0 {
							if bought, err = self.GetTicker(client, order.Symbol); err != nil {
								return new, err
							}
						}
					}
					// ---- END ---- svanas 2018-05-10 ---------------------------------------------
					var (
						base  string
						quote string
					)
					if base, quote, err = model.ParseMarket(markets, order.Symbol); err == nil {
						qty := self.GetMaxSize(client, base, quote, hold.HasMarket(order.Symbol), earn.HasMarket(order.Symbol), order.GetSize(), mult)
						if qty > 0 {
							var prec int
							if prec, err = self.GetPricePrec(client, order.Symbol); err == nil {
								var ticker float64
								if ticker, err = self.GetTicker(client, order.Symbol); err == nil {
									target := func() float64 {
										if call != nil && call.HasTarget() {
											return precision.Round(call.ParseTarget(), prec)
										}
										return pricing.Multiply(bought, mult, prec)
									}()
									if ticker >= target {
										_, _, err = self.Order(client,
											model.SELL,
											order.Symbol,
											order.GetSize(),
											0, model.MARKET,
											strconv.FormatFloat(bought, 'f', -1, 64),
										)
									} else {
										limit := func() error {
											_, _, err := self.Order(client,
												model.SELL,
												order.Symbol,
												qty,
												target,
												model.LIMIT,
												strconv.FormatFloat(bought, 'f', -1, 64),
											)
											return err
										}
										if strategy == model.STRATEGY_STOP_LOSS {
											// place an OCO (aka One-Cancels-the-Other) if we can
											var symbol *exchange.Symbol
											if symbol, err = binance.GetSymbol(client, order.Symbol); err == nil {
												if symbol.OcoAllowed {
													if _, err = self.OCO(client,
														order.Symbol,
														qty,
														target,
														func() float64 {
															if call != nil && call.HasStop() {
																return precision.Round(call.ParseStop(), prec)
															}
															return pricing.Multiply(bought, stop, prec)
														}(),
														strconv.FormatFloat(bought, 'f', -1, 64),
													); err != nil {
														self.warn(err)
														err = limit()
													}
												} else {
													err = limit()
												}
											}
										} else {
											err = limit()
										}
									}
								}
							}
						}
					}
					if err != nil {
						return new, errors.Append(err, "\t", string(data))
					}
				}
			}
		}
	}

	return new, nil
}

func (self *Binance) Sell(
	strategy model.Strategy,
	hold, earn model.Markets,
	sandbox, tweet, debug bool,
	success model.OnSuccess,
) error {
	if strategy == model.STRATEGY_STANDARD || strategy == model.STRATEGY_STOP_LOSS {
		// we are OK
	} else {
		return errors.New("strategy not implemented")
	}

	var (
		err       error
		apiKey    string
		apiSecret string
	)
	if apiKey, apiSecret, err = promptForApiKeys("Binance"); err != nil {
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

	client := binance.New(self.baseURL(sandbox), apiKey, apiSecret)

	// get my open orders
	var open []binance.Order
	if open, err = client.OpenOrders(); err != nil {
		return errors.Wrap(err, 1)
	}

	// get my filled orders
	var (
		quotes  []string = []string{model.BTC}
		filled  []binance.Order
		markets []model.Market
	)
	flg := flag.Get("quote")
	if flg.Exists {
		quotes = flg.Split()
	} else {
		flag.Set("quote", strings.Join(quotes, ","))
	}
	if markets, err = self.getMarketsEx(true, sandbox, nil, quotes); err != nil {
		return err
	}
	for _, market := range markets {
		var orders []binance.Order
		if orders, err = client.Orders(market.Name); err != nil {
			return errors.Wrap(err, 1)
		}
		for _, order := range orders {
			// get the orders that got filled during the last 24 hours
			if order.Status == exchange.OrderStatusTypeFilled && time.Since(order.UpdatedAt()).Hours() < 24 {
				filled = append(filled, order)
			}
		}
	}

	if err = success(service); err != nil {
		return err
	}

	for {
		// read the dynamic settings
		var (
			level  int64 = notify.LEVEL_DEFAULT
			mult   multiplier.Mult
			stop   multiplier.Mult
			quotes []string = flag.Get("quote").Split()
		)
		if level, err = notify.Level(); err != nil {
			self.notify(err, level, service)
		} else if mult, err = multiplier.Get(multiplier.FIVE_PERCENT); err != nil {
			self.notify(err, level, service)
		} else if stop, err = multiplier.Stop(); err != nil {
			self.notify(err, level, service)
		} else
		// listen to the filled orders, look for newly filled orders, automatically place new sell orders.
		if filled, err = self.sell(client, strategy, quotes, mult, stop, hold, earn, service, twitter, level, filled, sandbox, debug); err != nil {
			self.notify(err, level, service)
		} else
		// listen to the open orders, send a notification on newly opened orders.
		if open, err = self.listen(client, service, level, open); err != nil {
			self.notify(err, level, service)
		}
	}
}

func (self *Binance) Order(
	client interface{},
	side model.OrderSide,
	market string,
	size float64,
	price float64,
	kind model.OrderType,
	metadata string,
) (oid []byte, raw []byte, err error) {
	binanceClient, ok := client.(*binance.Client)
	if !ok {
		return nil, nil, errors.New("invalid argument: client")
	}

	service := binanceClient.NewCreateOrderService().
		Symbol(market).
		Quantity(size).
		NewClientOrderID(self.newClientOrderID(metadata))

	if kind == model.MARKET {
		service.Type(exchange.OrderTypeMarket)
	} else {
		service.Type(exchange.OrderTypeLimit).TimeInForce(exchange.TimeInForceTypeGTC).Price(price)
	}

	if side == model.BUY {
		service.Side(exchange.SideTypeBuy)
	} else if side == model.SELL {
		service.Side(exchange.SideTypeSell)
	}

	var order *exchange.CreateOrderResponse
	if order, err = service.Do(context.Background()); err != nil {
		return nil, nil, errors.Wrap(err, 1)
	}

	var out []byte
	if out, err = json.Marshal(order); err != nil {
		return nil, nil, errors.Wrap(err, 1)
	}

	return []byte(order.ClientOrderID), out, nil
}

func (self *Binance) StopLoss(client interface{}, market string, size float64, price float64, kind model.OrderType, metadata string) ([]byte, error) {
	var err error

	binanceClient, ok := client.(*binance.Client)
	if !ok {
		return nil, errors.New("invalid argument: client")
	}

	service := binanceClient.NewCreateOrderService().
		Symbol(market).
		Side(exchange.SideTypeSell).
		Quantity(size).
		StopPrice(price).
		NewClientOrderID(self.newClientOrderID(metadata))

	if kind == model.MARKET {
		service.Type(exchange.OrderTypeStopLoss)
	} else {
		var prec int
		if prec, err = self.GetPricePrec(client, market); err != nil {
			return nil, err
		}
		limit := price
		for {
			limit = limit * 0.99
			if precision.Round(limit, prec) < price {
				break
			}
		}
		service.Type(exchange.OrderTypeStopLossLimit).TimeInForce(exchange.TimeInForceTypeGTC).Price(precision.Round(limit, prec))
	}

	var order *exchange.CreateOrderResponse
	if order, err = service.Do(context.Background()); err != nil {
		// --- BEGIN --- svanas 2019-02-07 ------------------------------------
		_, ok := isBinanceError(err)
		if ok {
			self.warn(err)
			// -1013 stop loss orders are not supported for this symbol
			if kind != model.LIMIT {
				return self.StopLoss(client, market, size, price, model.LIMIT, metadata)
			}
			// -2010 order would trigger immediately
			if strings.Contains(err.Error(), "would trigger immediately") {
				var prec int
				if prec, err = self.GetPricePrec(client, market); err == nil {
					lower := price
					for {
						lower = lower * 0.99
						if precision.Round(lower, prec) < price {
							break
						}
					}
					return self.StopLoss(client, market, size, precision.Round(lower, prec), kind, metadata)
				}
			}
		}
		// ---- END ---- svanas 2019-02-07 ------------------------------------
		return nil, errors.Wrap(err, 1)
	}

	var out []byte
	if out, err = json.Marshal(order); err != nil {
		return nil, errors.Wrap(err, 1)
	}

	return out, nil
}

func (self *Binance) OCO(client interface{}, market string, size float64, price, stop float64, metadata string) ([]byte, error) {
	binanceClient, ok := client.(*binance.Client)
	if !ok {
		return nil, errors.New("invalid argument: client")
	}

	clientOrderId1 := self.newClientOrderID(metadata)
	clientOrderId2 := self.newClientOrderID(metadata)
	if clientOrderId1 == clientOrderId2 {
		clientOrderId1 = binance.NewClientOrderID()
		clientOrderId2 = binance.NewClientOrderID()
	}

	svc := binanceClient.NewCreateOCOService().
		Symbol(market).
		Quantity(size).
		Price(price).
		StopPrice(stop).
		Side(exchange.SideTypeSell).
		StopClientOrderID(clientOrderId1).
		LimitClientOrderID(clientOrderId2)

	var (
		err  error
		resp *exchange.CreateOCOResponse
	)
	if resp, err = svc.Do(context.Background()); err != nil {
		_, ok := isBinanceError(err)
		if ok {
			// -1013 Stop loss orders are not supported for this symbol
			if strings.Contains(err.Error(), "loss orders are not supported") {
				var prec int
				if prec, err = self.GetPricePrec(client, market); err != nil {
					return nil, err
				}
				lower := stop
				for {
					lower = lower * 0.99
					if precision.Round(lower, prec) < stop {
						break
					}
				}
				svc.StopLimitPrice(precision.Round(lower, prec)).StopLimitTimeInForce(exchange.TimeInForceTypeGTC)
				resp, err = svc.Do(context.Background())
			}
		}
		if err != nil {
			return nil, errors.Wrap(err, 1)
		}
	}

	var out []byte
	if out, err = json.Marshal(resp); err != nil {
		return nil, errors.Wrap(err, 1)
	}

	return out, nil
}

func (self *Binance) GetClosed(client interface{}, market string) (model.Orders, error) {
	var err error

	binanceClient, ok := client.(*binance.Client)
	if !ok {
		return nil, errors.New("invalid argument: client")
	}

	var orders []binance.Order
	if orders, err = binanceClient.Orders(market); err != nil {
		return nil, errors.Wrap(err, 1)
	}

	var out model.Orders
	for _, order := range orders {
		// get the orders that got filled during the last 24 hours
		if order.Status == exchange.OrderStatusTypeFilled && time.Since(order.UpdatedAt()).Hours() < 24 {
			out = append(out, model.Order{
				Side:      binanceOrderSide(&order),
				Market:    order.Symbol,
				Size:      order.GetSize(),
				Price:     order.GetPrice(),
				CreatedAt: time.Unix(order.Time/1000, 0),
			})
		}
	}

	return out, nil
}

func (self *Binance) GetOpened(client interface{}, market string) (model.Orders, error) {
	var err error

	binanceClient, ok := client.(*binance.Client)
	if !ok {
		return nil, errors.New("invalid argument: client")
	}

	var orders []binance.Order
	if orders, err = binanceClient.OpenOrdersEx(market); err != nil {
		return nil, errors.Wrap(err, 1)
	}

	var out model.Orders
	for _, order := range orders {
		out = append(out, model.Order{
			Side:      binanceOrderSide(&order),
			Market:    order.Symbol,
			Size:      order.GetSize(),
			Price:     order.GetPrice(),
			CreatedAt: time.Unix(order.Time/1000, 0),
		})
	}

	return out, nil
}

func (self *Binance) GetBook(client interface{}, market string, side model.BookSide) (interface{}, error) {
	var err error

	binanceClient, ok := client.(*binance.Client)
	if !ok {
		return nil, errors.New("invalid argument: client")
	}

	var book *exchange.DepthResponse
	if book, err = binanceClient.Depth(market, 1000); err != nil {
		return nil, errors.Wrap(err, 1)
	}

	var out []binance.BookEntry
	if side == model.BOOK_SIDE_ASKS {
		out = book.Asks
	} else {
		out = book.Bids
	}

	return out, nil
}

func (self *Binance) Aggregate(client, book interface{}, market string, agg float64) (model.Book, error) {
	bids, ok := book.([]binance.BookEntry)
	if !ok {
		return nil, errors.New("invalid argument: book")
	}

	prec, err := self.GetPricePrec(client, market)
	if err != nil {
		return nil, err
	}

	var out model.Book
	for _, e := range bids {
		var (
			price float64
			qty   float64
		)
		if price, qty, err = e.Parse(); err != nil {
			return nil, err
		}
		price = precision.Round(aggregation.Round(price, agg), prec)
		entry := out.EntryByPrice(price)
		if entry != nil {
			entry.Size = entry.Size + qty
		} else {
			entry = &model.Buy{
				Market: market,
				Price:  price,
				Size:   qty,
			}
			out = append(out, *entry)
		}
	}

	return out, nil
}

func (self *Binance) GetTicker(client interface{}, market string) (float64, error) {
	var err error

	binanceClient, ok := client.(*binance.Client)
	if !ok {
		return 0, errors.New("invalid argument: client")
	}

	var ticker *exchange.PriceChangeStats
	if ticker, err = binanceClient.Ticker(market); err != nil {
		return 0, errors.Wrap(err, 1)
	}

	var out float64
	if out, err = strconv.ParseFloat(ticker.LastPrice, 64); err != nil {
		return 0, errors.Wrap(err, 1)
	}

	return out, nil
}

func (self *Binance) Get24h(client interface{}, market string) (*model.Stats, error) {
	binanceClient, ok := client.(*binance.Client)
	if !ok {
		return nil, errors.New("invalid argument: client")
	}

	stats, err := binanceClient.Ticker(market)
	if err != nil {
		return nil, errors.Wrap(err, 1)
	}

	high, err := strconv.ParseFloat(stats.HighPrice, 64)
	if err != nil {
		return nil, errors.Wrap(err, 1)
	}

	low, err := strconv.ParseFloat(stats.LowPrice, 64)
	if err != nil {
		return nil, errors.Wrap(err, 1)
	}

	return &model.Stats{
		Market: market,
		High:   high,
		Low:    low,
		BtcVolume: func() float64 {
			symbol, err := binance.GetSymbol(binanceClient, market)
			if err == nil {
				volume, err := strconv.ParseFloat(stats.QuoteVolume, 64)
				if err == nil {
					if strings.EqualFold(symbol.QuoteAsset, model.BTC) {
						return volume
					} else {
						btcSymbol := self.FormatMarket(model.BTC, symbol.QuoteAsset)
						ticker2, err := binanceClient.Ticker(btcSymbol)
						if err == nil {
							price, err := strconv.ParseFloat(ticker2.LastPrice, 64)
							if err == nil {
								return volume / price
							}
						}
					}
				}
			}
			return 0
		}(),
	}, nil
}

func (self *Binance) GetPricePrec(client interface{}, market string) (int, error) {
	binanceClient, ok := client.(*binance.Client)
	if !ok {
		return 0, errors.New("invalid argument: client")
	}
	precs, err := binance.GetPrecs(binanceClient, true)
	if err != nil {
		return 0, errors.Wrap(err, 1)
	}
	prec := precs.PrecFromSymbol(market)
	if prec != nil {
		return prec.Price, nil
	}
	return 8, nil
}

func (self *Binance) GetSizePrec(client interface{}, market string) (int, error) {
	binanceClient, ok := client.(*binance.Client)
	if !ok {
		return 0, errors.New("invalid argument: client")
	}
	precs, err := binance.GetPrecs(binanceClient, true)
	if err != nil {
		return 0, errors.Wrap(err, 1)
	}
	prec := precs.PrecFromSymbol(market)
	if prec != nil {
		return prec.Size, nil
	}
	return 0, nil
}

func (self *Binance) GetMaxSize(client interface{}, base, quote string, hold, earn bool, def float64, mult multiplier.Mult) float64 {
	if hold {
		if base == "BNB" {
			return 0
		}
	}
	return model.GetSizeMax(hold, earn, def, mult, func() int {
		prec, err := self.GetSizePrec(client, self.FormatMarket(base, quote))
		if err != nil {
			return 0
		}
		return prec
	})
}

func (self *Binance) Cancel(client interface{}, market string, side model.OrderSide) error {
	var err error

	binanceClient, ok := client.(*binance.Client)
	if !ok {
		return errors.New("invalid argument: client")
	}

	var orders []binance.Order
	if orders, err = binanceClient.OpenOrdersEx(market); err != nil {
		return errors.Wrap(err, 1)
	}

	for _, order := range orders {
		if binanceOrderSide(&order) == side {
			if err = binanceClient.CancelOrder(market, order.OrderID); err != nil {
				return errors.Wrap(err, 1)
			}
			tmp := session.GetTempFileName(order.ClientOrderID, ".binance")
			if _, err = os.Stat(tmp); err == nil {
				os.Remove(tmp)
			}
		}
	}

	return nil
}

func (self *Binance) Buy(client interface{}, cancel bool, market string, calls model.Calls, deviation float64, kind model.OrderType) error {
	var err error

	binanceClient, ok := client.(*binance.Client)
	if !ok {
		return errors.New("invalid argument: client")
	}

	// step #1: delete the buy order(s) that are open in your book
	if cancel {
		var orders []binance.Order
		if orders, err = binanceClient.OpenOrdersEx(market); err != nil {
			return errors.Wrap(err, 1)
		}
		for _, order := range orders {
			side := binanceOrderSide(&order)
			if side == model.BUY {
				// do not cancel orders that we're about to re-place
				index := calls.IndexByPrice(order.GetPrice())
				if index > -1 {
					calls[index].Skip = true
				} else {
					if err = binanceClient.CancelOrder(market, order.OrderID); err != nil {
						return errors.Wrap(err, 1)
					}
				}
			}
		}
	}

	// step 2: open the top X buy orders
	for _, call := range calls {
		if !call.Skip {
			var (
				oid   []byte
				min   float64
				qty   float64 = call.Size
				limit float64 = call.Price
			)
			if deviation != 1.0 {
				kind, limit = call.Deviate(self, client, kind, deviation)
			}
			// --- BEGIN --- svanas 2018-11-30 --- <APIError> code=-1013, msg=Filter failure: MIN_NOTIONAL.
			if min, err = self.getMinTrade(binanceClient, market, true); err != nil {
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
			// ---- END ---- svanas 2018-11-30 ------------------------------------------------------------
			oid, _, err = self.Order(client,
				model.BUY,
				market,
				qty,
				limit,
				kind, "",
			)
			if err != nil {
				return err
			}
			if oid != nil {
				if kind == model.MARKET {
					var ticker float64
					if ticker, err = self.GetTicker(client, market); err == nil {
						err = model.Call2File(&model.Call{
							Buy: &model.Buy{
								Market: call.Market,
								Price:  ticker,
							},
							Stop:   call.Stop,
							Target: call.Target,
						}, session.GetTempFileName(string(oid), ".binance"))
					}
				} else {
					err = model.Call2File(&call, session.GetTempFileName(string(oid), ".binance"))
				}
				if err != nil {
					return err
				}
			}
		}
	}

	return nil
}

func (self *Binance) IsLeveragedToken(name string) bool {
	return (len(name) > 2 && strings.HasSuffix(strings.ToUpper(name), "UP")) ||
		(len(name) > 4 && strings.HasSuffix(strings.ToUpper(name), "DOWN")) ||
		(len(name) > 4 && strings.HasSuffix(strings.ToUpper(name), "BEAR")) ||
		(len(name) > 4 && strings.HasSuffix(strings.ToUpper(name), "BULL"))
}

func (self *Binance) HasAlgoOrder(client interface{}, market string) (bool, error) {
	return false, nil
}

func newBinance() model.Exchange {
	return &Binance{
		ExchangeInfo: &model.ExchangeInfo{
			Code: "BINA",
			Name: "Binance",
			URL:  "https://www.binance.com/",
			REST: model.Endpoint{
				URI:     binance.BASE_URL,
				Sandbox: "https://testnet.binance.vision",
			},
			WebSocket: model.Endpoint{
				URI:     "wss://stream.binance.com:9443",
				Sandbox: "wss://testnet.binance.vision",
			},
			Country: "China",
		},
	}
}

func newBinanceUS() model.Exchange {
	return &Binance{
		ExchangeInfo: &model.ExchangeInfo{
			Code: "BIUS",
			Name: "BinanceUS",
			URL:  "https://www.binance.us/",
			REST: model.Endpoint{
				URI:     binance.BASE_URL_US,
				Sandbox: "https://testnet.binance.vision",
			},
			WebSocket: model.Endpoint{
				URI:     "wss://stream.binance.us:9443",
				Sandbox: "wss://testnet.binance.vision",
			},
			Country: "United States",
		},
	}
}
