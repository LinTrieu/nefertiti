package command

import (
	"strings"

	"github.com/lintrieu/nefertiti/errors"
	"github.com/lintrieu/nefertiti/exchanges"
	"github.com/lintrieu/nefertiti/flag"
	"github.com/lintrieu/nefertiti/model"
)

type (
	CancelCommand struct {
		*CommandMeta
	}
)

func (c *CancelCommand) Run(args []string) int {
	exchange, err := exchanges.GetExchange()
	if err != nil {
		return c.ReturnError(err)
	}

	market, err := model.GetMarket(exchange)
	if err != nil {
		return c.ReturnError(err)
	}

	arg := flag.Get("side")
	if !arg.Exists {
		return c.ReturnError(errors.New("missing argument: side"))
	}
	side := model.NewOrderSide(arg.String())
	if side == model.ORDER_SIDE_NONE {
		return c.ReturnError(errors.Errorf("side %v is invalid", arg))
	}

	client, err := exchange.GetClient(model.PRIVATE, flag.Sandbox())
	if err != nil {
		return c.ReturnError(err)
	}

	if market != "all" {
		if err = exchange.Cancel(client, market, side); err != nil {
			return c.ReturnError(err)
		}
		return 0
	}

	markets, err := exchange.GetMarkets(true, flag.Sandbox(), flag.Get("ignore").Split())
	if err != nil {
		return c.ReturnError(err)
	}

	for _, market := range markets {
		if err = exchange.Cancel(client, market.Name, side); err != nil {
			return c.ReturnError(err)
		}
	}

	return 0
}

func (c *CancelCommand) Help() string {
	text := `
Usage: ./nefertiti cancel [options]

The cancel command cancels all your buy or sell orders on a given market.

Options:
  --exchange = name
  --market   = a valid market pair
  --side     = [buy|sell]
`
	return strings.TrimSpace(text)
}

func (c *CancelCommand) Synopsis() string {
	return "Cancels all your buy or sell orders on a given market."
}
