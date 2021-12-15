package model

import (
	"fmt"

	"github.com/lintrieu/nefertiti/flag"
)

type Strategy int

const (
	STRATEGY_STANDARD Strategy = iota
	STRATEGY_STOP_LOSS
)

func GetStrategy() (Strategy, error) {
	new := flag.Get("stoploss")
	if new.Exists {
		str := new.String()
		if len(str) > 0 && (str[0] == 'Y' || str[0] == 'y') {
			return STRATEGY_STOP_LOSS, nil
		}
		if len(str) == 0 || (str[0] != 'N' && str[0] != 'n') {
			return STRATEGY_STANDARD, fmt.Errorf("stoploss %v is invalid. valid values are Y or N", new)
		}
	}

	old := flag.Get("strategy")
	if old.Exists {
		out, err := old.Int64()
		if err != nil {
			return STRATEGY_STANDARD, fmt.Errorf("strategy %v is invalid. valid values are 0 or 4", old)
		}
		if out == 4 {
			return STRATEGY_STOP_LOSS, nil
		}
	}

	if !new.Exists {
		flag.Set("stoploss", "N")
	}

	return STRATEGY_STANDARD, nil
}
