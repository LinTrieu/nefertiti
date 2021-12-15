//lint:file-ignore ST1006 receiver name should be a reflection of its identity; don't use generic names such as "this" or "self"
package notify

import (
	"encoding/json"
	"time"

	"github.com/gregdel/pushover"
	"github.com/lintrieu/nefertiti/errors"
	"github.com/lintrieu/nefertiti/flag"
	"github.com/lintrieu/nefertiti/model"
	"github.com/lintrieu/nefertiti/passphrase"
)

type Pushover struct {
	appKey  string
	userKey string
}

func (self *Pushover) promptForKeys(interactive bool) (ok bool, err error) {
	self.appKey = flag.Get("pushover-app-key").String()

	if self.appKey == "none" {
		return false, nil
	}

	if self.appKey == "" && interactive {
		var data []byte
		if data, err = passphrase.Read("Pushover application key"); err != nil {
			return false, err
		}
		self.appKey = string(data)
	}

	if self.appKey == "none" {
		return false, nil
	}

	if self.appKey != "" {
		self.userKey = flag.Get("pushover-user-key").String()
		if self.userKey == "" && interactive {
			var data []byte
			if data, err = passphrase.Read("Pushover user key"); err != nil {
				return false, err
			}
			self.userKey = string(data)
		}
	}

	return self.appKey != "" && self.userKey != "", nil
}

func (self *Pushover) PromptForKeys(interactive, verify bool) (ok bool, err error) {
	ok, err = self.promptForKeys(interactive)
	if ok && verify {
		// verify the pushover user key
		if err = pushoverVerifyRecipient(self.appKey, self.userKey); err != nil {
			ok = false
		}
	}
	return
}

func pushoverVerifyRecipient(appToken, userToken string) error {
	if appToken == "" {
		return errors.New("missing argument: Pushover application key")
	}
	app := pushover.New(appToken)
	rec := pushover.NewRecipient(userToken)
	_, err := app.GetRecipientDetails(rec)
	return err
}

var (
	messageHistory map[string]time.Time
)

func init() {
	messageHistory = make(map[string]time.Time)
}

func (self *Pushover) SendMessage(message interface{}, title string, frequency model.Frequency) error {
	if self.appKey == "" {
		return errors.New("missing argument: Pushover application key")
	}
	if self.userKey == "" {
		return errors.New("missing argument: Pushover recipient")
	}

	var (
		err  error
		body string
	)
	if body, err = func(message interface{}) (string, error) {
		if str, ok := message.(string); ok {
			return str, nil
		} else {
			data, err := json.MarshalIndent(message, "", "  ")
			if err != nil {
				return "", err
			} else {
				return string(data), nil
			}
		}
	}(message); err != nil {
		return err
	}

	sent, ok := messageHistory[body]
	if ok {
		if frequency != model.ALWAYS {
			elapsed := time.Since(sent)
			if frequency == model.ONCE_PER_MINUTE && elapsed.Minutes() < 1 {
				return nil
			}
		}
		delete(messageHistory, body)
	}

	app := pushover.New(self.appKey)
	rec := pushover.NewRecipient(self.userKey)
	msg := pushover.NewMessageWithTitle(body, title)

	_, err = app.SendMessage(msg, rec)
	if err != nil {
		return err
	} else {
		messageHistory[body] = time.Now()
	}

	return nil
}

func NewPushover() model.Notify {
	return &Pushover{}
}
