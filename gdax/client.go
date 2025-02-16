//lint:file-ignore ST1006 receiver name should be a reflection of its identity; don't use generic names such as "this" or "self"
package gdax

import (
	exchange "github.com/svanas/go-coinbasepro"
	"net/http"
	"time"
)

const (
	BASE_URL         = "https://api.pro.coinbase.com"
	BASE_URL_SANDBOX = "https://api-public.sandbox.pro.coinbase.com"
)

type Client struct {
	*exchange.Client
}

func (self *Client) CreateOrder(neworder *Order) (*Order, error) {
	var (
		err       error
		unwrapped exchange.Order
		wrapped   *Order
	)
	if unwrapped, err = self.Client.CreateOrder(neworder.Order); err != nil {
		return nil, err
	}
	if wrapped, err = wrap(&unwrapped); err != nil {
		return nil, err
	}
	return wrapped, nil
}

func (self *Client) GetOrder(id string) (*Order, error) {
	var (
		err       error
		unwrapped exchange.Order
		wrapped   *Order
	)
	if unwrapped, err = self.Client.GetOrder(id); err != nil {
		return nil, err
	}
	if wrapped, err = wrap(&unwrapped); err != nil {
		return nil, err
	}
	return wrapped, nil
}

func New(sandbox bool) *Client {
	client := exchange.NewClient()

	client.HTTPClient = &http.Client{
		Timeout: 30 * time.Second,
	}

	if sandbox {
		client.UpdateConfig(&exchange.ClientConfig{
			BaseURL: BASE_URL_SANDBOX,
		})
	} else {
		client.UpdateConfig(&exchange.ClientConfig{
			BaseURL: BASE_URL,
		})
	}

	return &Client{Client: client}
}
