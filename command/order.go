package command

import (
	"fmt"
	"strings"

	"github.com/lintrieu/nefertiti/errors"
	"github.com/lintrieu/nefertiti/exchanges"
	"github.com/lintrieu/nefertiti/flag"
	"github.com/lintrieu/nefertiti/model"
	"github.com/lintrieu/nefertiti/multiplier"
	"github.com/lintrieu/nefertiti/pricing"
)

type (
	OrderCommand struct {
		*CommandMeta
	}
)

func (c *OrderCommand) Run(args []string) int {
	var (
		err error
		flg *flag.Flag
	)

	var exchange model.Exchange
	if exchange, err = exchanges.GetExchange(); err != nil {
		return c.ReturnError(err)
	}

	flg = flag.Get("side")
	if !flg.Exists {
		return c.ReturnError(errors.New("missing argument: side"))
	}
	side := model.NewOrderSide(flg.String())
	if side == model.ORDER_SIDE_NONE {
		return c.ReturnError(errors.Errorf("side %v is invalid", flg))
	}

	var kind model.OrderType = model.LIMIT
	flg = flag.Get("type")
	if flg.Exists {
		kind = model.NewOrderType(flg.String())
		if kind == model.ORDER_TYPE_NONE {
			return c.ReturnError(errors.Errorf("type %v is invalid", flg))
		}
	}

	var market string
	if market, err = model.GetMarket(exchange); err != nil {
		return c.ReturnError(err)
	}

	var size float64
	flg = flag.Get("size")
	if !flg.Exists {
		return c.ReturnError(errors.New("missing argument: size"))
	}
	if size, err = flg.Float64(); err != nil {
		return c.ReturnError(errors.Errorf("size %v is invalid", flg))
	}

	var price float64 = 0
	flg = flag.Get("price")
	if flg.Exists {
		if price, err = flg.Float64(); err != nil {
			return c.ReturnError(errors.Errorf("price %v is invalid", flg))
		}
	} else if kind == model.LIMIT {
		return c.ReturnError(errors.New("missing argument: price"))
	}

	var client interface{}
	if client, err = exchange.GetClient(model.PRIVATE, flag.Sandbox()); err != nil {
		return c.ReturnError(err)
	}

	var mult multiplier.Mult
	if mult, err = multiplier.Get(1.0); err != nil {
		return c.ReturnError(err)
	} else if mult != 1.0 {
		var prec int
		if prec, err = exchange.GetPricePrec(client, market); err != nil {
			return c.ReturnError(err)
		} else {
			price = pricing.Multiply(price, mult, prec)
		}
	}

	var out []byte
	if _, out, err = exchange.Order(
		client,
		side,
		market,
		size,
		price,
		kind, "",
	); err != nil {
		return c.ReturnError(err)
	}

	fmt.Println(string(out))

	return 0
}

func (c *OrderCommand) Help() string {
	text := `
Usage: ./nefertiti order [options]

The order command places an order with the specified exchange.

Options:
  --exchange = name
  --side     = [buy|sell]
  --type     = [limit|market]
  --market   = a valid market pair
  --size     = amount of cryptocurrency to buy or sell
  --price    = price per unit (optional, not needed for market orders)
  --mult     = vector to multiply price with (optional, defaults to 1.0)
`
	return strings.TrimSpace(text)
}

func (c *OrderCommand) Synopsis() string {
	return "Place an order with the specified exchange."
}
