package command

import (
	"fmt"
	"strings"

	"github.com/lintrieu/nefertiti/errors"
	"github.com/lintrieu/nefertiti/exchanges"
	"github.com/lintrieu/nefertiti/flag"
	"github.com/lintrieu/nefertiti/model"
)

type (
	StopLossCommand struct {
		*CommandMeta
	}
)

func (c *StopLossCommand) Run(args []string) int {
	var (
		err error
		flg *flag.Flag
	)

	var exchange model.Exchange
	if exchange, err = exchanges.GetExchange(); err != nil {
		return c.ReturnError(err)
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

	var out []byte
	if out, err = exchange.StopLoss(
		client,
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

func (c *StopLossCommand) Help() string {
	text := `
Usage: ./nefertiti stoploss [options]

The stoploss command places a stop-loss order with the specified exchange.

Options:
  --exchange = name
  --type     = [limit|market]
  --market   = a valid market pair
  --size     = amount of cryptocurrency to sell
  --price    = price per unit
`
	return strings.TrimSpace(text)
}

func (c *StopLossCommand) Synopsis() string {
	return "Place a stop-loss order with the specified exchange."
}
