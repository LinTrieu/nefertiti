//lint:file-ignore ST1006 receiver name should be a reflection of its identity; don't use generic names such as "this" or "self"
package signals

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/lintrieu/nefertiti/errors"
	"github.com/lintrieu/nefertiti/flag"
	"github.com/lintrieu/nefertiti/model"
	"github.com/lintrieu/nefertiti/passphrase"
	"github.com/lintrieu/nefertiti/precision"
)

// Possible values for risk_level are:
const (
	RISK_LEVEL_MINIMUM = 1 // pretty rare
	RISK_LEVEL_LOW     = 2
	RISK_LEVEL_MEDIUM  = 3 // pretty common
	RISK_LEVEL_HIGH    = 4 // for really risky trades
	RISK_LEVEL_MAXIMUM = 5
)

type QualitySignal struct {
	TimeStamp string  `json:"timestamp"`
	Exchange  string  `json:"exchange"`
	Quote     string  `json:"currency"`
	Base      string  `json:"coin"`
	BuyStart  float64 `json:"buy_start,string"`
	BuyEnd    float64 `json:"buy_end,string"`
	Target1   string  `json:"target1"`
	StopLoss  string  `json:"stop_loss"`
	Ask       float64 `json:"ask,string"`
	RiskLevel int64   `json:"risk_level,string"`
	Skip      bool
}

func (self *QualitySignal) Buy(exchange model.Exchange, risk_level int64, debug bool) bool {
	if self.RiskLevel > risk_level {
		if debug {
			log.Printf("[INFO] Ignoring %s because risk is (too) high.\n", self.Market(exchange))
		}
		return false
	}
	if self.Skip {
		if debug {
			log.Printf("[INFO] Ignoring %s because price has already pumped 5%% (or more).\n", self.Market(exchange))
		}
		return false
	}
	return true
}

func (self *QualitySignal) Price() float64 {
	if self.BuyStart > self.BuyEnd {
		return self.BuyStart
	} else {
		return self.BuyEnd
	}
}

func (self *QualitySignal) Market(exchange model.Exchange) string {
	return exchange.FormatMarket(self.Base, self.Quote)
}

func (self *QualitySignal) GetTimeStamp() (*time.Time, error) {
	var (
		err error
		out time.Time
	)
	if out, err = time.Parse("2006-01-02 15:04:05", self.TimeStamp); err != nil {
		return nil, err
	}
	return &out, nil
}

type (
	QualitySignals []QualitySignal
)

func (signals QualitySignals) IndexByMarket(signal *QualitySignal) int {
	for i, s := range signals {
		if (s.Exchange == signal.Exchange) && (s.Quote == signal.Quote) && (s.Base == signal.Base) {
			return i
		}
	}
	return -1
}

type QualityResponse struct {
	Error   int            `json:"error"`
	Message string         `json:"message"`
	Count   int            `json:"count"`
	Signals QualitySignals `json:"signals"`
}

type CryptoQualitySignals struct {
	apiKey string
	cache  QualitySignals
}

func (self *CryptoQualitySignals) get(exchange model.Exchange, validity time.Duration, sandbox bool) error {
	var err error

	// create the request
	var req *http.Request
	if req, err = http.NewRequest("GET", "https://api.cryptoqualitysignals.com/v1/getSignal", nil); err != nil {
		return err
	}

	var params = map[string]string{
		"api_key":  self.apiKey,
		"exchange": exchange.GetInfo().Name,
		"interval": strconv.FormatFloat(validity.Minutes(), 'f', -1, 64),
	}

	qry := req.URL.Query()
	for key, value := range params {
		qry.Set(key, value)
	}
	req.URL.RawQuery = qry.Encode()

	// submit the http request
	var resp *http.Response
	if resp, err = http.DefaultClient.Do(req); err != nil {
		return err
	}

	// read the body of the http message into a byte array
	var body []byte
	if body, err = ioutil.ReadAll(resp.Body); err != nil {
		return err
	}
	defer resp.Body.Close()

	var latest QualityResponse
	if err = json.Unmarshal(body, &latest); err != nil {
		return err
	}

	if latest.Error > 0 {
		return errors.New(latest.Message)
	}

	// add the new signals to the cache
	for _, signal := range latest.Signals {
		if self.cache.IndexByMarket(&signal) == -1 {
			self.cache = append(self.cache, signal)
		}
	}

	// remove signals from the cache that have disappeared from the response
	idx := 0
	for idx < len(self.cache) {
		if latest.Signals.IndexByMarket(&self.cache[idx]) == -1 {
			self.cache = append(self.cache[:idx], self.cache[idx+1:]...)
		} else {
			idx++
		}
	}

	// mark the signals in the cache that have already pumped 5% -- we should be ignoring those
	if len(self.cache) > 0 {
		cache := make(map[string]float64)
		var client interface{}
		if client, err = exchange.GetClient(model.PUBLIC, sandbox); err != nil {
			return err
		}
		for idx := range self.cache {
			ask := self.cache[idx].Ask
			if ask > 0 {
				market := self.cache[idx].Market(exchange)
				ticker, ok := cache[market]
				if !ok {
					if ticker, err = exchange.GetTicker(client, market); err == nil {
						cache[market] = ticker
					}
				}
				if ticker > (ask * 1.05) {
					self.cache[idx].Skip = true
				}
			}
		}
	}

	return nil
}

func (self *CryptoQualitySignals) Init() error {
	const (
		API_KEY_FREE = "FREE"
	)
	if self.apiKey == "" {
		if flag.Listen() {
			return errors.New("missing argument: quality-signals-key")
		}
		data, err := passphrase.Read("cryptoqualitysignals.com API key")
		if err == nil {
			self.apiKey = string(data)
		}
	}
	if self.apiKey == "" {
		self.apiKey = API_KEY_FREE
	}
	if self.apiKey != API_KEY_FREE {
		// validate the api key
		resp, err := http.Get(fmt.Sprintf("https://premium.cryptoqualitysignals.com/api/validate/subscription/%s/5BB1713C16223", self.apiKey))
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return errors.New("Invalid or expired API key")
		}
	}
	return nil
}

func (self *CryptoQualitySignals) GetName() string {
	return "cryptoqualitysignals.com"
}

func (self *CryptoQualitySignals) GetValidity() (time.Duration, error) {
	return 15 * time.Minute, nil
}

func (self *CryptoQualitySignals) GetRateLimit() time.Duration {
	return 30 * time.Second
}

func (self *CryptoQualitySignals) GetOrderType() model.OrderType {
	return model.LIMIT
}

func (self *CryptoQualitySignals) GetMarkets(
	exchange model.Exchange,
	quote model.Assets,
	btcVolumeMin float64,
	valid time.Duration,
	sandbox, debug bool,
	ignore []string,
) (model.Markets, error) {
	var err error

	if err = self.get(exchange, valid, sandbox); err != nil {
		return nil, err
	}

	if debug {
		if len(self.cache) > 0 {
			var msg []byte
			if msg, err = json.Marshal(self.cache); err == nil {
				log.Printf("[DEBUG] %s", string(msg))
			}
		}
	}

	var risk_level int64 = RISK_LEVEL_HIGH
	flg := flag.Get("risk_level")
	if flg.Exists {
		risk_level, err = flg.Int64()
		if err != nil {
			return nil, errors.Errorf("risk_level %v is invalid", flg)
		}
	}

	var out model.Markets
	for _, signal := range self.cache {
		if signal.Buy(exchange, risk_level, debug) {
			if quote.HasAsset(signal.Quote) {
				market := signal.Market(exchange)
				if out == nil || out.IndexOf(market) == -1 {
					out = append(out, market)
				}
			}
		}
	}

	return out, nil
}

func (self *CryptoQualitySignals) GetCalls(exchange model.Exchange, market string, sandbox, debug bool) (model.Calls, error) {
	var (
		err error
		out model.Calls
	)

	var client interface{}
	if client, err = exchange.GetClient(model.PUBLIC, sandbox); err != nil {
		return nil, err
	}

	var prec int
	if prec, err = exchange.GetPricePrec(client, market); err != nil {
		return nil, err
	}

	var risk_level int64 = RISK_LEVEL_HIGH
	flg := flag.Get("risk_level")
	if flg.Exists {
		risk_level, err = flg.Int64()
		if err != nil {
			return nil, errors.Errorf("risk_level %v is invalid", flg)
		}
	}

	for _, signal := range self.cache {
		if strings.EqualFold(signal.Market(exchange), market) {
			if signal.Buy(exchange, risk_level, debug) {
				price := precision.Round(signal.Price(), prec)
				if out == nil || out.IndexByPrice(price) == -1 {
					out = append(out, model.Call{
						Buy: &model.Buy{
							Market: market,
							Price:  price,
						},
						Stop:   signal.StopLoss,
						Target: signal.Target1,
					})
				}
			}
		}
	}

	return out, nil
}

func NewQualitySignals() model.Channel {
	return &CryptoQualitySignals{
		apiKey: flag.Get("quality-signals-key").String(),
	}
}
