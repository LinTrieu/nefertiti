package exchanges

import (
	"github.com/lintrieu/nefertiti/errors"
	"github.com/lintrieu/nefertiti/flag"
	"github.com/lintrieu/nefertiti/model"
	"github.com/lintrieu/nefertiti/passphrase"
)

type Exchanges []model.Exchange

func (exchanges *Exchanges) findByName(name string) model.Exchange {
	for _, exchange := range *exchanges {
		if exchange.GetInfo().Equals(name) {
			return exchange
		}
	}
	return nil
}

func New() *Exchanges {
	var out Exchanges
	out = append(out, newGdax())
	out = append(out, newBittrex())
	out = append(out, newBitstamp())
	out = append(out, newCexIo())
	out = append(out, newBinance())
	out = append(out, newBinanceUS())
	out = append(out, newHitBTC())
	out = append(out, newKucoin())
	out = append(out, newCryptoDotCom())
	out = append(out, newWoo())
	return &out
}

func GetExchange() (model.Exchange, error) {
	arg := flag.Get("exchange")
	if !arg.Exists {
		return nil, errors.New("missing argument: exchange")
	}
	out := New().findByName(arg.String())
	if out == nil {
		return nil, errors.Errorf("exchange %v does not exist", arg)
	}
	return out, nil
}

func promptForApiKeys(exchange string) (apiKey, apiSecret string, err error) {
	apiKey = flag.Get("api-key").String()
	if apiKey == "" {
		if flag.Listen() {
			return "", "", errors.New("missing argument: api-key")
		}
		var data []byte
		if data, err = passphrase.Read(exchange + " API key"); err != nil {
			return "", "", errors.Wrap(err, 1)
		}
		apiKey = string(data)
		flag.Set("api-key", apiKey)
	}

	apiSecret = flag.Get("api-secret").String()
	if apiSecret == "" {
		if flag.Listen() {
			return "", "", errors.New("missing argument: api-secret")
		}
		var data []byte
		if data, err = passphrase.Read(exchange + " API secret"); err != nil {
			return "", "", errors.Wrap(err, 1)
		}
		apiSecret = string(data)
		flag.Set("api-secret", apiSecret)
	}

	return apiKey, apiSecret, nil
}

func promptForApiKeysEx(exchange string) (apiKey, apiSecret, apiPassphrase string, err error) {
	apiKey, apiSecret, err = promptForApiKeys(exchange)

	if err != nil {
		return apiKey, apiSecret, "", err
	}

	apiPassphrase = flag.Get("api-passphrase").String()
	if apiPassphrase == "" {
		if flag.Listen() {
			return "", "", "", errors.New("missing argument: api-passphrase")
		}
		var data []byte
		if data, err = passphrase.Read(exchange + " API passphrase"); err != nil {
			return "", "", "", errors.Wrap(err, 1)
		}
		apiPassphrase = string(data)
	}

	return apiKey, apiSecret, apiPassphrase, nil
}
