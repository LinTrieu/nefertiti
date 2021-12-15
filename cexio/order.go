package cexio

import (
	"strconv"
	"time"

	"github.com/lintrieu/nefertiti/empty"
)

type Side int

const (
	SIDE_UNKNOWN Side = iota
	BUY
	SELL
)

var SideString = map[Side]string{
	SIDE_UNKNOWN: "",
	BUY:          "buy",
	SELL:         "sell",
}

func (side *Side) String() string {
	return SideString[*side]
}

func NewSide(data string) Side {
	for side := range SideString {
		if side.String() == data {
			return side
		}
	}
	return SIDE_UNKNOWN
}

type Order struct {
	Id      string      `json:"id"`
	Time    interface{} `json:"time"`
	Type    string      `json:"type"`
	Price   float64     `json:"price,string"`
	Amount  float64     `json:"amount,string"`
	Pending float64     `json:"pending,string"`
	Symbol1 string      `json:"symbol1"`
	Symbol2 string      `json:"symbol2"`
}

func (order *Order) Side() Side {
	return NewSide(order.Type)
}

func (order *Order) GetTime() (time.Time, error) {
	sec, err := strconv.ParseInt(empty.AsString(order.Time), 10, 64)
	if err != nil {
		return time.Unix(0, 0), err
	}
	return time.Unix(sec/1000, 0), nil
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
