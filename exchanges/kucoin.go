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
	"github.com/lintrieu/nefertiti/errors"
	"github.com/lintrieu/nefertiti/flag"
	exchange "github.com/lintrieu/nefertiti/kucoin"
	"github.com/lintrieu/nefertiti/model"
	"github.com/lintrieu/nefertiti/multiplier"
	"github.com/lintrieu/nefertiti/notify"
	"github.com/lintrieu/nefertiti/precision"
	"github.com/lintrieu/nefertiti/pricing"
	"github.com/lintrieu/nefertiti/session"
	"github.com/lintrieu/nefertiti/uuid"
)

var (
	kucoinMutex *filemutex.FileMutex
)

const (
	kucoinSessionFile = "kucoin.time"
	kucoinSessionLock = "kucoin.lock"
)

func init() {
	exchange.BeforeRequest = func(client *exchange.ApiService, request *exchange.Request, rps float64) error {
		var err error

		if kucoinMutex == nil {
			if kucoinMutex, err = filemutex.New(session.GetSessionFile(kucoinSessionLock)); err != nil {
				return err
			}
		}

		if err = kucoinMutex.Lock(); err != nil {
			return err
		}

		var lastRequest *time.Time
		if lastRequest, err = session.GetLastRequest(kucoinSessionFile); err != nil {
			return err
		}

		if lastRequest != nil {
			elapsed := time.Since(*lastRequest)
			if elapsed.Seconds() < (float64(1) / rps) {
				sleep := time.Duration((float64(time.Second) / rps)) - elapsed
				if flag.Debug() {
					log.Printf("[DEBUG] sleeping %f seconds", sleep.Seconds())
				}
				time.Sleep(sleep)
			}
		}

		if flag.Debug() {
			log.Printf("[DEBUG] %s %s", request.Method, request.Path)
		}

		return nil
	}
	exchange.AfterRequest = func() {
		defer func() {
			kucoinMutex.Unlock()
		}()
		session.SetLastRequest(kucoinSessionFile, time.Now())
	}
}

const (
	kucoinPartnerID     = "nefertiti"
	kucoinPartnerSecret = "1fac7c63-33d6-4662-801f-4a0900ecbd88"
)

type Kucoin struct {
	*model.ExchangeInfo
	symbols exchange.SymbolsModel
}

//-------------------- private -------------------

func (self *Kucoin) baseURI(sandbox bool) string {
	if sandbox {
		return self.ExchangeInfo.REST.Sandbox
	}
	return self.ExchangeInfo.REST.URI
}

func (self *Kucoin) error(err error, level int64, service model.Notify) {
	pc, file, line, _ := runtime.Caller(1)
	prefix := errors.FormatCaller(pc, file, line)

	msg := fmt.Sprintf("%s %v", prefix, err)

	if strings.Contains(err.Error(), "no such host") || strings.Contains(err.Error(), "network is unreachable") {
		log.Printf("[ERROR] %s", msg)
		return
	}

	_, ok := err.(*errors.Error)
	if ok && flag.Debug() {
		log.Printf("[ERROR] %s", err.(*errors.Error).ErrorStack(prefix, ""))
	} else {
		log.Printf("[ERROR] %s", msg)
	}

	if service != nil {
		if notify.CanSend(level, notify.ERROR) {
			err := service.SendMessage(msg, "Kucoin - ERROR", model.ONCE_PER_MINUTE)
			if err != nil {
				log.Printf("[ERROR] %v", err)
			}
		}
	}
}

//lint:ignore U1000 func is unused
func (self *Kucoin) getAvailableBalance(client *exchange.ApiService, curr string) (float64, error) {
	var (
		err      error
		out      float64
		resp     *exchange.ApiResponse
		accounts exchange.AccountsModel
	)
	if resp, err = client.Accounts(curr, "trade"); err != nil {
		return 0, errors.Wrap(err, 1)
	}
	if err = resp.ReadData(&accounts); err != nil {
		return 0, errors.Wrap(err, 1)
	}
	if len(accounts) == 0 {
		return 0, errors.Errorf("currency %s does not exist", curr)
	}
	if out, err = strconv.ParseFloat(accounts[0].Available, 64); err != nil {
		return 0, errors.Wrap(err, 1)
	}
	return out, nil
}

func (self *Kucoin) getSymbol(client *exchange.ApiService, name string) (*exchange.SymbolModel, error) {
	cached := true
	for {
		symbols, err := self.getSymbols(client, cached)
		if err != nil {
			return nil, err
		}
		for _, symbol := range symbols {
			if symbol.Symbol == name {
				return symbol, nil
			}
		}
		if !cached {
			return nil, errors.Errorf("symbol %s does not exist", name)
		}
		cached = false
	}
}

func (self *Kucoin) getSymbols(client *exchange.ApiService, cached bool) (exchange.SymbolsModel, error) {
	if self.symbols == nil || !cached {
		var (
			err     error
			resp    *exchange.ApiResponse
			symbols exchange.SymbolsModel
		)
		if resp, err = client.Symbols(""); err != nil {
			return self.symbols, errors.Wrap(err, 1)
		}
		if err = resp.ReadData(&symbols); err != nil {
			return self.symbols, errors.Wrap(err, 1)
		}
		self.symbols = symbols
	}
	return self.symbols, nil
}

// the minimum order quantity requried to place an order.
func (self *Kucoin) getMinSize(client *exchange.ApiService, market string) (float64, error) {
	symbol, err := self.getSymbol(client, market)
	if err != nil {
		return 0, err
	}
	out, err := strconv.ParseFloat(symbol.BaseMinSize, 64)
	if err != nil {
		return 0, errors.Wrap(err, 1)
	}
	return out, nil
}

// getOrders returns a list your current orders.
func (self *Kucoin) getOrders(client *exchange.ApiService, params map[string]string) (exchange.OrdersModel, error) {
	var (
		err    error
		curr   int64
		output exchange.OrdersModel
	)

	curr = 1
	for {
		var resp *exchange.ApiResponse
		if resp, err = client.Orders(params, &exchange.PaginationParam{CurrentPage: curr, PageSize: 50}); err != nil {
			return nil, errors.Wrap(err, 1)
		}
		var (
			page   *exchange.PaginationModel
			orders exchange.OrdersModel
		)
		if page, err = resp.ReadPaginationData(&orders); err != nil {
			return nil, errors.Wrap(err, 1)
		}
		output = append(output, orders...)
		if page.CurrentPage >= page.TotalPage {
			break
		} else {
			curr++
		}
	}

	curr = 1
	for {
		var resp *exchange.ApiResponse
		if resp, err = client.StopOrders(params, &exchange.PaginationParam{CurrentPage: curr, PageSize: 50}); err != nil {
			return nil, errors.Wrap(err, 1)
		}
		var (
			page   *exchange.PaginationModel
			orders exchange.OrdersModel
		)
		if page, err = resp.ReadPaginationData(&orders); err != nil {
			return nil, errors.Wrap(err, 1)
		}
		output = append(output, orders...)
		if page.CurrentPage >= page.TotalPage {
			break
		} else {
			curr++
		}
	}

	return output, nil
}

// getRecentFills returns a list of your recent fills, up to max orders.
func (self *Kucoin) getRecentFills(client *exchange.ApiService, max int) (exchange.FillsModel, error) {
	const pageSize = 100

	var (
		err    error
		curr   int64 = 1
		output exchange.FillsModel
	)

	for {
		var resp *exchange.ApiResponse
		if resp, err = client.Fills(map[string]string{}, &exchange.PaginationParam{CurrentPage: curr, PageSize: pageSize}); err != nil {
			return nil, errors.Wrap(err, 1)
		}
		var (
			page   *exchange.PaginationModel
			orders exchange.FillsModel
		)
		if page, err = resp.ReadPaginationData(&orders); err != nil {
			return nil, errors.Wrap(err, 1)
		}
		if len(orders) == 0 {
			if page.CurrentPage == 1 && page.TotalPage > 1 || page.CurrentPage > 1 {
				errors.Errorf("/api/v1/fills?currentPage=%d&pageSize=%d returned 0 orders, expected at least 1.", curr, pageSize)
			}
		}
		output = append(output, orders...)
		if len(output) >= max || page.CurrentPage >= page.TotalPage {
			break
		} else {
			curr++
		}
	}

	return output, nil
}

//-------------------- public --------------------

func (self *Kucoin) GetInfo() *model.ExchangeInfo {
	return self.ExchangeInfo
}

func (self *Kucoin) GetClient(permission model.Permission, sandbox bool) (interface{}, error) {
	// starting 04/26/21, the KuCoin order book endpoints require authentication.
	if permission == model.PUBLIC {
		return exchange.NewApiService(
			exchange.ApiBaseURIOption(self.baseURI(sandbox)),
		), nil
	}

	apiKey, apiSecret, apiPassphrase, err := promptForApiKeysEx("KuCoin")
	if err != nil {
		return nil, err
	}

	return exchange.NewApiService(
		exchange.ApiBaseURIOption(self.baseURI(sandbox)),
		exchange.ApiKeyOption(apiKey),
		exchange.ApiSecretOption(apiSecret),
		exchange.ApiPassPhraseOption(apiPassphrase),
		exchange.ApiPartnerIdOption(kucoinPartnerID),
		exchange.ApiPartnerSecretOption(kucoinPartnerSecret),
	), nil
}

func (self *Kucoin) GetMarkets(cached, sandbox bool, blacklist []string) ([]model.Market, error) {
	var out []model.Market

	client := exchange.NewApiService(
		exchange.ApiBaseURIOption(self.baseURI(sandbox)),
	)

	symbols, err := self.getSymbols(client, cached)
	if err != nil {
		return nil, err
	}

	for _, symbol := range symbols {
		if symbol.EnableTrading && func() bool {
			for _, ignore := range blacklist {
				if strings.EqualFold(symbol.Symbol, ignore) {
					return false
				}
			}
			return true
		}() {
			out = append(out, model.Market{
				Name:  symbol.Symbol,
				Base:  symbol.BaseCurrency,
				Quote: symbol.QuoteCurrency,
			})
		}
	}

	return out, nil
}

func (self *Kucoin) FormatMarket(base, quote string) string {
	return strings.ToUpper(fmt.Sprintf("%s-%s", base, quote))
}

// listens to the filled orders, look for newly filled orders, automatically place new sell orders.
func (self *Kucoin) sell(
	client *exchange.ApiService,
	strategy model.Strategy,
	mult, stop multiplier.Mult,
	hold, earn model.Markets,
	service model.Notify,
	twitter *notify.TwitterKeys,
	level int64,
	old exchange.FillsModel,
	sandbox bool,
	debug bool,
) (exchange.FillsModel, error) {
	var err error

	// get my filled orders
	var new exchange.FillsModel
	if new, err = self.getRecentFills(client, 500); err != nil {
		return old, err
	}

	if len(old) > 0 {
		if len(new) > 0 {
			for _, order := range old {
				if new.IndexOfOrderId(order.OrderId) > -1 {
					goto WeAreGood
				}
			}
		}
		goto WhatTheFuck
	WhatTheFuck:
		return old, errors.Errorf("/api/v1/fills returned %d orders, expected at least %d.", len(new), len(old))
	WeAreGood:
		// nothing to see here, carry on
	}

	// get the markets
	var markets []model.Market
	if markets, err = self.GetMarkets(false, sandbox, nil); err != nil {
		return old, err
	}

	type (
		SimpleOrder struct {
			Low  float64
			High float64
			Size float64
		}
		SimpleOrders map[string]*SimpleOrder
	)
	avg := func(order *SimpleOrder) float64 {
		if order.Low == 0 && order.High > 0 {
			return order.High
		}
		if order.High == 0 && order.Low > 0 {
			return order.Low
		}
		if order.High == 0 && order.Low == 0 {
			return 0
		}
		return (order.Low + order.High) / 2
	}
	var (
		bought  = make(SimpleOrders)
		stopped = make(SimpleOrders)
	)

	// look for newly filled orders
	for _, order := range new {
		if old.IndexOfOrderId(order.OrderId) == -1 {
			side := model.NewOrderSide(order.Side)

			var data []byte
			if data, err = json.Marshal(order); err != nil {
				return new, errors.Wrap(err, 1)
			}
			log.Println("[FILLED] " + string(data))

			var orders SimpleOrders = nil
			if side == model.BUY {
				orders = bought
			} else if side == model.SELL && order.Stop == "loss" {
				orders = stopped
			}

			if orders != nil {
				o, exist := orders[order.Symbol]
				if !exist {
					orders[order.Symbol] = &SimpleOrder{
						Low:  order.ParsePrice(),
						High: order.ParsePrice(),
						Size: order.ParseSize(),
					}
				} else {
					// add up amount(s), hereby preventing a problem with partial matches
					price := order.ParsePrice()
					if price > 0 {
						if price < o.Low {
							orders[order.Symbol].Low = price
						} else if price > o.High {
							orders[order.Symbol].High = price
						}
					}
					orders[order.Symbol].Size = o.Size + order.ParseSize()
				}
			}

			// send notification(s)
			if side != model.ORDER_SIDE_NONE {
				if notify.CanSend(level, notify.FILLED) {
					if service != nil {
						title := fmt.Sprintf("Kucoin - Done %s", model.FormatOrderSide(side))
						if side == model.SELL {
							if strategy == model.STRATEGY_STOP_LOSS && order.Stop == "loss" {
								title = fmt.Sprintf("%s %s", title, multiplier.Format(stop))
							} else {
								title = fmt.Sprintf("%s %s", title, multiplier.Format(mult))
							}
						}
						if err = service.SendMessage(order, title, model.ALWAYS); err != nil {
							log.Printf("[ERROR] %v", err)
						}
					}
					if twitter != nil {
						notify.Tweet(twitter, fmt.Sprintf("Done %s. %s priced at %s #Kucoin", model.FormatOrderSide(side), model.TweetMarket(markets, order.Symbol), order.Price))
					}
				}
			}
		}
	}

	// has a stop loss been filled? then place a buy order double the order size *** if --dca is included ***
	if strategy == model.STRATEGY_STOP_LOSS {
		if flag.Dca() {
			for symbol, stop := range stopped {
				var opened exchange.OrdersModel
				if opened, err = self.getOrders(client, map[string]string{"status": "active", "symbol": symbol}); err != nil {
					self.error(err, level, service)
				} else {
					var cb exchange.OrderPredicate = func(order *exchange.OrderModel) bool {
						return order.Stop == "loss" && order.Side == "sell"
					}
					if opened.Find(&cb) > -1 {
						log.Printf("[INFO] Not re-buying %s because you have at least one active (non-filled) stop-loss order.\n", symbol)
					} else {
						prec := 0
						size := 2.2 * stop.Size
						if prec, err = self.GetSizePrec(client, symbol); err == nil {
							_, _, err = self.Order(client,
								model.BUY, symbol,
								precision.Round(size, prec),
								0, model.MARKET, "",
							)
						}
						if err != nil {
							self.error(err, level, service)
						}
					}
				}
			}
		}
	}

	// has a buy order been filled? then place a sell order
	for symbol, buy := range bought {
		amount := buy.Size
		bought := avg(buy)

		var sp int
		if sp, err = self.GetSizePrec(client, symbol); err != nil {
			return new, err
		}
		amount = precision.Floor(amount, sp)

		if bought == 0 {
			if bought, err = self.GetTicker(client, symbol); err != nil {
				return new, err
			}
		}

		// get base currency and desired size, calculate price, place sell order
		var (
			base  string
			quote string
		)
		base, quote, err = model.ParseMarket(markets, symbol)
		if err == nil {
			amount = self.GetMaxSize(client, base, quote, hold.HasMarket(symbol), earn.HasMarket(symbol), amount, mult)
			if amount > 0 {
				var pp int
				if pp, err = self.GetPricePrec(client, symbol); err == nil {
					if strategy == model.STRATEGY_STOP_LOSS {
						_, err = self.StopLoss(client,
							symbol,
							amount,
							pricing.Multiply(bought, stop, pp),
							model.MARKET,
							strconv.FormatFloat(bought, 'f', -1, 64),
						)
					} else {
						_, _, err = self.Order(client,
							model.SELL,
							symbol,
							amount,
							pricing.Multiply(bought, mult, pp),
							model.LIMIT,
							strconv.FormatFloat(bought, 'f', -1, 64),
						)
					}
				}
			}
		}

		if err != nil {
			var data []byte
			if data, _ = json.Marshal(buy); data == nil {
				self.error(err, level, service)
			} else {
				self.error(errors.Append(err, "\t", string(data)), level, service)
			}
		}
	}

	return new, nil
}

// listens to the open orders, look for cancelled orders, send a notification on newly opened orders.
func (self *Kucoin) listen(
	client *exchange.ApiService,
	service model.Notify,
	level int64,
	old exchange.OrdersModel,
	filled exchange.FillsModel,
) (exchange.OrdersModel, error) {
	var (
		err error
		new exchange.OrdersModel
	)

	// get my opened orders
	if new, err = self.getOrders(client, map[string]string{"status": "active"}); err != nil {
		return old, err
	}

	// look for cancelled orders
	for _, order := range old {
		if new.IndexOfId(order.Id) == -1 {
			// if this order has NOT been FILLED, then it has been cancelled.
			if filled.IndexOfOrderId(order.Id) == -1 {
				var data []byte
				if data, err = json.Marshal(order); err != nil {
					return new, errors.Wrap(err, 1)
				}

				log.Println("[CANCELLED] " + string(data))

				side := model.NewOrderSide(order.Side)
				if side != model.ORDER_SIDE_NONE {
					if service != nil && notify.CanSend(level, notify.CANCELLED) {
						if err = service.SendMessage(order, fmt.Sprintf("Kucoin - Cancelled %s", model.FormatOrderSide(side)), model.ALWAYS); err != nil {
							log.Printf("[ERROR] %v", err)
						}
					}
				}
			}
		}
	}

	// look for new orders
	for _, order := range new {
		if old.IndexOfId(order.Id) == -1 {
			var data []byte
			if data, err = json.Marshal(order); err != nil {
				return new, errors.Wrap(err, 1)
			}

			log.Println("[OPEN] " + string(data))

			if service != nil {
				side := model.NewOrderSide(order.Side)
				if side != model.ORDER_SIDE_NONE {
					if notify.CanSend(level, notify.OPENED) || (level == notify.LEVEL_DEFAULT && side == model.SELL) {
						if err = service.SendMessage(order, ("Kucoin - Open " + model.FormatOrderSide(side)), model.ALWAYS); err != nil {
							log.Printf("[ERROR] %v", err)
						}
					}
				}
			}
		}
	}

	return new, nil
}

func (self *Kucoin) Sell(
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
		err           error
		apiKey        string
		apiSecret     string
		apiPassphrase string
	)
	if apiKey, apiSecret, apiPassphrase, err = promptForApiKeysEx("KuCoin"); err != nil {
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

	client := exchange.NewApiService(
		exchange.ApiBaseURIOption(self.baseURI(sandbox)),
		exchange.ApiKeyOption(apiKey),
		exchange.ApiSecretOption(apiSecret),
		exchange.ApiPassPhraseOption(apiPassphrase),
		exchange.ApiPartnerIdOption(kucoinPartnerID),
		exchange.ApiPartnerIdOption(kucoinPartnerSecret),
	)

	// get my filled orders
	var filled exchange.FillsModel
	if filled, err = self.getRecentFills(client, 500); err != nil {
		return err
	}

	// get my opened orders
	var opened exchange.OrdersModel
	if opened, err = self.getOrders(client, map[string]string{"status": "active"}); err != nil {
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
			stop  multiplier.Mult
		)
		if level, err = notify.Level(); err != nil {
			self.error(err, level, service)
		} else if mult, err = multiplier.Get(multiplier.FIVE_PERCENT); err != nil {
			self.error(err, level, service)
		} else if stop, err = multiplier.Stop(); err != nil {
			self.error(err, level, service)
		} else
		// listens to the filled orders, look for newly filled orders, automatically place new sell orders.
		if filled, err = self.sell(client, strategy, mult, stop, hold, earn, service, twitter, level, filled, sandbox, debug); err != nil {
			self.error(err, level, service)
		} else
		// listens to the open orders, look for cancelled orders, send a notification on newly opened orders.
		if opened, err = self.listen(client, service, level, opened, filled); err != nil {
			self.error(err, level, service)
		} else
		// listens to the open orders, follow up on the stop loss strategy
		if strategy == model.STRATEGY_STOP_LOSS {
			cache := make(map[string]float64)
			for _, order := range opened {
				// enumerate over stop loss orders
				if order.Stop == "loss" {
					ticker, ok := cache[order.Symbol]
					if !ok {
						if ticker, err = self.GetTicker(client, order.Symbol); err == nil {
							cache[order.Symbol] = ticker
						}
					}
					if ticker > 0 {
						var prec int
						if prec, err = self.GetPricePrec(client, order.Symbol); err == nil {
							bought := order.ParseStopPrice() / float64(stop)
							if ticker >= pricing.Multiply(bought, mult, prec) {
								if _, err = client.CancelStopOrder(order.Id); err == nil {
									_, _, err = self.Order(client,
										model.SELL,
										order.Symbol,
										order.ParseSize(),
										0, model.MARKET, "",
									)
								}
							}
						}
						if err != nil {
							var data []byte
							if data, _ = json.Marshal(order); data == nil {
								self.error(err, level, service)
							} else {
								self.error(errors.Append(err, "\t", string(data)), level, service)
							}
						}
					}
				}
			}
		}
	}
}

func (self *Kucoin) Order(
	client interface{},
	side model.OrderSide,
	market string,
	size float64,
	price float64,
	kind model.OrderType,
	metadata string,
) (oid []byte, raw []byte, err error) {
	var (
		resp  *exchange.ApiResponse
		order exchange.CreateOrderResultModel
	)

	kucoin, ok := client.(*exchange.ApiService)
	if !ok {
		return nil, nil, errors.New("invalid argument: client")
	}

	var params = map[string]string{
		"clientOid": uuid.New().Long(),
		"side":      side.String(),
		"symbol":    market,
		"type":      kind.String(),
		"size":      strconv.FormatFloat(size, 'f', -1, 64),
	}
	if kind == model.LIMIT {
		params["price"] = strconv.FormatFloat(price, 'f', -1, 64)
	}

	if resp, err = kucoin.CreateOrder(params); err != nil {
		return nil, nil, errors.Wrap(err, 1)
	}
	if err = resp.ReadData(&order); err != nil {
		return nil, nil, errors.Wrap(err, 1)
	}
	if raw, err = json.Marshal(order); err != nil {
		return nil, nil, errors.Wrap(err, 1)
	}

	return []byte(order.OrderId), raw, nil
}

func (self *Kucoin) StopLoss(client interface{}, market string, size float64, price float64, kind model.OrderType, metadata string) ([]byte, error) {
	var (
		err   error
		out   []byte
		resp  *exchange.ApiResponse
		order exchange.CreateOrderResultModel
	)

	kucoin, ok := client.(*exchange.ApiService)
	if !ok {
		return nil, errors.New("invalid argument: client")
	}

	var params = map[string]string{
		"clientOid": uuid.New().Long(),
		"side":      model.OrderSideString[model.SELL],
		"symbol":    market,
		"type":      kind.String(),
		"size":      strconv.FormatFloat(size, 'f', -1, 64),
		"stop":      "loss",
		"stopPrice": strconv.FormatFloat(price, 'f', -1, 64),
	}
	if kind == model.LIMIT {
		params["price"] = strconv.FormatFloat(price, 'f', -1, 64)
	}

	if resp, err = kucoin.CreateStopOrder(params); err != nil {
		return nil, errors.Wrap(err, 1)
	}
	if err = resp.ReadData(&order); err != nil {
		return nil, errors.Wrap(err, 1)
	}
	if out, err = json.Marshal(order); err != nil {
		return nil, errors.Wrap(err, 1)
	}

	return out, nil
}

func (self *Kucoin) OCO(client interface{}, market string, size float64, price, stop float64, metadata string) ([]byte, error) {
	return nil, errors.New("Not implemented")
}

func (self *Kucoin) GetClosed(client interface{}, market string) (model.Orders, error) {
	var (
		err   error
		resp  *exchange.ApiResponse
		page  *exchange.PaginationModel
		fills exchange.FillsModel
		out   model.Orders
	)

	kucoin, ok := client.(*exchange.ApiService)
	if !ok {
		return nil, errors.New("invalid argument: client")
	}

	var curr int64 = 1
	for {
		if resp, err = kucoin.Fills(
			map[string]string{"symbol": market},
			&exchange.PaginationParam{CurrentPage: curr, PageSize: 50}); err != nil {
			return nil, errors.Wrap(err, 1)
		}
		if page, err = resp.ReadPaginationData(&fills); err != nil {
			return nil, errors.Wrap(err, 1)
		}
		for _, fill := range fills {
			out = append(out, model.Order{
				Side:      model.NewOrderSide(fill.Side),
				Market:    fill.Symbol,
				Size:      fill.ParseSize(),
				Price:     fill.ParsePrice(),
				CreatedAt: fill.ParseCreatedAt(),
			})
		}
		if page.CurrentPage >= page.TotalPage {
			break
		} else {
			curr++
		}
	}

	return out, nil
}

func (self *Kucoin) GetOpened(client interface{}, market string) (model.Orders, error) {
	var (
		err    error
		out    model.Orders
		orders exchange.OrdersModel
	)

	kucoin, ok := client.(*exchange.ApiService)
	if !ok {
		return nil, errors.New("invalid argument: client")
	}

	if orders, err = self.getOrders(kucoin, map[string]string{"status": "active", "symbol": market}); err != nil {
		return nil, err
	}

	for _, order := range orders {
		out = append(out, model.Order{
			Side:      model.NewOrderSide(order.Side),
			Market:    order.Symbol,
			Size:      order.ParseSize(),
			Price:     order.ParsePrice(),
			CreatedAt: order.ParseCreatedAt(),
		})
	}

	return out, nil
}

func (self *Kucoin) GetBook(client interface{}, market string, side model.BookSide) (interface{}, error) {
	var (
		err  error
		out  []exchange.BookEntry
		resp *exchange.ApiResponse
		book exchange.FullOrderBookModel
	)

	kucoin, ok := client.(*exchange.ApiService)
	if !ok {
		return 0, errors.New("invalid argument: client")
	}

	if resp, err = kucoin.AggregatedFullOrderBook(market); err != nil {
		return nil, errors.Wrap(err, 1)
	}
	if err = resp.ReadData(&book); err != nil {
		return nil, errors.Wrap(err, 1)
	}

	if side == model.BOOK_SIDE_ASKS {
		out = book.Asks
	} else {
		out = book.Bids
	}

	return out, nil
}

func (self *Kucoin) Aggregate(client, book interface{}, market string, agg float64) (model.Book, error) {
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

func (self *Kucoin) GetTicker(client interface{}, market string) (float64, error) {
	var (
		err    error
		out    float64
		resp   *exchange.ApiResponse
		ticker exchange.TickerLevel1Model
	)

	kucoin, ok := client.(*exchange.ApiService)
	if !ok {
		return 0, errors.New("invalid argument: client")
	}

	if resp, err = kucoin.TickerLevel1(market); err != nil {
		return 0, errors.Wrap(err, 1)
	}
	if err = resp.ReadData(&ticker); err != nil {
		return 0, errors.Wrap(err, 1)
	}
	if out, err = strconv.ParseFloat(ticker.Price, 64); err != nil {
		return 0, errors.Wrap(err, 1)
	}

	return out, nil
}

func (self *Kucoin) Get24h(client interface{}, market string) (*model.Stats, error) {
	kucoin, ok := client.(*exchange.ApiService)
	if !ok {
		return nil, errors.New("invalid argument: client")
	}

	var (
		err  error
		resp *exchange.ApiResponse
		json exchange.Stats24hrModel
	)
	if resp, err = kucoin.Stats24hr(market); err != nil {
		return nil, errors.Wrap(err, 1)
	}
	if err = resp.ReadData(&json); err != nil {
		return nil, errors.Wrap(err, 1)
	}

	var (
		high float64
		low  float64
	)
	if high, err = strconv.ParseFloat(json.High, 64); err != nil {
		return nil, err
	}
	if low, err = strconv.ParseFloat(json.Low, 64); err != nil {
		return nil, err
	}

	return &model.Stats{
		Market: market,
		High:   high,
		Low:    low,
		BtcVolume: func() float64 {
			symbol, err := self.getSymbol(kucoin, market)
			if err == nil {
				if strings.EqualFold(symbol.QuoteCurrency, model.BTC) {
					out, err := strconv.ParseFloat(json.VolValue, 64)
					if err == nil {
						return out
					}
				}
			}
			return 0
		}(),
	}, nil
}

func (self *Kucoin) GetPricePrec(client interface{}, market string) (int, error) {
	kucoin, ok := client.(*exchange.ApiService)
	if !ok {
		return 8, errors.New("invalid argument: client")
	}
	symbol, err := self.getSymbol(kucoin, market)
	if err != nil {
		return 8, err
	}
	return precision.Parse(symbol.PriceIncrement, 8), nil
}

func (self *Kucoin) GetSizePrec(client interface{}, market string) (int, error) {
	kucoin, ok := client.(*exchange.ApiService)
	if !ok {
		return 0, errors.New("invalid argument: client")
	}
	symbol, err := self.getSymbol(kucoin, market)
	if err != nil {
		return 0, err
	}
	return precision.Parse(symbol.BaseIncrement, 0), nil
}

func (self *Kucoin) GetMaxSize(client interface{}, base, quote string, hold, earn bool, def float64, mult multiplier.Mult) float64 {
	if hold {
		if base == "KCS" {
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

func (self *Kucoin) Cancel(client interface{}, market string, side model.OrderSide) error {
	var (
		err    error
		orders exchange.OrdersModel
	)

	kucoin, ok := client.(*exchange.ApiService)
	if !ok {
		return errors.New("invalid argument: client")
	}

	if orders, err = self.getOrders(kucoin, map[string]string{
		"status": "active",
		"symbol": market,
		"side":   side.String(),
	}); err != nil {
		return err
	}

	for _, order := range orders {
		if _, err = kucoin.CancelOrder(order.Id); err != nil {
			return errors.Wrap(err, 1)
		}
	}

	return nil
}

func (self *Kucoin) Buy(client interface{}, cancel bool, market string, calls model.Calls, deviation float64, kind model.OrderType) error {
	var err error

	kucoin, ok := client.(*exchange.ApiService)
	if !ok {
		return errors.New("invalid argument: client")
	}

	// delete the buy order(s) that are open in your book
	if cancel {
		var orders exchange.OrdersModel
		if orders, err = self.getOrders(kucoin, map[string]string{
			"status": "active",
			"symbol": market,
			"side":   "buy",
			"type":   "limit",
		}); err != nil {
			return err
		}
		for _, order := range orders {
			// do not cancel orders that we're about to re-place
			index := calls.IndexByPrice(order.ParsePrice())
			if index > -1 && order.ParseSize() == calls[index].Size {
				calls[index].Skip = true
			} else {
				if _, err = kucoin.CancelOrder(order.Id); err != nil {
					return errors.Wrap(err, 1)
				}
			}
		}
	}

	// open the top X buy orders
	for _, call := range calls {
		if !call.Skip {
			qty := call.Size

			// respect the baseMinSize
			var min float64
			if min, err = self.getMinSize(kucoin, market); err != nil {
				return err
			}
			if qty < min {
				qty = min
			}

			limit := call.Price
			if deviation != 1.0 {
				kind, limit = call.Deviate(self, client, kind, deviation)
			}

			if _, _, err = self.Order(client,
				model.BUY,
				market,
				qty,
				limit,
				kind, "",
			); err != nil {
				return err
			}
		}
	}

	return nil
}

func (self *Kucoin) IsLeveragedToken(name string) bool {
	return len(name) > 2 && (strings.HasSuffix(strings.ToUpper(name), "3L") || strings.HasSuffix(strings.ToUpper(name), "3S"))
}

func (self *Kucoin) HasAlgoOrder(client interface{}, market string) (bool, error) {
	return false, nil
}

func newKucoin() model.Exchange {
	return &Kucoin{
		ExchangeInfo: &model.ExchangeInfo{
			Code: "KUCN",
			Name: "KuCoin",
			URL:  "https://www.kucoin.com",
			REST: model.Endpoint{
				URI:     "https://api.kucoin.com",
				Sandbox: "https://openapi-sandbox.kucoin.com",
			},
			Country: "Hong Kong",
		},
	}
}
