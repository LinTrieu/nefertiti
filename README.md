# Nefertiti

Nefertiti is a command-line crypto trading bot that follows a simple but proven trading strategy; buy the dip and then sell those trades as soon as possible.

### Exchanges

At the time of this writing, the trading bot supports the following crypto exchanges:
* [Bitstamp](https://www.bitstamp.net/ref/QWE1MDzZoyPWZNyU/)
* [Bittrex](https://bittrex.com/Account/Register?referralCode=CIC-YDN-5DX)
* [HitBTC](https://hitbtc.com/?ref_id=5aad6226b7072)
* [CoinbasePro](https://pro.coinbase.com/)
* [Binance](https://www.binance.com/en/register?ref=UME24R7B)
* [KuCoin](https://www.kucoin.com/?rcode=KJ6stw)
* [crypto.com](https://crypto.com/exch/rf3v8ucd4k)

### Setup

You will need [Go](https://golang.org/dl/) installed and `GOPATH` configured.

```bash
mkdir -p $GOPATH/src/github.com/svanas
cd $GOPATH/src/github.com/svanas
git clone git@github.com:svanas/nefertiti.git
```

Verify that you've installed [Go](https://golang.org/dl/) by opening a command prompt and typing the following command:

```bash
go version
```

### Dependencies

Most dependencies are vendored in with this repo. You might need to clone the following repositories:
* go get https://github.com/svanas/go-coinbasepro
* go get https://github.com/svanas/go-crypto-dot-com
* go get https://github.com/svanas/go-mining-hamster

### Running

```
cd $GOPATH/src/github.com/lintrieu/nefertiti
go build
./nefertiti --help
```
