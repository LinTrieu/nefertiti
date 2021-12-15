package command

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/lintrieu/nefertiti/exchanges"
	"github.com/lintrieu/nefertiti/flag"
)

type (
	MarketsCommand struct {
		*CommandMeta
	}
)

func (c *MarketsCommand) Run(args []string) int {
	exchange, err := exchanges.GetExchange()
	if err != nil {
		return c.ReturnError(err)
	}

	markets, err := exchange.GetMarkets(true, flag.Sandbox(), flag.Get("ignore").Split())
	if err != nil {
		return c.ReturnError(err)
	}

	out, err := json.Marshal(markets)
	if err != nil {
		return c.ReturnError(err)
	}

	fmt.Println(string(out))

	return 0
}

func (c *MarketsCommand) Help() string {
	text := `
Usage: ./nefertiti markets [options]

The markets command returns a list of available currency pairs for trading.

Options:
  --exchange=[name]
`
	return strings.TrimSpace(text)
}

func (c *MarketsCommand) Synopsis() string {
	return "Get a list of available currency pairs for trading."
}
