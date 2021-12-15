//lint:file-ignore ST1006 receiver name should be a reflection of its identity; don't use generic names such as "this" or "self"
package signals

import (
	"encoding/json"
	"log"
	"strings"
	"time"

	"github.com/lintrieu/nefertiti/errors"
	"github.com/lintrieu/nefertiti/flag"
	"github.com/lintrieu/nefertiti/model"
	"github.com/lintrieu/nefertiti/passphrase"
	mininghamster "github.com/svanas/go-mining-hamster"
)

type MiningHamster struct {
	apiKey string
	cache  mininghamster.Signals
}

func NewMiningHamster() model.Channel {
	return &MiningHamster{
		apiKey: flag.Get("mining-hamster-key").String(),
	}
}

func (self *MiningHamster) get(exchange model.Exchange, validity time.Duration) error {
	var err error
	client := mininghamster.New(self.apiKey)

	// get the latest signal
	var signals mininghamster.Signals
	if signals, err = client.Get(); err != nil {
		return err
	}

	// add the latest signal to the cache
	for _, signal := range signals {
		if self.cache.IndexOf(&signal) == -1 {
			if strings.EqualFold(signal.SignalMode, "buy") {
				if exchange.GetInfo().Equals(signal.Exchange) {
					self.cache = append(self.cache, signal)
				}
			}
		}
	}

	// remove signals from the cache that are older than 5 minutes
	if validity > 0 {
		i := 0
		for i < len(self.cache) {
			if time.Since(self.cache[i].Time) > validity {
				self.cache = append(self.cache[:i], self.cache[i+1:]...)
			} else {
				i++
			}
		}
	}

	return nil
}

func (self *MiningHamster) Init() error {
	if self.apiKey == "" {
		if flag.Listen() {
			return errors.New("missing argument: mining-hamster-key")
		}
		data, err := passphrase.Read("MiningHamster API key")
		if err == nil {
			self.apiKey = string(data)
		}
	}
	return nil
}

func (self *MiningHamster) GetName() string {
	return "MiningHamster"
}

func (self *MiningHamster) GetValidity() (time.Duration, error) {
	return 5 * time.Minute, nil
}

func (self *MiningHamster) GetRateLimit() time.Duration {
	return 30 * time.Second
}

func (self *MiningHamster) GetOrderType() model.OrderType {
	return model.MARKET
}

func (self *MiningHamster) GetMarkets(
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

	err = self.get(exchange, valid)
	if err != nil {
		if strings.Contains(err.Error(), "no signal due to btc price") {
			log.Printf("[INFO] %v", err)
			return nil, nil
		}
		return nil, err
	}

	for _, signal := range self.cache {
		if debug {
			var msg []byte
			if msg, err = json.Marshal(signal); err == nil {
				log.Printf("[DEBUG] %s", string(msg))
			}
		}
		if quote.HasAsset(signal.QuoteCurrency()) {
			market := exchange.FormatMarket(signal.BaseCurrency(), signal.QuoteCurrency())
			if out == nil || out.IndexOf(market) == -1 {
				out = append(out, market)
			}
		}
	}

	return out, nil
}

func (self *MiningHamster) GetCalls(exchange model.Exchange, market string, sandbox, debug bool) (model.Calls, error) {
	var (
		out model.Calls
	)
	for _, signal := range self.cache {
		if strings.EqualFold(exchange.FormatMarket(signal.BaseCurrency(), signal.QuoteCurrency()), market) {
			price := signal.LastPrice
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
