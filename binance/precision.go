//lint:file-ignore ST1006 receiver name should be a reflection of its identity; don't use generic names such as "this" or "self"
package binance

import (
	"context"
	"fmt"
	"strconv"
	"time"

	exchange "github.com/adshao/go-binance/v2"
	"github.com/lintrieu/nefertiti/precision"
)

type (
	Prec struct {
		Symbol exchange.Symbol
		Price  int
		Size   int
		Min    float64 // minimum notional value (aka price * quantity) allowed for an order
	}
	Precs []Prec
)

func (self Precs) indexBySymbol(symbol string) int {
	for i, p := range self {
		if p.Symbol.Symbol == symbol {
			return i
		}
	}
	return -1
}

func (self Precs) PrecFromSymbol(symbol string) *Prec {
	i := self.indexBySymbol(symbol)
	if i != -1 {
		return &self[i]
	}
	return nil
}

type Cache struct {
	precs     Precs
	updatedAt time.Time
}

var cache *Cache

func getPrecs(client *Client) (Precs, error) {
	var out Precs

	defer AfterRequest()
	BeforeRequest(client, WEIGHT_EXCHANGE_INFO)

	info, err := client.inner.NewExchangeInfoService().Do(context.Background())
	if err != nil {
		client.handleError(err)
		return nil, err
	} else {
		for _, symbol := range info.Symbols {
			prec := Prec{
				Symbol: symbol,
			}
			for _, filter := range symbol.Filters {
				if filter["filterType"] == string(exchange.SymbolFilterTypeLotSize) {
					if val, ok := filter["stepSize"]; ok {
						if str, ok := val.(string); ok {
							prec.Size = precision.Parse(str, 0)
						}
					}
				}
				if filter["filterType"] == string(exchange.SymbolFilterTypePriceFilter) {
					if val, ok := filter["tickSize"]; ok {
						if str, ok := val.(string); ok {
							prec.Price = precision.Parse(str, 8)
						}
					}
				}
				if filter["filterType"] == string(exchange.SymbolFilterTypeMinNotional) {
					if val, ok := filter["minNotional"]; ok {
						if str, ok := val.(string); ok {
							if prec.Min, err = strconv.ParseFloat(str, 64); err != nil {
								return nil, err
							}
						}
					}
				}
			}
			out = append(out, prec)
		}
	}
	return out, nil
}

func GetPrecs(client *Client, cached bool) (Precs, error) {
	if cache == nil || !cached || time.Since(cache.updatedAt).Minutes() > 15 {
		latest, err := getPrecs(client)
		if err != nil {
			return nil, err
		}
		cache = &Cache{
			precs:     latest,
			updatedAt: time.Now(),
		}
	}
	return cache.precs, nil
}

func GetSymbol(client *Client, name string) (*exchange.Symbol, error) {
	var (
		err    error
		precs  Precs
		cached bool = true
	)
	for {
		if precs, err = GetPrecs(client, cached); err != nil {
			return nil, err
		}
		prec := precs.PrecFromSymbol(name)
		if prec != nil {
			return &prec.Symbol, nil
		}
		if !cached {
			return nil, fmt.Errorf("symbol %s does not exist", name)
		}
		cached = false
	}
}
