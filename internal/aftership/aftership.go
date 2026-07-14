// Package aftership is the external shipment-tracking client (AfterShip) behind
// dependency.Tracker. It registers shipments so AfterShip monitors them and emits delivery
// webhooks, and polls the normalized delivery status for the delivery-sync worker's reconcile
// path. A disabled no-op impl is returned when no API key is configured.
package aftership

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
)

const (
	defaultBaseURL = "https://api.aftership.com/v4"
	httpTimeout    = 15 * time.Second
	maxRespBody    = 1 << 20 // 1 MiB

	// tagDelivered is AfterShip's normalized "delivered" status tag (also emitted for
	// pickup-point / locker collection). Shared with the webhook handler.
	tagDelivered = "Delivered"

	// AfterShip meta codes we branch on. 4003 = tracking already exists (register is idempotent);
	// 4004 = tracking does not exist (poll should report not-found, not error).
	codeAlreadyExists = 4003
	codeNotExist      = 4004
)

// Config holds AfterShip credentials.
type Config struct {
	APIKey        string `mapstructure:"api_key"`
	WebhookSecret string `mapstructure:"webhook_secret"`
}

// Client is the AfterShip tracking client implementing dependency.Tracker.
type Client struct {
	apiKey  string
	baseURL string
	http    *http.Client
}

// New builds an AfterShip tracker. When the API key is empty it returns a disabled no-op tracker,
// so the rest of the app wires the same interface regardless of configuration (the delivery-sync
// worker then relies solely on the per-carrier timer safety net).
func New(c *Config) dependency.Tracker {
	if c == nil || strings.TrimSpace(c.APIKey) == "" {
		return Disabled{}
	}
	return &Client{
		apiKey:  strings.TrimSpace(c.APIKey),
		baseURL: defaultBaseURL,
		http:    &http.Client{Timeout: httpTimeout},
	}
}

// aftershipEnvelope is the common AfterShip v4 response shape (only the fields we read).
type aftershipEnvelope struct {
	Meta struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Type    string `json:"type"`
	} `json:"meta"`
	Data struct {
		Tracking struct {
			Tag string `json:"tag"`
		} `json:"tracking"`
	} `json:"data"`
}

// RegisterTracking creates a tracking in AfterShip so it starts monitoring the shipment and emits
// delivery webhooks. An already-existing tracking (meta code 4003) is treated as success, so
// registration is idempotent and safe to retry every worker tick.
func (c *Client) RegisterTracking(ctx context.Context, slug, trackingNumber string) error {
	slug, trackingNumber = strings.TrimSpace(slug), strings.TrimSpace(trackingNumber)
	if slug == "" || trackingNumber == "" {
		return fmt.Errorf("aftership: slug and tracking number are required")
	}
	body, err := json.Marshal(map[string]any{
		"tracking": map[string]any{
			"slug":            slug,
			"tracking_number": trackingNumber,
		},
	})
	if err != nil {
		return fmt.Errorf("aftership: marshal register body: %w", err)
	}
	env, statusCode, err := c.do(ctx, http.MethodPost, c.baseURL+"/trackings", body)
	if err != nil {
		return err
	}
	if statusCode == http.StatusCreated || statusCode == http.StatusOK || env.Meta.Code == codeAlreadyExists {
		return nil
	}
	return fmt.Errorf("aftership: register tracking failed: http=%d code=%d msg=%q", statusCode, env.Meta.Code, env.Meta.Message)
}

// GetTrackingStatus polls the normalized status of a tracking. A not-yet-registered tracking
// (HTTP 404 / meta code 4004) returns TrackingStatus{Found: false} without error, so the caller
// can register it and retry on the next tick.
func (c *Client) GetTrackingStatus(ctx context.Context, slug, trackingNumber string) (entity.TrackingStatus, error) {
	slug, trackingNumber = strings.TrimSpace(slug), strings.TrimSpace(trackingNumber)
	if slug == "" || trackingNumber == "" {
		return entity.TrackingStatus{}, fmt.Errorf("aftership: slug and tracking number are required")
	}
	u := fmt.Sprintf("%s/trackings/%s/%s", c.baseURL, url.PathEscape(slug), url.PathEscape(trackingNumber))
	env, statusCode, err := c.do(ctx, http.MethodGet, u, nil)
	if err != nil {
		return entity.TrackingStatus{}, err
	}
	if statusCode == http.StatusNotFound || env.Meta.Code == codeNotExist {
		return entity.TrackingStatus{Found: false}, nil
	}
	if statusCode != http.StatusOK {
		return entity.TrackingStatus{}, fmt.Errorf("aftership: get tracking failed: http=%d code=%d msg=%q", statusCode, env.Meta.Code, env.Meta.Message)
	}
	tag := env.Data.Tracking.Tag
	return entity.TrackingStatus{
		Found:     true,
		Delivered: strings.EqualFold(tag, tagDelivered),
		Tag:       tag,
	}, nil
}

func (c *Client) do(ctx context.Context, method, u string, body []byte) (aftershipEnvelope, int, error) {
	var reqBody io.Reader
	if body != nil {
		reqBody = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, reqBody)
	if err != nil {
		return aftershipEnvelope{}, 0, fmt.Errorf("aftership: build request: %w", err)
	}
	req.Header.Set("as-api-key", c.apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return aftershipEnvelope{}, 0, fmt.Errorf("aftership: request: %w", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxRespBody))
	if err != nil {
		return aftershipEnvelope{}, resp.StatusCode, fmt.Errorf("aftership: read response: %w", err)
	}
	var env aftershipEnvelope
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &env); err != nil {
			return aftershipEnvelope{}, resp.StatusCode, fmt.Errorf("aftership: decode response (http=%d): %w", resp.StatusCode, err)
		}
	}
	return env, resp.StatusCode, nil
}

// Disabled is a no-op Tracker used when AfterShip is not configured. Registration silently
// succeeds and status is always "not found", so the delivery-sync worker falls back to the timer.
type Disabled struct{}

// RegisterTracking is a no-op.
func (Disabled) RegisterTracking(_ context.Context, _, _ string) error { return nil }

// GetTrackingStatus always reports the tracking as not found.
func (Disabled) GetTrackingStatus(_ context.Context, _, _ string) (entity.TrackingStatus, error) {
	return entity.TrackingStatus{Found: false}, nil
}
