package notify

import (
	"github.com/dghubble/go-twitter/twitter"
	"github.com/dghubble/oauth1"
	"github.com/lintrieu/nefertiti/errors"
	"github.com/lintrieu/nefertiti/flag"
	"github.com/lintrieu/nefertiti/passphrase"
)

type TwitterKeys struct {
	ConsumerKey    string
	ConsumerSecret string
	AccessToken    string
	AccessSecret   string
}

func TwitterPromptForKeys(interactive bool) (keys *TwitterKeys, err error) {
	var (
		consumerKey    string
		consumerSecret string
		accessToken    string
		accessSecret   string
	)

	consumerKey = flag.Get("twitter-consumer-key").String()
	if consumerKey == "" {
		if interactive {
			var data []byte
			if data, err = passphrase.Read("Twitter consumer key"); err != nil {
				return nil, errors.Wrap(err, 1)
			}
			consumerKey = string(data)
		}
		if consumerKey == "" {
			return nil, errors.New("missing argument: twitter-consumer-key")
		}
	}

	consumerSecret = flag.Get("twitter-consumer-secret").String()
	if consumerSecret == "" {
		if interactive {
			var data []byte
			if data, err = passphrase.Read("Twitter consumer secret"); err != nil {
				return nil, errors.Wrap(err, 1)
			}
			consumerSecret = string(data)
		}
		if consumerSecret == "" {
			return nil, errors.New("missing argument: twitter-consumer-secret")
		}
	}

	accessToken = flag.Get("twitter-access-token").String()
	if accessToken == "" {
		if interactive {
			var data []byte
			if data, err = passphrase.Read("Twitter access token"); err != nil {
				return nil, errors.Wrap(err, 1)
			}
			accessToken = string(data)
		}
		if accessToken == "" {
			return nil, errors.New("missing argument: twitter-access-token")
		}
	}

	accessSecret = flag.Get("twitter-access-secret").String()
	if accessSecret == "" {
		if interactive {
			var data []byte
			if data, err = passphrase.Read("Twitter access secret"); err != nil {
				return nil, errors.Wrap(err, 1)
			}
			accessSecret = string(data)
		}
		if accessSecret == "" {
			return nil, errors.New("missing argument: twitter-access-secret")
		}
	}

	return &TwitterKeys{
		ConsumerKey:    consumerKey,
		ConsumerSecret: consumerSecret,
		AccessToken:    accessToken,
		AccessSecret:   accessSecret,
	}, nil
}

func Tweet(keys *TwitterKeys, status string) {
	config := oauth1.NewConfig(keys.ConsumerKey, keys.ConsumerSecret)
	if config != nil {
		token := oauth1.NewToken(keys.AccessToken, keys.AccessSecret)
		if token != nil {
			httpClient := config.Client(oauth1.NoContext, token)
			if httpClient != nil {
				client := twitter.NewClient(httpClient)
				if client != nil {
					client.Statuses.Update(status, nil)
				}
			}
		}
	}
}
