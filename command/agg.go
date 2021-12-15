package command

import (
	"fmt"
	"strings"

	"github.com/lintrieu/nefertiti/aggregation"
	"github.com/lintrieu/nefertiti/exchanges"
	"github.com/lintrieu/nefertiti/flag"
	"github.com/lintrieu/nefertiti/model"
)

type (
	AggCommand struct {
		*CommandMeta
	}
)

func (c *AggCommand) Run(args []string) int {
	exchange, err := exchanges.GetExchange()
	if err != nil {
		return c.ReturnError(err)
	}

	market, err := model.GetMarket(exchange)
	if err != nil {
		return c.ReturnError(err)
	}

	dip, err := flag.Dip()
	if err != nil {
		return c.ReturnError(err)
	}

	pip, err := flag.Pip()
	if err != nil {
		return c.ReturnError(err)
	}

	max, err := flag.Max()
	if err != nil {
		return c.ReturnError(err)
	}

	min, err := flag.Min()
	if err != nil {
		return c.ReturnError(err)
	}

	agg, _, _, err := aggregation.Get(exchange, market, dip, pip, max, min, 2, flag.Strict(), flag.Sandbox())
	if err != nil {
		return c.ReturnError(err)
	}

	fmt.Println(agg)

	return 0
}

func (c *AggCommand) Help() string {
	text := `
Usage: ./nefertiti agg [options]

The agg command calculates the aggregation level for a given market pair.

Options:
  --exchange = name
  --market   = a valid market pair
  --dip      = percentage that will kick the bot into action.
               (optional, defaults to 5%)
  --pip      = range in where the market is suspected to move up and down.
               the bot will ignore supports outside of this range.
               (optional, defaults to 30%)
  --max      = maximum price that you will want to pay for the coins.
               (optional)
  --min      = minimum price that you will want to pay for the coins.
               (optional)
`
	return strings.TrimSpace(text)
}

func (c *AggCommand) Synopsis() string {
	return "Calculates the aggregation level for a given market pair."
}
