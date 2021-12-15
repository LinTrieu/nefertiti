package command

import (
	"fmt"
	"log"
	"strings"

	"github.com/lintrieu/nefertiti/exchanges"
	"github.com/lintrieu/nefertiti/flag"
	"github.com/lintrieu/nefertiti/model"
	"github.com/lintrieu/nefertiti/multiplier"
	"github.com/lintrieu/nefertiti/notify"
)

type (
	SellCommand struct {
		*CommandMeta
	}
)

func (c *SellCommand) Run(args []string) int {
	var err error

	var exchange model.Exchange
	if exchange, err = exchanges.GetExchange(); err != nil {
		return c.ReturnError(err)
	}

	var strategy model.Strategy = model.STRATEGY_STANDARD
	if strategy, err = model.GetStrategy(); err != nil {
		return c.ReturnError(err)
	}

	if _, err = multiplier.Get(multiplier.FIVE_PERCENT); err != nil {
		return c.ReturnError(err)
	}

	var all []model.Market
	if all, err = exchange.GetMarkets(true, flag.Sandbox(), flag.Get("ignore").Split()); err != nil {
		return c.ReturnError(err)
	}

	hold := flag.Get("hold").Split()
	if len(hold) > 0 && hold[0] != "" {
		for _, market := range hold {
			if market != "" && !model.HasMarket(all, market) {
				return c.ReturnError(fmt.Errorf("market %s does not exist", market))
			}
		}
	}

	earn := flag.Get("earn").Split()
	if len(earn) > 0 && earn[0] != "" {
		for _, market := range earn {
			if market != "" && !model.HasMarket(all, market) {
				return c.ReturnError(fmt.Errorf("market %s does not exist", market))
			}
		}
	}

	var level int64 = notify.LEVEL_DEFAULT
	if level, err = notify.Level(); err != nil {
		return c.ReturnError(err)
	}

	success := func(service model.Notify) error {
		err := c.ReturnSuccess()
		if err != nil {
			return err
		}
		msg := fmt.Sprintf("Listening to %s...", exchange.GetInfo().Name)
		log.Println("[INFO] " + msg)
		if service != nil {
			if notify.CanSend(level, notify.INFO) {
				err := service.SendMessage(msg, fmt.Sprintf("%s - INFO", exchange.GetInfo().Name), model.ALWAYS)
				if err != nil {
					return err
				}
			}
		}
		return nil
	}

	if err = exchange.Sell(strategy, hold, earn, flag.Sandbox(), flag.Exists("tweet"), flag.Debug(), success); err != nil {
		return c.ReturnError(err)
	}

	return 0
}

func (c *SellCommand) Help() string {
	text := `
Usage: ./nefertiti sell [options]

The sell command listens for buy orders getting filled, and then opens new sell orders for them.

Options:
  --exchange = [name]
  --sandbox  = [Y|N] (optional)
  --stoploss = [Y|N] (optional)
  --notify   = [0|1|2|3] (see below)
  --mult     = multiplier, for example: 1.05 (aka 5 percent, optional)
  --hold     = name of the market not to sell, for example: BTC-EUR (optional)
  --earn     = name of the market where you want to sell only enough of the
               base asset at "mult" to break even; hold the rest (optional)

Notify:
  0 = nothing, ever
  1 = errors only
  2 = errors + filled orders (default)
  3 = everything (including opened and cancelled orders)
`
	return strings.TrimSpace(text)
}

func (c *SellCommand) Synopsis() string {
	return "Automatically opens new sell orders on the specified exchange/market."
}
