package ga4mp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
)

const (
	collectEndpoint = "https://www.google-analytics.com/mp/collect"
	requestTimeout  = 5 * time.Second
)

type Config struct {
	Enabled       bool   `mapstructure:"enabled"`
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
	// Honor the GA4MP_ENABLED switch and never POST with blank credentials (e.g. on
	// beta where analytics is off). GA4's /mp/collect returns 2xx even for garbage, so
	// a misconfigured send would otherwise silently look successful.
	if c.cfg == nil || !c.cfg.Enabled || c.cfg.MeasurementID == "" || c.cfg.APISecret == "" {
		return
	}
	// Detach from the caller's context. The payment monitor cancels its context via
	// defer cancel() the moment it returns, which would abort this in-flight send and
	// silently drop the purchase event. We keep the context values (trace/log IDs) but
	// drop cancellation; sendPurchaseEvent applies its own requestTimeout.
	ctx = context.WithoutCancel(ctx)
	go func() {
		// A panic in this best-effort analytics goroutine must never take down the
		// payment-processing process.
		defer func() {
			if r := recover(); r != nil {
				slog.Default().ErrorContext(ctx, "ga4mp: panic while tracking purchase",
					slog.String("orderUUID", order.Order.UUID),
					slog.Any("panic", r),
				)
			}
		}()
		if err := c.sendPurchaseEvent(ctx, order); err != nil {
			slog.Default().ErrorContext(ctx, "ga4mp: failed to send purchase event",
				slog.String("orderUUID", order.Order.UUID),
				slog.String("err", err.Error()),
				slog.String("clientID", order.Order.GAClientID.String),
			)
		}
		// success is logged inside sendPurchaseEvent
	}()
}

func (c *Client) sendPurchaseEvent(ctx context.Context, order entity.OrderFull) error {
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()

	clientID := order.Order.GAClientID.String
	if !order.Order.GAClientID.Valid || clientID == "" {
		// No GA cookie on the order (e.g. admin/custom order): fall back to the order
		// UUID. Do NOT derive the client_id from the buyer's email — a hash of an email
		// is still a PII-derived identifier and must not be sent to GA without consent.
		clientID = order.Order.UUID
	}
	val, _ := order.Order.TotalPrice.Float64()

	items := buildPurchaseItems(order.OrderItems)

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
	// Drain before close so the keep-alive connection can be reused.
	defer func() { _, _ = io.Copy(io.Discard, resp.Body); resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("unexpected status: %d", resp.StatusCode)
	}

	slog.Default().InfoContext(ctx, "ga4mp: purchase event sent",
		slog.String("orderUUID", order.Order.UUID),
		slog.String("clientID", clientID),
	)
	return nil
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
	ItemID      string  `json:"item_id"`
	ItemName    string  `json:"item_name"`
	ItemGroupID string  `json:"item_group_id,omitempty"`
	ItemVariant string  `json:"item_variant,omitempty"`
	Price       float64 `json:"price"`
	Quantity    int     `json:"quantity"`
}

// buildPurchaseItems converts order lines into GA4 Measurement Protocol items per the R3 item-identity
// contract (problem 020): item_id is the variant SKU (order fetch resolves oi.SKU to the frozen
// order_item snapshot), item_group_id is the base SKU and item_variant is the public size ordinal,
// both derived strictly from that same snapshot string (splitVariantSKU) — never from a live
// product/size lookup. item_name stays a human label (brand + product name), not the SKU. Pulled out
// as a pure function so the event contract is unit-testable without an HTTP round trip.
func buildPurchaseItems(orderItems []entity.OrderItem) []mpItem {
	items := make([]mpItem, 0, len(orderItems))
	for _, oi := range orderItems {
		price, _ := oi.ProductPriceWithSale.Float64()
		qty, _ := oi.Quantity.Float64()
		name := oi.ProductBrand
		if len(oi.Translations) > 0 && oi.Translations[0].Name != "" {
			name = oi.ProductBrand + " " + oi.Translations[0].Name
		}
		item := mpItem{
			ItemID:   oi.SKU,
			ItemName: name,
			Price:    price,
			Quantity: int(qty),
		}
		if base, sizeCode, ok := splitVariantSKU(oi.SKU); ok {
			item.ItemGroupID = base
			item.ItemVariant = sizeCode
		}
		items = append(items, item)
	}
	return items
}
