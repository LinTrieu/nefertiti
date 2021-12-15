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
	"github.com/lintrieu/nefertiti/model"
	"github.com/lintrieu/nefertiti/multiplier"
	"github.com/lintrieu/nefertiti/notify"
	"github.com/lintrieu/nefertiti/precision"
	"github.com/lintrieu/nefertiti/pricing"
	"github.com/lintrieu/nefertiti/session"
	exchange "github.com/lintrieu/nefertiti/woo"
)

var (
	wooMutex *filemutex.FileMutex
)

const (
	wooSessionFile = "woo.time"
	wooSessionLock = "woo.lock"
)

func init() {
	exchange.BeforeRequest = func(method, path string, rps float64) error {
		var err error

		if wooMutex == nil {
			if wooMutex, err = filemutex.New(session.GetSessionFile(wooSessionLock)); err != nil {
				return err
			}
		}

		if err = wooMutex.Lock(); err != nil {
			return err
		}

		var lastRequest *time.Time
		if lastRequest, err = session.GetLastRequest(wooSessionFile); err != nil {
			return err
		}

		if lastRequest != nil {
			elapsed := time.Since(*lastRequest)
			if elapsed.Seconds() < (float64(1) / rps) {
				sleep := time.Duration((float64(time.Second) / rps) - float64(elapsed))
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
			wooMutex.Unlock()
		}()
		session.SetLastRequest(wooSessionFile, time.Now())
	}
}

type Woo struct {
	*model.ExchangeInfo
	symbols []exchange.Symbol
}

func (self *Woo) error(err error, level int64, service model.Notify) {
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
			err := service.SendMessage(msg, "Woo - ERROR", model.ONCE_PER_MINUTE)
			if err != nil {
				log.Printf("[ERROR] %v", err)
			}
		}
	}
}

func (self *Woo) indexByOrderID(orders []exchange.Order, id int64) int {
	for i, o := range orders {
		if o.OrderID == id {
			return i
		}
	}
	return -1
}

func (self *Woo) getBaseURL(sandbox bool) string {
	if sandbox {
		return self.ExchangeInfo.REST.Sandbox
	}
	return self.ExchangeInfo.REST.URI
}

func (self *Woo) getSymbols(client *exchange.Client, cached bool) ([]exchange.Symbol, error) {
	if self.symbols == nil || !cached {
		var err error
		if self.symbols, err = client.Symbols(); err != nil {
			return nil, errors.Wrap(err, 1)
		}
	}
	return self.symbols, nil
}

func (self *Woo) getSymbol(client *exchange.Client, market string, cached bool) (*exchange.Symbol, error) {
	symbols, err := self.getSymbols(client, cached)
	if err != nil {
		return nil, err
	}

	for _, symbol := range symbols {
		if symbol.Symbol == market {
			return &symbol, nil
		}
	}

	return nil, errors.Errorf("symbol %v does not exist", market)
}

func (self *Woo) GetInfo() *model.ExchangeInfo {
	return self.ExchangeInfo
}

func (self *Woo) GetClient(permission model.Permission, sandbox bool) (interface{}, error) {
	if permission == model.PUBLIC {
		return exchange.New(self.getBaseURL(sandbox), "", ""), nil
	}

	apiKey, apiSecret, err := promptForApiKeys("Woo")
	if err != nil {
		return nil, err
	}

	return exchange.New(self.getBaseURL(sandbox), apiKey, apiSecret), nil
}

func (self *Woo) GetMarkets(cached, sandbox bool, blacklist []string) ([]model.Market, error) {
	var out []model.Market

	symbols, err := self.getSymbols(exchange.New(self.getBaseURL(sandbox), "", ""), cached)
	if err != nil {
		return nil, err
	}

	for _, symbol := range symbols {
		base, quote, err := self.parseMarket(symbol.Symbol)
		if err != nil {
			return nil, err
		}
		if func() bool {
			for _, ignore := range blacklist {
				if strings.EqualFold(symbol.Symbol, ignore) {
					return false
				}
			}
			return true
		}() {
			out = append(out, model.Market{
				Name:  symbol.Symbol,
				Base:  base,
				Quote: quote,
			})
		}
	}

	return out, nil
}

func (self *Woo) FormatMarket(base, quote string) string {
	return exchange.FormatSymbol(base, quote)
}

func (self *Woo) parseMarket(symbol string) (string, string, error) { // -> (base, quote, error)
	return exchange.ParseSymbol(symbol)
}

// listen to the opened orders, look for cancelled orders, send a notification.
func (self *Woo) listen(
	client *exchange.Client,
	service model.Notify,
	level int64,
	old []exchange.Order,
	filled []exchange.Order,
) ([]exchange.Order, error) {
	symbols, err := self.getSymbols(client, true)
	if err != nil {
		return old, err
	}

	// get my opened orders
	var new []exchange.Order
	for _, market := range symbols {
		orders, err := client.Orders(market.Symbol, exchange.OrderStatusIncomplete)
		if err != nil {
			return old, errors.Wrap(err, 1)
		}
		new = append(new, orders...)
	}

	// look for cancelled orders
	for _, order := range old {
		if self.indexByOrderID(new, order.OrderID) == -1 {
			// if this order has NOT been FILLED, then it has been cancelled.
			if self.indexByOrderID(filled, order.OrderID) == -1 {
				data, err := json.Marshal(order)
				if err != nil {
					return new, errors.Wrap(err, 1)
				}

				log.Println("[CANCELLED] " + string(data))

				if service != nil {
					if notify.CanSend(level, notify.CANCELLED) {
						err := service.SendMessage(order, fmt.Sprintf("Woo - Done %v (Reason: Cancelled)", order.Side), model.ALWAYS)
						if err != nil {
							log.Printf("[ERROR] %v", err)
						}
					}
				}
			}
		}
	}

	// look for newly opened orders
	for _, order := range new {
		if self.indexByOrderID(old, order.OrderID) == -1 {
			data, err := json.Marshal(order)
			if err != nil {
				return new, errors.Wrap(err, 1)
			}

			log.Println("[OPEN] " + string(data))

			if service != nil {
				if notify.CanSend(level, notify.OPENED) || (level == notify.LEVEL_DEFAULT && order.Side == exchange.OrderSideSell) {
					err := service.SendMessage(order, fmt.Sprintf("Woo - Open %v", order.Side), model.ALWAYS)
					if err != nil {
						log.Printf("[ERROR] %v", err)
					}
				}
			}
		}
	}

	return new, nil
}

// listen to the filled orders, look for newly filled orders, automatically place new LIMIT SELL orders.
func (self *Woo) sell(
	client *exchange.Client,
	mult multiplier.Mult,
	hold, earn model.Markets,
	service model.Notify,
	level int64,
	old []exchange.Order,
) ([]exchange.Order, error) {
	symbols, err := self.getSymbols(client, true)
	if err != nil {
		return old, err
	}

	// get my filled orders
	var filled []exchange.Order
	for _, market := range symbols {
		orders, err := client.Orders(market.Symbol, exchange.OrderStatusFilled)
		if err != nil {
			return old, errors.Wrap(err, 1)
		}
		filled = append(filled, orders...)
	}

	// make a list of newly filled orders
	var new []exchange.Order
	for _, order := range filled {
		if self.indexByOrderID(old, order.OrderID) == -1 {
			new = append(new, order)
		}
	}

	// send notification(s)
	for _, order := range new {
		data, err := json.Marshal(order)
		if err != nil {
			self.error(err, level, service)
		} else {
			log.Println("[FILLED] " + string(data))
			if notify.CanSend(level, notify.FILLED) && service != nil {
				err := service.SendMessage(order, fmt.Sprintf("Woo - Done %s (Reason: Filled)", order.Side), model.ALWAYS)
				if err != nil {
					log.Printf("[ERROR] %v", err)
				}
			}
		}
	}

	// has a buy order been filled? then place a sell order
	for i := 0; i < len(new); i++ {
		if new[i].Side == exchange.OrderSideBuy {
			qty := new[i].QuantityMinusFee()

			// add up amount(s), hereby preventing a problem with partial matches
			n := i + 1
			for n < len(new) {
				if new[n].Symbol == new[i].Symbol && new[n].Side == new[i].Side && new[n].Price == new[i].Price {
					qty = qty + new[n].QuantityMinusFee()
					new = append(new[:n], new[n+1:]...)
				} else {
					n++
				}
			}

			// round to precision if (a) fees got deducted, or (b) we added up partial matches
			if qty < new[i].Quantity || qty > new[i].QuantityMinusFee() {
				prec, err := self.GetSizePrec(client, new[i].Symbol)
				if err != nil {
					self.error(err, level, service)
				} else {
					qty = precision.Floor(qty, prec)
				}
			}

			// get base currency and desired size, calculate price, place sell order
			base, quote, err := self.parseMarket(new[i].Symbol)
			if err == nil {
				qty = self.GetMaxSize(client, base, quote, hold.HasMarket(new[i].Symbol), earn.HasMarket(new[i].Symbol), qty, mult)
				if qty > 0 {
					var prec int
					prec, err = self.GetPricePrec(client, new[i].Symbol)
					if err == nil {
						_, err = client.Order(
							new[i].Symbol,
							exchange.OrderSideSell,
							exchange.OrderTypeLimit,
							qty,
							pricing.Multiply(new[i].ExecutedAt(), mult, prec),
							"NEF2021xxxxxxx",
						)
					}
				}
			}

			if err != nil {
				data, _ := json.Marshal(new[i])
				if data == nil {
					self.error(err, level, service)
				} else {
					self.error(errors.Append(err, "\t", string(data)), level, service)
				}
			}
		}
	}

	return filled, nil
}

func (self *Woo) Sell(
	strategy model.Strategy,
	hold, earn model.Markets,
	sandbox, tweet, debug bool,
	success model.OnSuccess,
) error {
	if strategy != model.STRATEGY_STANDARD {
		return errors.New("strategy not implemented")
	}

	apiKey, apiSecret, err := promptForApiKeys("Woo")
	if err != nil {
		return err
	}

	service, err := notify.New().Init(flag.Interactive(), true)
	if err != nil {
		return err
	}

	client := exchange.New(self.getBaseURL(sandbox), apiKey, apiSecret)

	symbols, err := self.getSymbols(client, true)
	if err != nil {
		return err
	}

	var (
		filled []exchange.Order
		opened []exchange.Order
	)

	// get my filled orders
	for _, market := range symbols {
		orders, err := client.Orders(market.Symbol, exchange.OrderStatusFilled)
		if err != nil {
			return errors.Wrap(err, 1)
		}
		filled = append(filled, orders...)
	}

	// get my opened orders
	for _, market := range symbols {
		orders, err := client.Orders(market.Symbol, exchange.OrderStatusIncomplete)
		if err != nil {
			return errors.Wrap(err, 1)
		}
		opened = append(opened, orders...)
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
		// listen to the filled orders, look for newly filled orders, automatically place new LIMIT SELL orders.
		if filled, err = self.sell(client, mult, hold, earn, service, level, filled); err != nil {
			self.error(err, level, service)
		} else
		// listen to the opened orders, look for cancelled orders, send a notification.
		if opened, err = self.listen(client, service, level, opened, filled); err != nil {
			self.error(err, level, service)
		}
	}
}

func (self *Woo) Order(
	client interface{},
	side model.OrderSide,
	market string,
	size float64,
	price float64,
	kind model.OrderType,
	metadata string,
) (oid []byte, raw []byte, err error) {
	wooClient, ok := client.(*exchange.Client)
	if !ok {
		return nil, nil, errors.New("invalid argument: client")
	}

	var order *exchange.NewOrder
	if order, err = wooClient.Order(market, func() exchange.OrderSide {
		if side == model.BUY {
			return exchange.OrderSideBuy
		} else {
			return exchange.OrderSideSell
		}
	}(), func() exchange.OrderType {
		if kind == model.MARKET {
			return exchange.OrderTypeMarket
		} else {
			return exchange.OrderTypeLimit
		}
	}(), size, price, metadata); err != nil {
		return nil, nil, errors.Wrap(err, 1)
	}

	if raw, err = json.Marshal(order); err != nil {
		return nil, nil, errors.Wrap(err, 1)
	}

	return []byte(strconv.FormatInt(order.ID, 10)), raw, nil
}

func (self *Woo) StopLoss(client interface{}, market string, size float64, price float64, kind model.OrderType, metadata string) ([]byte, error) {
	return nil, errors.New("not implemented")
}

func (self *Woo) OCO(client interface{}, market string, size float64, price, stop float64, metadata string) ([]byte, error) {
	return nil, errors.New("not implemented")
}

func (self *Woo) GetClosed(client interface{}, market string) (model.Orders, error) {
	wooClient, ok := client.(*exchange.Client)
	if !ok {
		return nil, errors.New("invalid argument: client")
	}

	var (
		err    error
		orders []exchange.Order
		output model.Orders
	)

	if orders, err = wooClient.Orders(market, exchange.OrderStatusFilled); err != nil {
		return nil, errors.Wrap(err, 1)
	}

	for _, order := range orders {
		output = append(output, model.Order{
			Side: func() model.OrderSide {
				if order.Side == exchange.OrderSideSell {
					return model.SELL
				}
				return model.BUY
			}(),
			Market:    market,
			Size:      order.QuantityMinusFee(),
			Price:     order.ExecutedAt(),
			CreatedAt: order.CreatedAt(),
		})
	}

	return output, nil
}

func (self *Woo) GetOpened(client interface{}, market string) (model.Orders, error) {
	wooClient, ok := client.(*exchange.Client)
	if !ok {
		return nil, errors.New("invalid argument: client")
	}

	var (
		err    error
		orders []exchange.Order
		output model.Orders
	)

	if orders, err = wooClient.Orders(market, exchange.OrderStatusIncomplete); err != nil {
		return nil, errors.Wrap(err, 1)
	}

	for _, order := range orders {
		output = append(output, model.Order{
			Side: func() model.OrderSide {
				if order.Side == exchange.OrderSideSell {
					return model.SELL
				}
				return model.BUY
			}(),
			Market:    market,
			Size:      order.Quantity,
			Price:     order.Price,
			CreatedAt: order.CreatedAt(),
		})
	}

	return output, nil
}

func (self *Woo) GetBook(client interface{}, market string, side model.BookSide) (interface{}, error) {
	wooClient, ok := client.(*exchange.Client)
	if !ok {
		return nil, errors.New("invalid argument: client")
	}

	book, err := wooClient.OrderBook(market)
	if err != nil {
		return nil, errors.Wrap(err, 1)
	}

	return func() []exchange.BookEntry {
		if side == model.BOOK_SIDE_ASKS {
			return book.Asks
		} else {
			return book.Bids
		}
	}(), nil
}

func (self *Woo) Aggregate(client, book interface{}, market string, agg float64) (model.Book, error) {
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
			entry.Size = entry.Size + e.Quantity
		} else {
			entry = &model.Buy{
				Market: market,
				Price:  price,
				Size:   e.Quantity,
			}
			out = append(out, *entry)
		}
	}

	return out, nil
}

func (self *Woo) GetTicker(client interface{}, market string) (float64, error) {
	wooClient, ok := client.(*exchange.Client)
	if !ok {
		return 0, errors.New("invalid argument: client")
	}

	ticker, err := wooClient.Ticker(market)
	if err != nil {
		return 0, errors.Wrap(err, 1)
	}

	return ticker.LastPrice, nil
}

func (self *Woo) Get24h(client interface{}, market string) (*model.Stats, error) {
	wooClient, ok := client.(*exchange.Client)
	if !ok {
		return nil, errors.New("invalid argument: client")
	}

	ticker, err := wooClient.Ticker(market)
	if err != nil {
		return nil, errors.Wrap(err, 1)
	}

	return &model.Stats{
		Market: market,
		High:   ticker.HighestPrice24h,
		Low:    ticker.LowestPrice24h,
		BtcVolume: func(ticker1 *exchange.Ticker) float64 {
			_, quote, err := self.parseMarket(market)
			if err == nil {
				if strings.EqualFold(quote, model.BTC) {
					return ticker1.QuoteVolume
				}
				ticker2, err := wooClient.Ticker(self.FormatMarket(model.BTC, quote))
				if err == nil {
					return ticker1.QuoteVolume / ticker2.LastPrice
				}
			}
			return 0
		}(ticker),
	}, nil
}

func (self *Woo) GetPricePrec(client interface{}, market string) (int, error) {
	wooClient, ok := client.(*exchange.Client)
	if !ok {
		return 8, errors.New("invalid argument: client")
	}

	symbol, err := self.getSymbol(wooClient, market, true)
	if err != nil {
		return 8, err
	}

	return precision.Parse(strconv.FormatFloat(symbol.QuoteTick, 'f', -1, 64), 8), nil
}

func (self *Woo) GetSizePrec(client interface{}, market string) (int, error) {
	wooClient, ok := client.(*exchange.Client)
	if !ok {
		return 8, errors.New("invalid argument: client")
	}

	symbol, err := self.getSymbol(wooClient, market, true)
	if err != nil {
		return 0, err
	}

	return precision.Parse(strconv.FormatFloat(symbol.BaseTick, 'f', -1, 64), 0), nil
}

func (self *Woo) GetMaxSize(client interface{}, base, quote string, hold, earn bool, def float64, mult multiplier.Mult) float64 {
	if hold {
		if base == "WOO" {
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

func (self *Woo) Cancel(client interface{}, market string, side model.OrderSide) error {
	wooClient, ok := client.(*exchange.Client)
	if !ok {
		return errors.New("invalid argument: client")
	}

	orders, err := wooClient.Orders(market, exchange.OrderStatusIncomplete)
	if err != nil {
		return errors.Wrap(err, 1)
	}

	for _, order := range orders {
		if ((side == model.BUY) && (order.Side == exchange.OrderSideBuy)) || ((side == model.SELL) && (order.Side == exchange.OrderSideSell)) {
			if err := wooClient.CancelOrder(market, order.OrderID); err != nil {
				return errors.Wrap(err, 1)
			}
		}
	}

	return nil
}

func (self *Woo) Buy(client interface{}, cancel bool, market string, calls model.Calls, deviation float64, kind model.OrderType) error {
	wooClient, ok := client.(*exchange.Client)
	if !ok {
		return errors.New("invalid argument: client")
	}

	// step #1: delete the buy order(s) that are open in your book
	if cancel {
		orders, err := wooClient.Orders(market, exchange.OrderStatusIncomplete)
		if err != nil {
			return errors.Wrap(err, 1)
		}
		for _, order := range orders {
			if order.Side == exchange.OrderSideBuy {
				// do not cancel orders that we're about to re-place
				index := calls.IndexByPrice(order.Price)
				if index > -1 && order.Quantity == calls[index].Size {
					calls[index].Skip = true
				} else {
					if err := wooClient.CancelOrder(market, order.OrderID); err != nil {
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
				qty   float64 = call.Size
				limit float64 = call.Price
			)
			if deviation != 1.0 {
				kind, limit = call.Deviate(self, client, kind, deviation)
			}
			// --- BEGIN --- svanas 2021-07-25 --- order value should be greater or equal to X ----
			symbol, err := self.getSymbol(wooClient, market, true)
			if err != nil {
				return err
			}
			if symbol.MinNotional > 0 {
				if limit == 0 {
					if limit, err = self.GetTicker(client, market); err != nil {
						return err
					}
				}
				if (qty * limit) < symbol.MinNotional {
					prec, err := self.GetSizePrec(client, market)
					if err != nil {
						return err
					}
					qty = precision.Ceil((symbol.MinNotional / limit), prec)
				}
			}
			if symbol.BaseMin > 0 && qty < symbol.BaseMin {
				qty = symbol.BaseMin
			}
			// ---- END ---- svanas 2021-07-25 ----------------------------------------------------
			if _, err := wooClient.Order(market, exchange.OrderSideBuy, func() exchange.OrderType {
				if kind == model.MARKET {
					return exchange.OrderTypeMarket
				}
				return exchange.OrderTypeLimit
			}(), qty, limit, "NEF2021xxxxxxx"); err != nil {
				return errors.Wrap(err, 1)
			}
		}
	}

	return nil
}

func (self *Woo) IsLeveragedToken(name string) bool {
	return false
}

func (self *Woo) HasAlgoOrder(client interface{}, market string) (bool, error) {
	return false, nil
}

func newWoo() model.Exchange {
	return &Woo{
		ExchangeInfo: &model.ExchangeInfo{
			Code: "WOO",
			Name: "Woo",
			URL:  "https://x.woo.network",
			REST: model.Endpoint{
				URI:     "https://api.woo.network",
				Sandbox: "https://api.staging.woo.network",
			},
			WebSocket: model.Endpoint{
				URI:     "wss://wss.woo.network/ws/stream",
				Sandbox: "wss://wss.staging.woo.network/ws/stream",
			},
			Country: "Taiwan",
		},
	}
}
