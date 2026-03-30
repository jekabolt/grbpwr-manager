package ga4mp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
)

const (
	collectEndpoint = "https://www.google-analytics.com/mp/collect"
	requestTimeout  = 5 * time.Second
)

type Config struct {
	MeasurementID string `mapstructure:"measurement_id"` // G-XXXXXXX
	APISecret     string `mapstructure:"api_secret"`
}

type Client struct {
	cfg        *Config
	httpClient *http.Client
}

func New(cfg *Config) *Client {

	return &Client{
		cfg: cfg,
		httpClient: &http.Client{
			Timeout: requestTimeout,
		},
	}
}

// TrackPurchase sends a server-side purchase event to GA4 via the Measurement Protocol.
// Uses order.Order.GAClientID (from browser _ga cookie) when available so the event
// stitches into the same GA4 session/funnel. Falls back to a deterministic UUID from buyer email.
// Non-blocking: errors are logged but never propagated to callers.
func (c *Client) TrackPurchase(ctx context.Context, order entity.OrderFull) {
	go func() {
		if err := c.sendPurchaseEvent(ctx, order); err != nil {
			slog.Default().ErrorContext(ctx, "ga4mp: failed to send purchase event",
				slog.String("orderUUID", order.Order.UUID),
				slog.String("err", err.Error()),
				slog.String("clientID", order.Order.GAClientID.String),
			)
		}
		slog.Default().InfoContext(ctx, "ga4mp: purchase event sent",
			slog.String("orderUUID", order.Order.UUID),
			slog.String("clientID", order.Order.GAClientID.String),
		)
	}()
}

func (c *Client) sendPurchaseEvent(ctx context.Context, order entity.OrderFull) error {
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()

	clientID := order.Order.GAClientID.String
	if !order.Order.GAClientID.Valid || clientID == "" {
		clientID = deterministicClientID(order.Buyer.Email)
	}
	val, _ := order.Order.TotalPrice.Float64()

	items := make([]mpItem, 0, len(order.OrderItems))
	for _, oi := range order.OrderItems {
		price, _ := oi.ProductPriceWithSale.Float64()
		qty, _ := oi.Quantity.Float64()
		items = append(items, mpItem{
			ItemID:   oi.SKU,
			ItemName: oi.ProductBrand + " " + oi.SKU,
			Price:    price,
			Quantity: int(qty),
		})
	}

	payload := mpPayload{
		ClientID: clientID,
		Events: []mpEvent{{
			Name: "purchase",
			Params: mpPurchaseParams{
				TransactionID: order.Order.UUID,
				Value:         val,
				Currency:      order.Order.Currency,
				Items:         items,
				ServerSide:    true,
			},
		}},
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal payload: %w", err)
	}

	url := fmt.Sprintf("%s?measurement_id=%s&api_secret=%s",
		collectEndpoint, c.cfg.MeasurementID, c.cfg.APISecret)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("send request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("unexpected status: %d", resp.StatusCode)
	}

	slog.Default().InfoContext(ctx, "ga4mp: purchase event sent",
		slog.String("orderUUID", order.Order.UUID),
		slog.String("clientID", clientID),
	)
	return nil
}

func deterministicClientID(email string) string {
	return uuid.NewSHA1(uuid.NameSpaceDNS, []byte(email)).String()
}

type mpPayload struct {
	ClientID string    `json:"client_id"`
	Events   []mpEvent `json:"events"`
}

type mpEvent struct {
	Name   string           `json:"name"`
	Params mpPurchaseParams `json:"params"`
}

type mpPurchaseParams struct {
	TransactionID string   `json:"transaction_id"`
	Value         float64  `json:"value"`
	Currency      string   `json:"currency"`
	Items         []mpItem `json:"items"`
	ServerSide    bool     `json:"server_side"`
}

type mpItem struct {
	ItemID   string  `json:"item_id"`
	ItemName string  `json:"item_name"`
	Price    float64 `json:"price"`
	Quantity int     `json:"quantity"`
}
