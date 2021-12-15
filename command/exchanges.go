package command

import (
	"encoding/json"
	"fmt"

	"github.com/lintrieu/nefertiti/exchanges"
)

type (
	ExchangesCommand struct {
		*CommandMeta
	}
)

func (c *ExchangesCommand) Run(args []string) int {
	var err error

	exchanges := exchanges.New()

	var out []byte
	if out, err = json.Marshal(exchanges); err != nil {
		return c.ReturnError(err)
	}

	fmt.Println(string(out))

	return 0
}

func (c *ExchangesCommand) Help() string {
	return "Usage: ./nefertiti exchanges"
}

func (c *ExchangesCommand) Synopsis() string {
	return "Get a list of supported exchanges."
}
