package dto

// TransactionsResponse defines the structure for transactions response.
type TronTransactionsResponse struct {
	Data    []TransactionData `json:"data"`
	Success bool              `json:"success"`
	Meta    MetaData          `json:"meta"`
}

// TransactionData defines the structure for transaction data.
type TransactionData struct {
	TransactionID  string    `json:"transaction_id"`
	TokenInfo      TokenInfo `json:"token_info"`
	BlockTimestamp int64     `json:"block_timestamp"`
	From           string    `json:"from"`
	To             string    `json:"to"`
	Type           string    `json:"type"`
	Value          string    `json:"value"`
}

// TokenInfo defines the structure for token information.
type TokenInfo struct {
	Symbol   string `json:"symbol"`
	Address  string `json:"address"`
	Decimals int    `json:"decimals"`
	Name     string `json:"name"`
}

// MetaData defines the structure for metadata.
type MetaData struct {
	At          int64  `json:"at"`
	Fingerprint string `json:"fingerprint"`
	Links       Links  `json:"links"`
	PageSize    int    `json:"page_size"`
}

// Links defines the structure for pagination links.
type Links struct {
	Next string `json:"next"`
}
