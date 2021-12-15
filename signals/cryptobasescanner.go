//lint:file-ignore ST1006 receiver name should be a reflection of its identity; don't use generic names such as "this" or "self"
package signals

import (
	"encoding/json"
	"io/ioutil"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/lintrieu/nefertiti/empty"
	"github.com/lintrieu/nefertiti/errors"
	"github.com/lintrieu/nefertiti/flag"
	"github.com/lintrieu/nefertiti/model"
	"github.com/lintrieu/nefertiti/passphrase"
)

type CryptoBaseScannerAlgo int

const (
	CBS_ALGO_ORIGINAL CryptoBaseScannerAlgo = iota
	CBS_ALGO_DAY_TRADE
	CBS_ALGO_CONSERVATIVE
	CBS_ALGO_POSITION
)

var CryptoBaseScannerAlgoString = map[CryptoBaseScannerAlgo]string{
	CBS_ALGO_ORIGINAL:     "original",
	CBS_ALGO_DAY_TRADE:    "day_trade",
	CBS_ALGO_CONSERVATIVE: "conservative",
	CBS_ALGO_POSITION:     "position",
}

func (algo *CryptoBaseScannerAlgo) String() string {
	return CryptoBaseScannerAlgoString[*algo]
}

func NewCryptoBaseScannerAlgo(data string) (CryptoBaseScannerAlgo, error) {
	for algo := range CryptoBaseScannerAlgoString {
		if algo.String() == data {
			return algo, nil
		}
	}
	return CBS_ALGO_DAY_TRADE, errors.Errorf("%s does not exist", data)
}

type MarketStat struct {
	Algorithm  string  `json:"algorithm"`
	Ratio      float64 `json:"ratio,string"`
	MedianDrop float64 `json:"medianDrop,string"`
}

type (
	MarketStats []MarketStat
)

func (stats MarketStats) ratio(algorithm CryptoBaseScannerAlgo) (float64, error) {
	for _, stat := range stats {
		if stat.Algorithm == algorithm.String() {
			return stat.Ratio, nil
		}
	}
	return 0, errors.Errorf("marketStats[%s].ratio does not exist", algorithm.String())
}

func (stats MarketStats) medianDrop(algorithm CryptoBaseScannerAlgo) (float64, error) {
	for _, stat := range stats {
		if stat.Algorithm == algorithm.String() {
			return stat.MedianDrop, nil
		}
	}
	return 0, errors.Errorf("marketStats[%s].medianDrop does not exist", algorithm.String())
}

type CryptoBase struct {
	ExchangeName  string  `json:"exchangeName"`
	BaseCurrency  string  `json:"baseCurrency"`
	QuoteCurrency string  `json:"quoteCurrency"`
	BtcVolume     float64 `json:"btcVolume,string"`
	CurrentPrice  float64 `json:"currentPrice"`
	LatestBase    struct {
		CurrentDrop float64     `json:"currentDrop,string"`
		CrackedAt   interface{} `json:"crackedAt"`
	} `json:"latestBase"`
	MarketStats MarketStats `json:"marketStats"`
}

func (self *CryptoBase) Market(exchange model.Exchange) string {
	return exchange.FormatMarket(self.BaseCurrency, self.QuoteCurrency)
}

func (self *CryptoBase) GetCrackedAt() (*time.Time, error) {
	var (
		err error
		src string
		out time.Time
	)
	src = empty.AsString(self.LatestBase.CrackedAt)
	if src == "" {
		out = time.Now()
	} else {
		if out, err = time.Parse(time.RFC3339, src); err != nil {
			return nil, err
		}
	}
	return &out, nil
}

func (self *CryptoBase) Buy(exchange model.Exchange, algorithm CryptoBaseScannerAlgo, btcVolumeMin, successRatio float64, sandbox, debug bool) (bool, error) {
	market := self.Market(exchange)

	medianDrop, err := self.MarketStats.medianDrop(algorithm)
	if err != nil {
		return false, errors.Errorf("%v. Market: %s", err, market)
	}

	if medianDrop < 0 && medianDrop > -100 {
		if self.LatestBase.CurrentDrop < medianDrop {
			if self.BtcVolume < btcVolumeMin {
				log.Printf("[INFO] Ignoring %s because volume %f is lower than %.2f BTC\n", market, self.BtcVolume, btcVolumeMin)
				return false, nil
			}

			if successRatio > 0 {
				ratio, err := self.MarketStats.ratio(algorithm)
				if err != nil {
					return false, errors.Errorf("%v. Market: %s", err, market)
				}
				if ratio < successRatio {
					log.Printf("[INFO] Ignoring %s because success ratio %f is lower than %.2f\n", market, ratio, successRatio)
					return false, nil
				}
			}

			return true, nil
		} else {
			if debug {
				log.Printf("[DEBUG] Ignoring %s because latestBase.currentDrop: %f is above marketStats[%s].medianDrop: %f\n",
					market,
					self.LatestBase.CurrentDrop,
					algorithm.String(),
					medianDrop)
			}
		}
	}

	return false, nil
}

type (
	CryptoBases []CryptoBase
)

func (bases CryptoBases) IndexByMarket(base *CryptoBase) int {
	for i, b := range bases {
		if (b.ExchangeName == base.ExchangeName) && (b.BaseCurrency == base.BaseCurrency) && (b.QuoteCurrency == base.QuoteCurrency) {
			return i
		}
	}
	return -1
}

type CryptoBaseScannerResponse struct {
	Bases CryptoBases `json:"bases"`
}

type CryptoBaseScanner struct {
	apiKey string
	cache  CryptoBases
}

// https://cryptobasescanner.docs.apiary.io/#
func (self *CryptoBaseScanner) get(
	exchange model.Exchange,
	quote model.Assets,
	algorithm CryptoBaseScannerAlgo,
	btcVolumeMin,
	successRatio float64,
	validity time.Duration,
	sandbox, debug bool,
) error {
	var err error

	// create the request
	var req *http.Request
	if req, err = http.NewRequest("GET", "https://api.cryptobasescanner.com/v1/bases", nil); err != nil {
		return err
	}

	var params = map[string]string{
		"api_key":   self.apiKey,
		"algorithm": algorithm.String(),
		"exchanges": exchange.GetInfo().Code,
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

	// is this an error?
	pairs := make(map[string]interface{})
	if err = json.Unmarshal(body, &pairs); err == nil {
		if err, ok := pairs["error"]; ok {
			return errors.Errorf("%v", err)
		}
	}

	var latest CryptoBaseScannerResponse
	if err = json.Unmarshal(body, &latest); err != nil {
		return err
	}

	// add new bases to the cache
	for _, base := range latest.Bases {
		if exchange.GetInfo().Equals(base.ExchangeName) {
			if quote.HasAsset(base.QuoteCurrency) {
				var buy bool
				if buy, err = base.Buy(exchange, algorithm, btcVolumeMin, successRatio, sandbox, debug); err != nil {
					log.Printf("[ERROR] %v\n", err)
				} else {
					if buy {
						if self.cache.IndexByMarket(&base) == -1 {
							self.cache = append(self.cache, base)
						}
					}
				}
			}
		}
	}

	// remove bases from the cache that are older than 12 hours
	if validity > 0 {
		i := 0
		for i < len(self.cache) {
			var date *time.Time
			if date, err = self.cache[i].GetCrackedAt(); err != nil {
				return err
			}
			if time.Since(*date) > validity {
				self.cache = append(self.cache[:i], self.cache[i+1:]...)
			} else {
				i++
			}
		}
	}

	return nil
}

func (self *CryptoBaseScanner) Init() error {
	if self.apiKey == "" {
		if flag.Listen() {
			return errors.New("missing argument: crypto-base-scanner-key")
		}
		data, err := passphrase.Read("cryptobasescanner.com API key")
		if err == nil {
			self.apiKey = string(data)
		}
	}
	return nil
}

func (self *CryptoBaseScanner) GetName() string {
	return "cryptobasescanner.com"
}

func (self *CryptoBaseScanner) GetValidity() (time.Duration, error) {
	return 12 * time.Hour, nil
}

func (self *CryptoBaseScanner) GetRateLimit() time.Duration {
	return 30 * time.Second
}

func (self *CryptoBaseScanner) GetOrderType() model.OrderType {
	return model.MARKET
}

func (self *CryptoBaseScanner) GetMarkets(
	exchange model.Exchange,
	quote model.Assets,
	btcVolumeMin float64,
	valid time.Duration,
	sandbox, debug bool,
	ignore []string,
) (model.Markets, error) {
	var (
		err error
		flg *flag.Flag
		out model.Markets
	)

	var success_ratio float64 = 60
	flg = flag.Get("success")
	if flg.Exists {
		success_ratio, err = flg.Float64()
		if err != nil {
			return nil, errors.Errorf("success %v is invalid", flg)
		}
	}

	var algo CryptoBaseScannerAlgo = CBS_ALGO_DAY_TRADE
	flg = flag.Get("algo")
	if flg.Exists {
		algo, err = NewCryptoBaseScannerAlgo(flg.String())
		if err != nil {
			return nil, errors.Errorf("algo %v does not exist", flg)
		}
	}

	if err = self.get(exchange, quote, algo, btcVolumeMin, success_ratio, valid, sandbox, debug); err != nil {
		return nil, err
	}

	for _, base := range self.cache {
		if debug {
			var msg []byte
			if msg, err = json.Marshal(base); err == nil {
				log.Printf("[DEBUG] %s", string(msg))
			}
		}
		market := base.Market(exchange)
		if out == nil || out.IndexOf(market) == -1 {
			out = append(out, market)
		}
	}

	return out, nil
}

func (self *CryptoBaseScanner) GetCalls(exchange model.Exchange, market string, sandbox, debug bool) (model.Calls, error) {
	var (
		out model.Calls
	)
	for _, base := range self.cache {
		if strings.EqualFold(base.Market(exchange), market) {
			price := base.CurrentPrice
			if out == nil || out.IndexByPrice(price) == -1 {
				out = append(out, model.Call{
					Buy: &model.Buy{
						Market: market,
						Price:  price,
					},
				})
			}
		}
	}
	return out, nil
}

func NewCryptoBaseScanner() model.Channel {
	return &CryptoBaseScanner{
		apiKey: flag.Get("crypto-base-scanner-key").String(),
	}
}
