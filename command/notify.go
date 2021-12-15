package command

import (
	"strings"

	"github.com/lintrieu/nefertiti/errors"
	"github.com/lintrieu/nefertiti/flag"
	"github.com/lintrieu/nefertiti/model"
	"github.com/lintrieu/nefertiti/notify"
)

type (
	NotifyCommand struct {
		*CommandMeta
	}
)

func (c *NotifyCommand) Run(args []string) int {
	var err error

	var service model.Notify = nil
	if service, err = notify.New().Init(flag.Interactive(), true); err != nil {
		return c.ReturnError(err)
	}

	if service == nil {
		return c.ReturnError(errors.New("Service not found or not initialized. Quitting."))
	}

	if err = service.SendMessage(flag.Get("msg").String(), flag.Get("title").String(), model.ALWAYS); err != nil {
		return c.ReturnError(err)
	}

	return 0
}

func (c *NotifyCommand) Help() string {
	text := `
Usage: ./nefertiti notify [options]

The notify command sends a notification.

Options:
  --pushover-app-key=X
  --pushover-user-key=Y

or:
  --telegram-app-key=X
  --telegram-chat-id=Y

and:
  --msg="blah blah blah"
`
	return strings.TrimSpace(text)
}

func (c *NotifyCommand) Synopsis() string {
	return "Send a notification."
}
