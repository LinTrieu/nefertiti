package notify

import (
	"github.com/lintrieu/nefertiti/model"
)

type Services []model.Notify

func (services *Services) Init(interactive, verify bool) (model.Notify, error) {
	for _, service := range *services {
		ok, err := service.PromptForKeys(false, verify)
		if ok {
			return service, err
		}
	}
	if interactive {
		for _, service := range *services {
			ok, err := service.PromptForKeys(true, verify)
			if ok {
				return service, err
			}
		}
	}
	return nil, nil
}

func New() *Services {
	var out Services
	out = append(out, NewPushover())
	out = append(out, NewTelegram())
	return &out
}
