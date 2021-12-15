//lint:file-ignore ST1006 receiver name should be a reflection of its identity; don't use generic names such as "this" or "self"
package signals

import (
	"strings"
	"time"

	"github.com/lintrieu/nefertiti/model"
)

type Listing struct {
	Market  string
	Price   float64
	Created time.Time
}

type Listings struct {
	old    []model.Market
	client interface{}
	cache  []Listing
}

func (self *Listings) Init() error {
	return nil
}

func (self *Listings) GetName() string {
	return "listings"
}

func (self *Listings) GetValidity() (time.Duration, error) {
	return 1 * time.Hour, nil
}

func (self *Listings) GetRateLimit() time.Duration {
	return 1 * time.Minute
}

func (self *Listings) GetOrderType() model.OrderType {
	return model.MARKET
}

func (self *Listings) getClient(exchange model.Exchange, sandbox bool) (interface{}, error) {
	if self.client == nil {
		var err error
		self.client, err = exchange.GetClient(model.PUBLIC, sandbox)
		if err != nil {
			return nil, err
		}
	}
	return self.client, nil
}

func (self *Listings) GetMarkets(
	exchange model.Exchange,
	quote model.Assets,
	btcVolumeMin float64,
	valid time.Duration,
	sandbox, debug bool,
	ignore []string,
) (model.Markets, error) {
	// get the (non-cached) markets
	var (
		err error
		new []model.Market
	)
	if new, err = exchange.GetMarkets(false, sandbox, ignore); err != nil {
		return nil, err
	}

	// if we're unaware of any markets, set our starting point and wait for the next iteration
	if self.old == nil {
		self.old = new
		return nil, nil
	}

	for _, market := range new {
		// is this a new listing?
		if model.IndexByMarket(self.old, market.Name) == -1 {
			var client interface{}
			client, err = self.getClient(exchange, sandbox)
			if err == nil {
				// do we actually have a ticker price yet?
				var ticker float64
				ticker, err = exchange.GetTicker(client, market.Name)
				if err == nil && ticker > 0 {
					if quote.HasAsset(market.Quote) {
						self.cache = append(self.cache, Listing{
							Market:  market.Name,
							Price:   ticker,
							Created: time.Now(),
						})
					}
					// add this new market to the markets we're aware about
					self.old = append(self.old, market)
				}
			}
		}
	}

	// remove listings that are older than 1 hour
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

	var out model.Markets
	for _, listing := range self.cache {
		out = append(out, listing.Market)
	}

	return out, nil
}

func (self *Listings) GetCalls(exchange model.Exchange, market string, sandbox, debug bool) (model.Calls, error) {
	var (
		out model.Calls
	)
	for _, listing := range self.cache {
		if strings.EqualFold(listing.Market, market) {
			out = append(out, model.Call{
				Buy: &model.Buy{
					Market: market,
					Price:  listing.Price,
				},
			})
		}
	}
	return out, nil
}

func NewListings() model.Channel {
	return &Listings{}
}
