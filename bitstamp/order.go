package bitstamp

import (
	"strings"
	"time"

	"github.com/lintrieu/nefertiti/errors"
)

type Order struct {
	Id           string  `json:"id"`
	DateTime     string  `json:"datetime"`
	Type         int     `json:"type,string"`
	Price        float64 `json:"price,string"`
	Amount       float64 `json:"amount,string"`
	CurrencyPair string  `json:"currency_pair,omitempty"` // warning: NOT equal to market name
}

const (
	BUY  = "buy"
	SELL = "sell"
)

func (order *Order) Side() string {
	if order.Type == 0 {
		return BUY
	}
	if order.Type == 1 {
		return SELL
	}
	return ""
}

func (order *Order) Market() (string, error) {
	var out string

	for i := 0; i < len(order.CurrencyPair); i++ {
		c := order.CurrencyPair[i]
		if c >= 'A' && c <= 'Z' {
			out = out + string(c)
		}
	}

	if out == "" {
		return "", errors.New("currency_pair is empty")
	}

	return strings.ToLower(out), nil
}

func (order *Order) MarketEx() string {
	out, err := order.Market()
	if err == nil {
		return out
	}
	return ""
}

func (order *Order) GetDateTime() (*time.Time, error) {
	var (
		err error
		out time.Time
	)
	if out, err = time.Parse(TimeFormat, order.DateTime); err != nil {
		return nil, errors.Wrap(err, 1)
	}
	return &out, nil
}

func (order *Order) GetDateTimeEx() time.Time {
	out, err := order.GetDateTime()
	if err == nil {
		return *out
	}
	return time.Time{}
}

type (
	Orders []Order
)

func (orders Orders) IndexById(id string) int {
	for i, o := range orders {
		if o.Id == id {
			return i
		}
	}
	return -1
}

func (orders Orders) OrderById(id string) *Order {
	i := orders.IndexById(id)
	if i != -1 {
		return &orders[i]
	}
	return nil
}
