package model

import (
	"strings"

	"github.com/lintrieu/nefertiti/multiplier"
)

type Permission int

const (
	PRIVATE Permission = iota
	PUBLIC
	BOOK
)

type (
	OnSuccess func(service Notify) error
)

type (
	Endpoint struct {
		URI     string `json:"uri,omitempty"`
		Sandbox string `json:"sandbox,omitempty"`
	}
	ExchangeInfo struct {
		Code      string   `json:"code"`
		Name      string   `json:"name"`
		URL       string   `json:"url"`
		REST      Endpoint `json:"rest"`
		WebSocket Endpoint `json:"websocket,omitempty"`
		Country   string   `json:"country,omitempty"`
	}
)

func (info *ExchangeInfo) Equals(name string) bool {
	return strings.EqualFold(info.Code, name) || strings.EqualFold(info.Name, name)
}

type Exchange interface {
	GetInfo() *ExchangeInfo
	GetClient(permission Permission, sandbox bool) (interface{}, error)
	GetMarkets(cached, sandbox bool, ignore []string) ([]Market, error)
	FormatMarket(base, quote string) string
	Sell(stategy Strategy, hold, earn Markets, sandbox, tweet, debug bool, success OnSuccess) error
	Order(client interface{}, side OrderSide, market string, size float64, price float64, kind OrderType, metadata string) (oid []byte, raw []byte, err error)
	StopLoss(client interface{}, market string, size float64, price float64, kind OrderType, metadata string) ([]byte, error)
	OCO(client interface{}, market string, size float64, price, stop float64, metadata string) ([]byte, error)
	GetClosed(client interface{}, market string) (Orders, error)
	GetOpened(client interface{}, market string) (Orders, error)
	GetBook(client interface{}, market string, side BookSide) (interface{}, error)
	Aggregate(client, book interface{}, market string, agg float64) (Book, error)
	GetTicker(client interface{}, market string) (float64, error)
	Get24h(client interface{}, market string) (*Stats, error)
	GetPricePrec(client interface{}, market string) (int, error)
	GetSizePrec(client interface{}, market string) (int, error)
	GetMaxSize(client interface{}, base, quote string, hold, earn bool, def float64, mult multiplier.Mult) float64
	Cancel(client interface{}, market string, side OrderSide) error
	Buy(client interface{}, cancel bool, market string, calls Calls, deviation float64, kind OrderType) error
	IsLeveragedToken(name string) bool
	HasAlgoOrder(client interface{}, market string) (bool, error)
}
