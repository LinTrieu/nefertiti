package command

import (
	"fmt"
	"strings"

	"github.com/lintrieu/nefertiti/exchanges"
	"github.com/lintrieu/nefertiti/flag"
	"github.com/lintrieu/nefertiti/model"
)

type (
	QuoteCommand struct {
		*CommandMeta
	}
)

func (c *QuoteCommand) Run(args []string) int {
	exchange, err := exchanges.GetExchange()
	if err != nil {
		return c.ReturnError(err)
	}

	markets, err := exchange.GetMarkets(true, flag.Sandbox(), flag.Get("ignore").Split())
	if err != nil {
		return c.ReturnError(err)
	}

	market, err := model.GetMarket(exchange)
	if err != nil {
		return c.ReturnError(err)
	}

	quote, err := model.GetQuoteCurr(markets, market)
	if err != nil {
		return c.ReturnError(err)
	}

	fmt.Println(strings.ToUpper(quote))

	return 0
}

func (c *QuoteCommand) Help() string {
	text := `
Usage: ./nefertiti quote [options]

The quote command returns the quote symbol for a given market pair.

Options:
  --exchange = name
  --market   = a valid market pair
`
	return strings.TrimSpace(text)
}

func (c *QuoteCommand) Synopsis() string {
	return "Get the quote symbol for a given market pair."
}
