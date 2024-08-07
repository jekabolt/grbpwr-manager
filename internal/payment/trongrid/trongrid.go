package trongrid

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/dto"
)

// Define constants for mainnet and testnets.
const (
	Mainnet       = "https://api.trongrid.io"
	ShastaTestnet = "https://api.shasta.trongrid.io"
	NileTestnet   = "https://nile.trongrid.io"

	TypeTransfer = "Transfer"
)

// APIKeyHeader represents the header name for the API key.
const APIKeyHeader = "TRON-PRO-API-KEY"

type Config struct {
	APIKey  string        `mapstructure:"api_key"`
	BaseURL string        `mapstructure:"base_url"`
	Timeout time.Duration `mapstructure:"timeout"`
}

// Client represents a client for the TronGrid API.
type Client struct {
	httpClient *http.Client
	c          *Config
}

// NewClient creates a new TronGrid API client.
func New(c *Config) dependency.Trongrid {
	return &Client{
		httpClient: &http.Client{Timeout: c.Timeout},
		c:          c,
	}
}

// GetAddressTransactions retrieves TRC-20 token transactions for a given address.
func (c *Client) GetAddressTransactions(address string) (*dto.TronTransactionsResponse, error) {
	url := fmt.Sprintf("%s/v1/accounts/%s/transactions/trc20?limit=1", c.c.BaseURL, address)

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}

	req.Header.Add(APIKeyHeader, c.c.APIKey)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("sending request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("non-200 status code: %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading response body: %w", err)
	}

	var transactionsResponse dto.TronTransactionsResponse
	if err := json.Unmarshal(body, &transactionsResponse); err != nil {
		return nil, fmt.Errorf("unmarshaling response: %w", err)
	}

	return &transactionsResponse, nil
}
