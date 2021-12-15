package command

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/lintrieu/nefertiti/errors"
	"github.com/lintrieu/nefertiti/exchanges"
	"github.com/lintrieu/nefertiti/flag"
	"github.com/lintrieu/nefertiti/model"
)

type (
	BookCommand struct {
		*CommandMeta
	}
)

func (c *BookCommand) Run(args []string) int {
	var (
		err error
		flg *flag.Flag
	)

	var exchange model.Exchange
	if exchange, err = exchanges.GetExchange(); err != nil {
		return c.ReturnError(err)
	}

	var market string
	if market, err = model.GetMarket(exchange); err != nil {
		return c.ReturnError(err)
	}

	var side model.BookSide
	flg = flag.Get("side")
	if flg.String() == "asks" {
		side = model.BOOK_SIDE_ASKS
	} else {
		side = model.BOOK_SIDE_BIDS
	}

	var agg float64
	flg = flag.Get("agg")
	if !flg.Exists {
		return c.ReturnError(errors.New("missing argument: agg"))
	} else {
		if agg, err = flg.Float64(); err != nil {
			return c.ReturnError(errors.Errorf("agg value %v is invalid", flg))
		}
	}

	var client interface{}
	if client, err = exchange.GetClient(model.BOOK, flag.Sandbox()); err != nil {
		return c.ReturnError(err)
	}

	var book1 interface{}
	if book1, err = exchange.GetBook(client, market, side); err != nil {
		return c.ReturnError(err)
	}

	var book2 model.Book
	if book2, err = exchange.Aggregate(client, book1, market, agg); err != nil {
		return c.ReturnError(err)
	}

	// sort the order book by size (highest order size first)
	sort.Slice(book2, func(i1, i2 int) bool {
		return book2[i1].Size > book2[i2].Size
	})

	var out []byte
	if out, err = json.Marshal(book2); err != nil {
		return c.ReturnError(err)
	}

	fmt.Println(string(out))

	return 0
}

func (c *BookCommand) Help() string {
	text := `
Usage: ./nefertiti book [options]

The book command returns a list of all public orders on a market.

Options:
  --exchange = name
  --market   = a valid market pair
  --side     = [bids|asks] (optional, defaults to bids)
  --agg      = aggregate the book to nearest multiple of agg
`
	return strings.TrimSpace(text)
}

func (c *BookCommand) Synopsis() string {
	return "Get a list of all public orders on a market."
}
