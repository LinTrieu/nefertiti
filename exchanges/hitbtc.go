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
	exchange "github.com/lintrieu/nefertiti/hitbtc"
	"github.com/lintrieu/nefertiti/model"
	"github.com/lintrieu/nefertiti/multiplier"
	"github.com/lintrieu/nefertiti/notify"
	"github.com/lintrieu/nefertiti/precision"
	"github.com/lintrieu/nefertiti/pricing"
	"github.com/lintrieu/nefertiti/session"
	"github.com/lintrieu/nefertiti/uuid"
)

var (
	hitbtcMutex *filemutex.FileMutex
)

const (
	hitbtcSessionFile = "hitbtc.time"
	hitbtcSessionLock = "hitbtc.lock"
)

func init() {
	exchange.BeforeRequest = func(method, path string) error {
		var err error

		if hitbtcMutex == nil {
			if hitbtcMutex, err = filemutex.New(session.GetSessionFile(hitbtcSessionLock)); err != nil {
				return err
			}
		}

		if err = hitbtcMutex.Lock(); err != nil {
			return err
		}

		var lastRequest *time.Time
		if lastRequest, err = session.GetLastRequest(hitbtcSessionFile); err != nil {
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
			hitbtcMutex.Unlock()
		}()
		session.SetLastRequest(hitbtcSessionFile, time.Now())
	}
}

type (
	hitbtcOrders []exchange.Order
)

func (orders hitbtcOrders) indexById(id uint64) int {
	for i, o := range orders {
		if o.Id == id {
			return i
		}
	}
	return -1
}

type (
	hitbtcTrades []exchange.Trade
)

func (trades hitbtcTrades) indexByOrderId(id uint64) int {
	for i, t := range trades {
		if t.OrderId == id {
			return i
		}
	}
	return -1
}

type HitBTC struct {
	*model.ExchangeInfo
	symbols   []exchange.Symbol
	updatedAt time.Time // the last time we pulled symbols
}

// We can track API requests using ClientOrderID field. The string has 32 symbols,
// we suggest to use the first 8 symbols as a unique partner ID which we assign to
// you. Other 24 symbols are a unique order ID generated on your end.
func (self *HitBTC) getUniquePartnerId() string {
	out := uuid.New().LongEx("")
	out = "refzzz18" + out[8:]
	return out
}

func (self *HitBTC) error(err error, level int64, service model.Notify) {
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
			if err.Error() != "502 Bad Gateway" {
				err := service.SendMessage(msg, "HitBTC - ERROR", model.ONCE_PER_MINUTE)
				if err != nil {
					log.Printf("[ERROR] %v", err)
				}
			}
		}
	}
}

func (self *HitBTC) getSymbol(client *exchange.HitBtc, name string) (*exchange.Symbol, error) {
	cached := true
	for {
		symbols, err := self.getSymbols(client, cached)
		if err != nil {
			return nil, err
		}
		for _, symbol := range symbols {
			if symbol.Id == name {
				return &symbol, nil
			}
		}
		if !cached {
			return nil, errors.Errorf("symbol %s does not exist", name)
		}
		cached = false
	}
}

func (self *HitBTC) getSymbols(client *exchange.HitBtc, cached bool) (symbols []exchange.Symbol, err error) {
	if self.symbols == nil || !cached || time.Since(self.updatedAt).Minutes() > 30 {
		if self.symbols, err = client.GetSymbols(); err != nil {
			return nil, errors.Wrap(err, 1)
		}
		self.updatedAt = time.Now()
	}
	return self.symbols, nil
}

func (self *HitBTC) getOrderSide(order *exchange.Order) model.OrderSide {
	if order.Side == "buy" {
		return model.BUY
	} else {
		if order.Side == "sell" {
			return model.SELL
		}
	}
	return model.ORDER_SIDE_NONE
}

func (self *HitBTC) getTradeSide(trade *exchange.Trade) model.OrderSide {
	if trade.Side == "buy" {
		return model.BUY
	} else {
		if trade.Side == "sell" {
			return model.SELL
		}
	}
	return model.ORDER_SIDE_NONE
}

func (self *HitBTC) GetInfo() *model.ExchangeInfo {
	return self.ExchangeInfo
}

func (self *HitBTC) GetClient(permission model.Permission, sandbox bool) (interface{}, error) {
	if permission != model.PRIVATE {
		return exchange.New("", ""), nil
	}

	apiKey, apiSecret, err := promptForApiKeys("HitBTC")
	if err != nil {
		return nil, err
	}

	return exchange.New(apiKey, apiSecret), nil
}

func (self *HitBTC) GetMarkets(cached, sandbox bool, ignore []string) ([]model.Market, error) {
	var out []model.Market

	client := exchange.New("", "")

	symbols, err := self.getSymbols(client, cached)
	if err != nil {
		return nil, err
	}

	for _, symbol := range symbols {
		out = append(out, model.Market{
			Name:  symbol.Id,
			Base:  symbol.BaseCurrency,
			Quote: symbol.QuoteCurrency,
		})
	}

	return out, nil
}

func (self *HitBTC) FormatMarket(base, quote string) string {
	return strings.ToUpper(base + quote)
}

// listens to the open orders, look for cancelled orders, send a notification.
func (self *HitBTC) listen(
	client *exchange.HitBtc,
	service model.Notify,
	level int64,
	old hitbtcOrders,
	filled hitbtcTrades,
) (hitbtcOrders, error) {
	var err error

	// get my new open orders
	var new hitbtcOrders
	if new, err = client.GetOpenOrders("all"); err != nil {
		return old, errors.Wrap(err, 1)
	}

	// look for cancelled orders
	for _, order := range old {
		if new.indexById(order.Id) == -1 {
			// if this order has NOT been FILLED, then it has been cancelled.
			if filled.indexByOrderId(order.Id) == -1 {
				var data []byte
				if data, err = json.Marshal(order); err != nil {
					return new, errors.Wrap(err, 1)
				}

				log.Println("[CANCELLED] " + string(data))

				side := self.getOrderSide(&order)
				if side != model.ORDER_SIDE_NONE {
					if service != nil && notify.CanSend(level, notify.CANCELLED) {
						if err = service.SendMessage(order, fmt.Sprintf("HitBTC - Done %s (Reason: Cancelled)", model.FormatOrderSide(side)), model.ALWAYS); err != nil {
							log.Printf("[ERROR] %v", err)
						}
					}
				}
			}
		}
	}

	// look for new orders
	for _, order := range new {
		if old.indexById(order.Id) == -1 {
			var data []byte
			if data, err = json.Marshal(order); err != nil {
				return new, errors.Wrap(err, 1)
			}

			log.Println("[OPEN] " + string(data))

			if service != nil {
				side := self.getOrderSide(&order)
				if side != model.ORDER_SIDE_NONE {
					if notify.CanSend(level, notify.OPENED) || (level == notify.LEVEL_DEFAULT && side == model.SELL) {
						if err = service.SendMessage(order, ("HitBTC - Open " + model.FormatOrderSide(side)), model.ALWAYS); err != nil {
							log.Printf("[ERROR] %v", err)
						}
					}
				}
			}
		}
	}

	return new, nil
}

// listens to the filled orders, look for newly filled orders, automatically place new sell orders.
func (self *HitBTC) sell(
	client *exchange.HitBtc,
	strategy model.Strategy,
	mult multiplier.Mult,
	hold, earn model.Markets,
	service model.Notify,
	twitter *notify.TwitterKeys,
	level int64,
	old hitbtcTrades,
	sandbox bool,
) (hitbtcTrades, error) {
	var err error

	// get the markets
	var markets []model.Market
	if markets, err = self.GetMarkets(true, sandbox, nil); err != nil {
		return old, err
	}

	// get my filled orders
	var new []exchange.Trade
	if new, err = client.GetTrades("all"); err != nil {
		return old, errors.Wrap(err, 1)
	}

	// send notification(s)
	for _, trade := range new {
		if old.indexByOrderId(trade.OrderId) == -1 {
			var data []byte
			if data, err = json.Marshal(trade); err != nil {
				return new, errors.Wrap(err, 1)
			}

			log.Println("[FILLED] " + string(data))

			if notify.CanSend(level, notify.FILLED) {
				if service != nil {
					if err = service.SendMessage(trade, fmt.Sprintf("HitBTC - Done %s (Reason: Filled)", strings.Title(trade.Side)), model.ALWAYS); err != nil {
						log.Printf("[ERROR] %v", err)
					}
				}
				if twitter != nil {
					notify.Tweet(twitter, fmt.Sprintf("Done %s. %s priced at %f #HitBTC", strings.Title(trade.Side), model.TweetMarket(markets, trade.Symbol), trade.Price))
				}
			}
		}
	}

	// has a buy order been filled? then place a sell order
	for i := 0; i < len(new); i++ {
		if old.indexByOrderId(new[i].OrderId) == -1 {
			side := model.NewOrderSide(new[i].Side)
			if side == model.BUY {
				qty := new[i].Quantity

				// add up amount(s), hereby preventing a problem with partial matches
				n := i + 1
				for n < len(new) {
					if new[n].Symbol == new[i].Symbol && new[n].Side == new[i].Side && new[n].Price == new[i].Price {
						qty = qty + new[n].Quantity
						new = append(new[:n], new[n+1:]...)
					} else {
						n++
					}
				}

				price := new[i].Price
				if price == 0 {
					if price, err = self.GetTicker(client, new[i].Symbol); err != nil {
						return new, err
					}
				}

				// get base currency and desired size, calculate price, place sell order
				var (
					base  string
					quote string
				)
				base, quote, err = model.ParseMarket(markets, new[i].Symbol)
				if err == nil {
					qty = self.GetMaxSize(client, base, quote, hold.HasMarket(new[i].Symbol), earn.HasMarket(new[i].Symbol), qty, mult)
					if qty > 0 {
						var prec int
						if prec, err = self.GetPricePrec(client, new[i].Symbol); err == nil {
							_, _, err = self.Order(client,
								model.SELL,
								new[i].Symbol,
								qty,
								pricing.Multiply(price, mult, prec),
								model.LIMIT,
								strconv.FormatFloat(price, 'f', -1, 64),
							)
						}
					}
				}

				if err != nil {
					var data []byte
					if data, _ = json.Marshal(new[i]); data == nil {
						self.error(err, level, service)
					} else {
						self.error(errors.Append(err, "\t", string(data)), level, service)
					}

				}
			}
		}
	}

	return new, nil
}

func (self *HitBTC) Sell(
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
	if apiKey, apiSecret, err = promptForApiKeys("HitBTC"); err != nil {
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

	// get my filled orders
	var filled []exchange.Trade
	if filled, err = client.GetTrades("all"); err != nil {
		return errors.Wrap(err, 1)
	}

	// get my open orders
	var opened []exchange.Order
	if opened, err = client.GetOpenOrders("all"); err != nil {
		return errors.Wrap(err, 1)
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
		// listens to the filled orders, look for newly filled orders, automatically place new sell orders.
		if filled, err = self.sell(client, strategy, mult, hold, earn, service, twitter, level, filled, sandbox); err != nil {
			self.error(err, level, service)
		} else
		// listens to the open orders, look for cancelled orders, send a notification.
		if opened, err = self.listen(client, service, level, opened, filled); err != nil {
			self.error(err, level, service)
		}
	}
}

func (self *HitBTC) Order(
	client interface{},
	side model.OrderSide,
	market string,
	size float64,
	price float64,
	kind model.OrderType,
	metadata string,
) (oid []byte, raw []byte, err error) {
	hitbtc, ok := client.(*exchange.HitBtc)
	if !ok {
		return nil, nil, errors.New("invalid argument: client")
	}

	var order exchange.Order
	if kind == model.LIMIT {
		order, err = hitbtc.PlaceOrder(
			self.getUniquePartnerId(),
			market,
			model.OrderSideString[side],
			exchange.ORDER_TYPE_LIMIT,
			exchange.GTC,
			size,
			price,
			0,
		)
	} else {
		order, err = hitbtc.PlaceOrder(
			self.getUniquePartnerId(),
			market,
			model.OrderSideString[side],
			exchange.ORDER_TYPE_MARKET,
			exchange.GTC,
			size,
			0,
			0,
		)
	}
	if err != nil {
		return nil, nil, errors.Wrap(err, 1)
	}

	var out []byte
	if out, err = json.Marshal(order); err != nil {
		return nil, nil, errors.Wrap(err, 1)
	}

	return []byte(order.ClientOrderId), out, nil
}

func (self *HitBTC) StopLoss(client interface{}, market string, size float64, price float64, kind model.OrderType, metadata string) ([]byte, error) {
	var err error

	hitbtc, ok := client.(*exchange.HitBtc)
	if !ok {
		return nil, errors.New("invalid argument: client")
	}

	var order exchange.Order
	if kind == model.LIMIT {
		order, err = hitbtc.PlaceOrder(
			self.getUniquePartnerId(),
			market,
			model.OrderSideString[model.SELL],
			exchange.ORDER_TYPE_STOP_LIMIT,
			exchange.GTC,
			size,
			price,
			price,
		)
	} else {
		order, err = hitbtc.PlaceOrder(
			self.getUniquePartnerId(),
			market,
			model.OrderSideString[model.SELL],
			exchange.ORDER_TYPE_STOP_MARKET,
			exchange.GTC,
			size,
			0,
			price,
		)
	}
	if err != nil {
		return nil, errors.Wrap(err, 1)
	}

	var out []byte
	if out, err = json.Marshal(order); err != nil {
		return nil, errors.Wrap(err, 1)
	}

	return out, nil
}

func (self *HitBTC) OCO(client interface{}, market string, size float64, price, stop float64, metadata string) ([]byte, error) {
	return nil, errors.New("Not implemented")
}

func (self *HitBTC) GetClosed(client interface{}, market string) (model.Orders, error) {
	var err error

	hitbtc, ok := client.(*exchange.HitBtc)
	if !ok {
		return nil, errors.New("invalid argument: client")
	}

	var trades []exchange.Trade
	if trades, err = hitbtc.GetTrades(market); err != nil {
		return nil, errors.Wrap(err, 1)
	}

	var out model.Orders
	for _, trade := range trades {
		out = append(out, model.Order{
			Side:   self.getTradeSide(&trade),
			Market: trade.Symbol,
			Size:   trade.Quantity,
			Price:  trade.Price,
		})
	}

	return out, nil
}

func (self *HitBTC) GetOpened(client interface{}, market string) (model.Orders, error) {
	var err error

	hitbtc, ok := client.(*exchange.HitBtc)
	if !ok {
		return nil, errors.New("invalid argument: client")
	}

	var orders []exchange.Order
	if orders, err = hitbtc.GetOpenOrders(market); err != nil {
		return nil, errors.Wrap(err, 1)
	}

	var out model.Orders
	for _, order := range orders {
		out = append(out, model.Order{
			Side:      self.getOrderSide(&order),
			Market:    order.Symbol,
			Size:      order.Quantity,
			Price:     order.ParsePrice(),
			CreatedAt: order.Created,
		})
	}

	return out, nil
}

func (self *HitBTC) GetBook(client interface{}, market string, side model.BookSide) (interface{}, error) {
	var err error

	hitbtc, ok := client.(*exchange.HitBtc)
	if !ok {
		return nil, errors.New("invalid argument: client")
	}

	var book exchange.Book
	if book, err = hitbtc.GetOrderBook(market, 0); err != nil {
		return nil, errors.Wrap(err, 1)
	}

	var out []exchange.BookEntry
	if side == model.BOOK_SIDE_ASKS {
		out = book.Ask
	} else {
		out = book.Bid
	}

	return out, nil
}

func (self *HitBTC) Aggregate(client, book interface{}, market string, agg float64) (model.Book, error) {
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
		price := precision.Round(aggregation.Round(e.Price, agg), prec)
		entry := out.EntryByPrice(price)
		if entry != nil {
			entry.Size = entry.Size + e.Size
		} else {
			entry = &model.Buy{
				Market: market,
				Price:  price,
				Size:   e.Size,
			}
			out = append(out, *entry)
		}
	}

	return out, nil
}

func (self *HitBTC) GetTicker(client interface{}, market string) (float64, error) {
	hitbtc, ok := client.(*exchange.HitBtc)
	if !ok {
		return 0, errors.New("invalid argument: client")
	}

	ticker, err := hitbtc.GetTicker(market)
	if err != nil {
		return 0, errors.Wrap(err, 1)
	}

	return ticker.Last, nil
}

func (self *HitBTC) Get24h(client interface{}, market string) (*model.Stats, error) {
	hitbtc, ok := client.(*exchange.HitBtc)
	if !ok {
		return nil, errors.New("invalid argument: client")
	}

	ticker, err := hitbtc.GetTicker(market)
	if err != nil {
		return nil, errors.Wrap(err, 1)
	}

	return &model.Stats{
		Market: market,
		High:   ticker.High,
		Low:    ticker.Low,
		BtcVolume: func() float64 {
			symbol, err := self.getSymbol(hitbtc, market)
			if err == nil {
				if strings.EqualFold(symbol.QuoteCurrency, model.BTC) {
					return ticker.VolumeQuote
				}
			}
			return 0
		}(),
	}, nil
}

func (self *HitBTC) GetPricePrec(client interface{}, market string) (int, error) {
	hitbtc, ok := client.(*exchange.HitBtc)
	if !ok {
		return 8, errors.New("invalid argument: client")
	}
	symbol, err := self.getSymbol(hitbtc, market)
	if err != nil {
		return 8, err
	}
	return precision.Parse(strconv.FormatFloat(symbol.TickSize, 'f', -1, 64), 8), nil
}

func (self *HitBTC) GetSizePrec(client interface{}, market string) (int, error) {
	hitbtc, ok := client.(*exchange.HitBtc)
	if !ok {
		return 0, errors.New("invalid argument: client")
	}
	symbol, err := self.getSymbol(hitbtc, market)
	if err != nil {
		return 0, err
	}
	return precision.Parse(strconv.FormatFloat(symbol.QuantityIncrement, 'f', -1, 64), 0), nil
}

func (self *HitBTC) GetMaxSize(client interface{}, base, quote string, hold, earn bool, def float64, mult multiplier.Mult) float64 {
	if hold {
		if base == "HIT" {
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

func (self *HitBTC) Cancel(client interface{}, market string, side model.OrderSide) error {
	var err error

	hitbtc, ok := client.(*exchange.HitBtc)
	if !ok {
		return errors.New("invalid argument: client")
	}

	var orders []exchange.Order
	if orders, err = hitbtc.GetOpenOrders(market); err != nil {
		return errors.Wrap(err, 1)
	}

	for _, order := range orders {
		if self.getOrderSide(&order) == side {
			if _, err = hitbtc.CancelClientOrderId(order.ClientOrderId); err != nil {
				return errors.Wrap(err, 1)
			}
		}
	}

	return nil
}

func (self *HitBTC) Buy(client interface{}, cancel bool, market string, calls model.Calls, deviation float64, kind model.OrderType) error {
	var err error

	hitbtc, ok := client.(*exchange.HitBtc)
	if !ok {
		return errors.New("invalid argument: client")
	}

	// step #1: delete the buy order(s) that are open in your book
	if cancel {
		var orders []exchange.Order
		if orders, err = hitbtc.GetOpenOrders(market); err != nil {
			return errors.Wrap(err, 1)
		}
		for _, order := range orders {
			side := self.getOrderSide(&order)
			if side == model.BUY {
				// do not cancel orders that we're about to re-place
				index := calls.IndexByPrice(order.ParsePrice())
				if index > -1 {
					calls[index].Skip = true
				} else {
					if _, err = hitbtc.CancelClientOrderId(order.ClientOrderId); err != nil {
						return errors.Wrap(err, 1)
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
			_, _, err = self.Order(client,
				model.BUY,
				market,
				call.Size,
				limit,
				kind, "",
			)
			if err != nil {
				return err
			}
		}
	}

	return nil
}

func (self *HitBTC) IsLeveragedToken(name string) bool {
	return false
}

func (self *HitBTC) HasAlgoOrder(client interface{}, market string) (bool, error) {
	return false, nil
}

func newHitBTC() model.Exchange {
	return &HitBTC{
		ExchangeInfo: &model.ExchangeInfo{
			Code: "HITB",
			Name: "HitBTC",
			URL:  "https://hitbtc.com/",
			REST: model.Endpoint{
				URI:     "https://api.hitbtc.com/api/2",
				Sandbox: "https://api.demo.hitbtc.com/api/2",
			},
			WebSocket: model.Endpoint{
				URI:     "wss://api.hitbtc.com/api/2/ws",
				Sandbox: "wss://api.demo.hitbtc.com/api/2/ws",
			},
			Country: "Hong-Kong",
		},
	}
}
