//lint:file-ignore ST1006 receiver name should be a reflection of its identity; don't use generic names such as "this" or "self"
package notify

import (
	"encoding/json"
	"strconv"

	"github.com/lintrieu/nefertiti/errors"
	"github.com/lintrieu/nefertiti/flag"
	"github.com/lintrieu/nefertiti/model"
	"github.com/lintrieu/nefertiti/passphrase"
	"github.com/yanzay/tbot"
)

type Telegram struct {
	appKey string
	chatId int64
}

func (self *Telegram) PromptForKeys(interactive, verify bool) (ok bool, err error) {
	self.appKey = flag.Get("telegram-app-key").String()

	if self.appKey == "none" {
		return false, nil
	}

	if self.appKey == "" && interactive {
		var data []byte
		if data, err = passphrase.Read("Telegram application key"); err != nil {
			return false, err
		}
		self.appKey = string(data)
	}

	if self.appKey == "none" {
		return false, nil
	}

	if self.appKey != "" {
		chatId := flag.Get("telegram-chat-id").String()
		if chatId != "" {
			self.chatId, err = strconv.ParseInt(chatId, 10, 64)
		} else {
			if interactive {
				var data []byte
				if data, err = passphrase.Read("Telegram chat ID"); err != nil {
					return false, err
				}
				self.chatId, err = strconv.ParseInt(string(data), 10, 64)
			}
		}
		if err != nil {
			return false, errors.New("error when getting Telegram chat ID")
		}
	}

	return self.appKey != "" && self.chatId != 0, nil
}

func (self *Telegram) SendMessage(message interface{}, title string, frequency model.Frequency) error {
	if self.appKey == "" {
		return errors.New("missing argument: Telegram application key")
	}
	if self.chatId == 0 {
		return errors.New("missing argument: Telegram chatId")
	}

	var (
		err  error
		body string
	)
	if body, err = func(message interface{}) (string, error) {
		if str, ok := message.(string); ok {
			return str, nil
		} else {
			data, err := json.Marshal(message)
			if err != nil {
				return "", err
			} else {
				return string(data), nil
			}
		}
	}(message); err != nil {
		return err
	}

	bot, err := tbot.NewServer(self.appKey)
	if err != nil {
		return err
	}

	if title == "" {
		return bot.Send(self.chatId, body)
	} else {
		return bot.Send(self.chatId, (title + ": " + body))
	}
}

func NewTelegram() model.Notify {
	return &Telegram{}
}
