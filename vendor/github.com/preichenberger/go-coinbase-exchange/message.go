package coinbase

type Message struct {
	Type          string  `json:"type"`
	ProductId     string  `json:"product_id"`
	TradeId       int     `json:"trade_id,number"`
	OrderId       string  `json:"order_id"`
	Sequence      int64   `json:"sequence,number"`
	MakerOrderId  string  `json:"maker_order_id"`
	TakerOrderId  string  `json:"taker_order_id"`
	Time          Time    `json:"time,string"`
	RemainingSize string  `json:"remaining_size"`
	NewSize       float64 `json:"new_size,string"`
	OldSize       float64 `json:"old_size,string"`
	Size          float64 `json:"size,string"`
	Price         float64 `json:"price,string"`
	Side          string  `json:"side"`
	Reason        string  `json:"reason"`
	OrderType     string  `json:"order_type"`
	Funds         string  `json:"funds"`
	NewFunds      float64 `json:"new_funds,string"`
	OldFunds      float64 `json:"old_funds,string"`
	Message       string  `json:"message"`
}
