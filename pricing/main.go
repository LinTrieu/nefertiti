package pricing

import (
	"fmt"
	"strconv"

	"github.com/lintrieu/nefertiti/multiplier"
)

func Multiply(price float64, mult multiplier.Mult, prec int) float64 {
	var (
		multiplied float64
		rounded    float64
	)
	multiplied = price * float64(mult)
	if multiplied != 0 {
		for {
			rounded, _ = strconv.ParseFloat(fmt.Sprintf("%.[2]*[1]f", multiplied, prec), 64)
			if (mult > 1 && rounded > price) || (mult < 1 && rounded < price) || (mult == 1) {
				break
			} else {
				if mult >= 1 {
					multiplied = multiplied * 1.01
				} else {
					multiplied = multiplied * 0.99
				}
			}
		}
	}
	return rounded
}
