package bitstamp

import (
	"strings"

	"github.com/lintrieu/nefertiti/errors"
	"github.com/lintrieu/nefertiti/precision"
)

type Market struct {
	Name      string `json:"name"`
	Enabled   bool   `json:"enabled"`
	SizePrec  int    `json:"sizePrec"`
	PricePrec int    `json:"pricePrec"`
	Base      string // 1st listed currency of this market pair
	Quote     string // 2nd listed currency of this market pair
}

var markets []Market

func getMarkets(client *Client) ([]Market, error) {
	var out []Market
	pairs, err := client.TradingPairsInfo()
	if err != nil {
		return nil, err
	}
	for _, pair := range pairs {
		out = append(out, Market{
			Name:      pair.UrlSymbol,
			Enabled:   strings.EqualFold(pair.Trading, "enabled"),
			SizePrec:  pair.BaseDecimals,
			PricePrec: pair.CounterDecimals,
			Base:      strings.ToLower(strings.Split(pair.Name, "/")[0]),
			Quote:     strings.ToLower(strings.Split(pair.Name, "/")[1]),
		})
	}
	return out, nil
}

func GetMarkets(client *Client, cached bool) ([]Market, error) {
	if markets == nil || !cached {
		var err error
		if markets, err = getMarkets(client); err != nil {
			return nil, err
		}
	}
	return markets, nil
}

func GetMinimumOrder(client *Client, market string) (float64, error) {
	pairs, err := client.TradingPairsInfo()
	if err != nil {
		return 0, err
	}
	for _, pair := range pairs {
		if strings.EqualFold(pair.UrlSymbol, market) {
			return pair.getMinimumOrder()
		}
	}
	return 0, errors.Errorf("market %s does not exist", market)
}

func GetMinOrderSize(client *Client, market string, ticker float64, prec int) (float64, error) {
	min, err := GetMinimumOrder(client, market)
	if err != nil {
		return 0, err
	}
	return precision.Round((min / ticker), prec), nil
}
