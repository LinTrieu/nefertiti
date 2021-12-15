package ethplorer

import (
	"strconv"

	"github.com/lintrieu/nefertiti/empty"
)

type (
	Price struct {
		Rate interface{} `json:"rate"`
		Diff float64     `json:"diff"`
	}
)

func (price *Price) ParseRate() float64 {
	if out, err := strconv.ParseFloat(empty.AsString(price.Rate), 64); err != nil {
		return out
	}
	return empty.AsFloat64(price.Rate)
}
