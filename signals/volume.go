//lint:file-ignore ST1006 receiver name should be a reflection of its identity; don't use generic names such as "this" or "self"
package signals

import (
	"fmt"
	"log"
	"sort"
	"time"

	"github.com/lintrieu/nefertiti/ethplorer"
	"github.com/lintrieu/nefertiti/flag"
	"github.com/lintrieu/nefertiti/model"
	"github.com/lintrieu/nefertiti/passphrase"
)

type signal struct {
	Market  string
	Created time.Time
}

type cache []signal

func (c cache) indexByMarket(market string) int {
	for i, s := range c {
		if s.Market == market {
			return i
		}
	}
	return -1
}

type Volume struct {
	apiKey string
	cache
}

func (self *Volume) Init() error {
	if self.apiKey == "" {
		data, err := passphrase.Read("ethplorer.io API key")
		if err == nil {
			self.apiKey = string(data)
		}
		if self.apiKey == "" {
			self.apiKey = ethplorer.FREE_KEY
		}
	}
	return nil
}

func (self *Volume) GetName() string {
	return "volume"
}

func (self *Volume) GetValidity() (time.Duration, error) {
	return 12 * time.Hour, nil
}

func (self *Volume) GetRateLimit() time.Duration {
	return 1 * time.Minute
}

func (self *Volume) GetOrderType() model.OrderType {
	return model.MARKET
}

func (self *Volume) get(
	exchange model.Exchange,
	quote model.Assets,
	btcVolumeMin,
	diff float64,
	valid time.Duration,
	sandbox, debug bool,
	ignore []string,
) error {
	var err error

	client := ethplorer.New(self.apiKey)

	var top []ethplorer.Top
	if top, err = client.GetTop(ethplorer.ByTradeVolume); err != nil {
		return err
	}

	// sort by volume diff (highest volume diff first)
	sort.Slice(top, func(i1, i2 int) bool {
		return top[i1].VolumeDiff() > top[i2].VolumeDiff()
	})

	for _, token := range top {
		if token.Buy(diff) {
			var markets []model.Market
			if markets, err = exchange.GetMarkets(true, sandbox, ignore); err != nil {
				return err
			}
			for _, asset := range quote {
				market := exchange.FormatMarket(token.Symbol, asset)
				if model.HasMarket(markets, market) {
					if debug {
						log.Printf("[DEBUG] %s %.2f%%", token.Symbol, token.VolumeDiff())
					}
					if self.cache.indexByMarket(market) == -1 {
						self.cache = append(self.cache, signal{
							Market:  market,
							Created: time.Now(),
						})
					}
				}
			}
		}
	}

	// remove signals from the cache that are older than 12 hours
	if valid > 0 {
		i := 0
		for i < len(self.cache) {
			if time.Since(self.cache[i].Created) > valid {
				self.cache = append(self.cache[:i], self.cache[i+1:]...)
			} else {
				i++
			}
		}
	}

	return nil
}

func (self *Volume) GetMarkets(
	exchange model.Exchange,
	quote model.Assets,
	btcVolumeMin float64,
	valid time.Duration,
	sandbox, debug bool,
	ignore []string,
) (model.Markets, error) {
	var (
		err error
		out model.Markets
	)

	// minimal volume diff: 500%
	var diff float64 = 500
	flg := flag.Get("diff")
	if flg.Exists {
		if diff, err = flg.Float64(); err != nil {
			return nil, fmt.Errorf("diff %v is invalid", flg)
		}
		if debug {
			log.Printf("[INFO] Looking for a volume diff of at least %.2f%%", diff)
		}
	}

	if err = self.get(exchange, quote, btcVolumeMin, diff, valid, sandbox, debug, ignore); err != nil {
		return nil, err
	}

	for _, signal := range self.cache {
		if out == nil || out.IndexOf(signal.Market) == -1 {
			out = append(out, signal.Market)
		}
	}

	return out, nil
}

func (self *Volume) GetCalls(exchange model.Exchange, market string, sandbox, debug bool) (model.Calls, error) {
	var out model.Calls
	out = append(out, model.Call{
		Buy: &model.Buy{
			Market: market,
		},
	})
	return out, nil
}

func NewVolume() model.Channel {
	return &Volume{
		apiKey: flag.Get("ethplorer-api-key").String(),
	}
}
