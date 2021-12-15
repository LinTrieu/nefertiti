package bitstamp

import (
	"strconv"
	"strings"

	"github.com/lintrieu/nefertiti/errors"
)

type Pair struct {
	BaseDecimals    int    `json:"base_decimals"`
	MinimumOrder    string `json:"minimum_order"`
	Name            string `json:"name"`
	CounterDecimals int    `json:"counter_decimals"`
	Trading         string `json:"trading"`
	UrlSymbol       string `json:"url_symbol"`
	Description     string `json:"description"`
}

func (p *Pair) getMinimumOrder() (float64, error) {
	str := p.MinimumOrder
	if str == "" {
		return 0, errors.New("minimum_order is empty")
	}

	var num string
	for i := 0; i < len(str); i++ {
		c := string(str[i])
		if strings.Contains("0123456789.", c) {
			num = num + c
		} else {
			break
		}
	}

	out, err := strconv.ParseFloat(num, 64)
	if err != nil {
		return 0, errors.Wrap(err, 1)
	}

	return out, nil
}
