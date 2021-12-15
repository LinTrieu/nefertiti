package bitstamp

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/lintrieu/nefertiti/errors"
	"github.com/lintrieu/nefertiti/uuid"
)

const (
	Endpoint   = "https://www.bitstamp.net/api/v2"
	TimeFormat = "2006-01-02 15:04:05"
)

var (
	lastRequest       time.Time
	RequestsPerSecond float64                         = 10
	BeforeRequest     func(method, path string) error = nil
	AfterRequest      func()                          = nil
)

func init() {
	BeforeRequest = func(method, path string) error {
		elapsed := time.Since(lastRequest)
		if elapsed.Seconds() < (float64(1) / RequestsPerSecond) {
			time.Sleep(time.Duration((float64(time.Second) / RequestsPerSecond) - float64(elapsed)))
		}
		return nil
	}
	AfterRequest = func() {
		lastRequest = time.Now()
	}
}

type Client struct {
	URL        string
	Key        string
	Secret     string
	httpClient *http.Client
}

func New(apiKey, apiSecret string) *Client {
	return &Client{
		URL:    Endpoint,
		Key:    apiKey,
		Secret: apiSecret,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

func (client *Client) reason(body []byte) error {
	resp := make(map[string]interface{})
	if json.Unmarshal(body, &resp) == nil {
		if reason1, ok := resp["reason"]; ok {
			if reason2, ok := reason1.(map[string]interface{}); ok {
				if all, ok := reason2["__all__"]; ok {
					msg := fmt.Sprintf("%v", all)
					if msg != "" && msg != "[]" {
						return errors.New(msg)
					}
				}
			}
			return errors.Errorf("%v", reason1)
		}
	}
	return nil
}

func (client *Client) get(path string) ([]byte, error) {
	var err error

	// satisfy the rate limiter (limited to 8000 requests per 10 minutes)
	if err = BeforeRequest("GET", path); err != nil {
		return nil, err
	}
	defer func() {
		AfterRequest()
	}()

	// parse the bitstamp URL
	var endpoint *url.URL
	if endpoint, err = url.Parse(client.URL); err != nil {
		return nil, errors.Wrap(err, 1)
	}

	// set the endpoint for this request
	endpoint.Path += path

	var resp *http.Response
	if resp, err = client.httpClient.Get(endpoint.String()); err != nil {
		return nil, errors.Wrap(err, 1)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, errors.Errorf("GET %s %s", resp.Status, path)
	}

	var body []byte
	if body, err = ioutil.ReadAll(resp.Body); err != nil {
		return nil, errors.Wrap(err, 1)
	}

	// is this an error?
	if err = client.reason(body); err != nil {
		return nil, err
	}

	return body, nil
}

func (client *Client) post(path string, values url.Values) ([]byte, error) {
	var err error

	// satisfy the rate limiter (limited to 8000 requests per 10 minutes)
	if err = BeforeRequest("POST", path); err != nil {
		return nil, err
	}
	defer func() {
		AfterRequest()
	}()

	// parse the bitstamp URL
	var endpoint *url.URL
	if endpoint, err = url.Parse(client.URL); err != nil {
		return nil, errors.Wrap(err, 1)
	}

	// set the endpoint for this request
	endpoint.Path += path

	// encode the url.Values in the body
	payload := values.Encode()
	input := strings.NewReader(payload)

	// create the request
	var req *http.Request
	if req, err = http.NewRequest("POST", endpoint.String(), input); err != nil {
		return nil, errors.Wrap(err, 1)
	}

	// there is no need to set Content-Type if there is no body
	if payload != "" {
		req.Header.Add("Content-Type", "application/x-www-form-urlencoded")
	}

	// compute v2 authentication headers
	x_auth := "BITSTAMP" + " " + client.Key
	x_auth_nonce := strings.ToLower(uuid.New().Long())
	x_auth_timestamp := strconv.FormatInt((time.Now().UnixNano() / 1000000), 10)
	x_auth_version := "v2"

	// there is no need to set Content-Type if there is no body
	content_type := func() string {
		if payload == "" {
			return ""
		}
		return "application/x-www-form-urlencoded"
	}

	// v2 auth message that we will need to sign
	x_auth_message := x_auth +
		req.Method +
		req.Host +
		"/api/v2" + path +
		"" +
		content_type() +
		x_auth_nonce +
		x_auth_timestamp +
		x_auth_version +
		payload

	// compute the v2 signature
	mac := hmac.New(sha256.New, []byte(client.Secret))
	mac.Write([]byte(x_auth_message))
	x_auth_signature := strings.ToUpper(hex.EncodeToString(mac.Sum(nil)))

	// add v2 autentication headers
	req.Header.Add("X-Auth", x_auth)
	req.Header.Add("X-Auth-Nonce", x_auth_nonce)
	req.Header.Add("X-Auth-Timestamp", x_auth_timestamp)
	req.Header.Add("X-Auth-Version", x_auth_version)
	req.Header.Add("X-Auth-Signature", x_auth_signature)

	// submit the http request
	var resp *http.Response
	if resp, err = client.httpClient.Do(req); err != nil {
		return nil, errors.Wrap(err, 1)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, errors.Errorf("POST %s %s", resp.Status, path)
	}

	// read the body of the http message into a byte array
	var body []byte
	if body, err = ioutil.ReadAll(resp.Body); err != nil {
		return nil, errors.Wrap(err, 1)
	}

	// is this an error?
	if err = client.reason(body); err != nil {
		return nil, err
	}

	return body, nil
}

func (client *Client) Ticker(pair string) (*Ticker, error) {
	var (
		err  error
		body []byte
	)
	if body, err = client.get(fmt.Sprintf("/ticker/%s/", pair)); err != nil {
		return nil, err
	}
	var out Ticker
	if err = json.Unmarshal(body, &out); err != nil {
		return nil, errors.Wrap(err, 1)
	}
	return &out, nil
}

func (client *Client) OrderBook(pair string) (*OrderBook, error) {
	var (
		err  error
		body []byte
	)
	if body, err = client.get(fmt.Sprintf("/order_book/%s/", pair)); err != nil {
		return nil, err
	}
	var out OrderBook
	if err = json.Unmarshal(body, &out); err != nil {
		return nil, errors.Wrap(err, 1)
	}
	return &out, nil
}

func (client *Client) BuyMarketOrder(pair string, amount float64) (*Order, error) {
	var err error

	v := url.Values{}
	v.Add("amount", strconv.FormatFloat(amount, 'f', -1, 64))

	var body []byte
	if body, err = client.post(fmt.Sprintf("/buy/market/%s/", pair), v); err != nil {
		return nil, err
	}

	var out Order
	if err = json.Unmarshal(body, &out); err != nil {
		return nil, errors.Wrap(err, 1)
	}

	return &out, nil
}

func (client *Client) BuyLimitOrder(pair string, amount, price float64) (*Order, error) {
	var err error

	v := url.Values{}
	v.Add("amount", strconv.FormatFloat(amount, 'f', -1, 64))
	v.Add("price", strconv.FormatFloat(price, 'f', -1, 64))

	var body []byte
	if body, err = client.post(fmt.Sprintf("/buy/%s/", pair), v); err != nil {
		return nil, err
	}

	var out Order
	if err = json.Unmarshal(body, &out); err != nil {
		return nil, errors.Wrap(err, 1)
	}

	return &out, nil
}

func (client *Client) SellMarketOrder(pair string, amount float64) (*Order, error) {
	var err error

	v := url.Values{}
	v.Add("amount", strconv.FormatFloat(amount, 'f', -1, 64))

	var body []byte
	if body, err = client.post(fmt.Sprintf("/sell/market/%s/", pair), v); err != nil {
		return nil, err
	}

	var out Order
	if err = json.Unmarshal(body, &out); err != nil {
		return nil, errors.Wrap(err, 1)
	}

	return &out, nil
}

func (client *Client) SellLimitOrder(pair string, amount, price float64) (*Order, error) {
	var err error

	v := url.Values{}
	v.Add("amount", strconv.FormatFloat(amount, 'f', -1, 64))
	v.Add("price", strconv.FormatFloat(price, 'f', -1, 64))

	var body []byte
	if body, err = client.post(fmt.Sprintf("/sell/%s/", pair), v); err != nil {
		return nil, err
	}

	var out Order
	if err = json.Unmarshal(body, &out); err != nil {
		return nil, errors.Wrap(err, 1)
	}

	return &out, nil
}

func (client *Client) GetOpenOrders() ([]Order, error) {
	var err error

	var body []byte
	if body, err = client.post("/open_orders/all/", url.Values{}); err != nil {
		return nil, err
	}

	var out []Order
	if err = json.Unmarshal(body, &out); err != nil {
		return nil, errors.Wrap(err, 1)
	}

	return out, nil
}

func (client *Client) GetOpenOrdersEx(pair string) ([]Order, error) {
	var err error

	var body []byte
	if body, err = client.post(fmt.Sprintf("/open_orders/%s/", pair), url.Values{}); err != nil {
		return nil, err
	}

	var out []Order
	if err = json.Unmarshal(body, &out); err != nil {
		return nil, errors.Wrap(err, 1)
	}

	return out, nil
}

func (client *Client) CancelOrder(id string) error {
	v := url.Values{}
	v.Add("id", id)

	if _, err := client.post("/cancel_order/", v); err != nil {
		return err
	}

	return nil
}

func (client *Client) GetUserTransactions(pair string) (Transactions, error) {
	var err error

	var body []byte
	if body, err = client.post(fmt.Sprintf("/user_transactions/%s/", pair), url.Values{}); err != nil {
		return nil, err
	}

	var out []Transaction
	if err = json.Unmarshal(body, &out); err != nil {
		return nil, errors.Wrap(err, 1)
	}

	return out, nil
}

func (client *Client) TradingPairsInfo() ([]Pair, error) {
	var err error

	var body []byte
	if body, err = client.get("/trading-pairs-info/"); err != nil {
		return nil, err
	}

	var out []Pair
	if err = json.Unmarshal(body, &out); err != nil {
		return nil, errors.Wrap(err, 1)
	}

	return out, nil
}
