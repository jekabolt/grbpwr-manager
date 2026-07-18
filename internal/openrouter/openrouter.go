// Package openrouter is a small client for the OpenRouter chat/completions API
// (https://openrouter.ai). It drafts structured garment sewing operations from a
// plain-language description, grounded in a tech card's pieces + BOM + type, for a
// technologist to review, edit and save.
//
// The client is optional and degrades gracefully: when no API key is configured
// Enabled() is false and GenerateOperations returns ErrNotConfigured, so the admin
// service keeps working with the feature simply unavailable.
package openrouter

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	// defaultModel is the OpenRouter model slug used when none is configured.
	defaultModel = "anthropic/claude-3.5-sonnet"
	// defaultBaseURL is the OpenRouter API root (OpenAI-compatible).
	defaultBaseURL = "https://openrouter.ai/api/v1"
	// defaultTimeout bounds a single generation call (LLM latency can be seconds).
	defaultTimeout = 60 * time.Second
	// maxResponseBytes caps how much of an API response we read (defensive).
	maxResponseBytes = 4 << 20 // 4 MiB
	// maxOperations caps how many drafted operations we return (runaway guard).
	maxOperations = 200
	// generationTemperature keeps drafts fairly deterministic/consistent.
	generationTemperature = 0.2
)

// ErrNotConfigured is returned when GenerateOperations is called with no API key.
// Callers should surface it as a clear "not configured" precondition failure.
var ErrNotConfigured = errors.New("openrouter: OPENROUTER_API_KEY is not set")

// Config is the OpenRouter client configuration. Bound in config/cfg.go; every
// field is optional except APIKey (without which the client is disabled).
type Config struct {
	APIKey      string        `mapstructure:"api_key"`      // OPENROUTER_API_KEY; empty = disabled
	Model       string        `mapstructure:"model"`        // OPENROUTER_MODEL; empty = defaultModel
	BaseURL     string        `mapstructure:"base_url"`     // OPENROUTER_BASE_URL; empty = defaultBaseURL
	HTTPTimeout time.Duration `mapstructure:"http_timeout"` // OPENROUTER_HTTP_TIMEOUT; <=0 = defaultTimeout
}

// Client is a configured OpenRouter chat client. A nil *Client is a valid,
// permanently-disabled client (Enabled() == false), so callers need not nil-check.
type Client struct {
	cfg  Config
	http *http.Client
}

// New builds a client, applying defaults for model / base URL / timeout. It does
// not validate the API key (an unset key just leaves the client disabled).
func New(cfg Config) *Client {
	if strings.TrimSpace(cfg.Model) == "" {
		cfg.Model = defaultModel
	}
	if strings.TrimSpace(cfg.BaseURL) == "" {
		cfg.BaseURL = defaultBaseURL
	}
	timeout := cfg.HTTPTimeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	return &Client{cfg: cfg, http: &http.Client{Timeout: timeout}}
}

// Enabled reports whether an API key is configured. Nil-safe.
func (c *Client) Enabled() bool {
	return c != nil && strings.TrimSpace(c.cfg.APIKey) != ""
}

// Model returns the effective model id (for response provenance). Nil-safe.
func (c *Client) Model() string {
	if c == nil {
		return ""
	}
	return c.cfg.Model
}

// TechCardContext is the tech-card knowledge fed to the model as grounding: the
// style header plus its cut-pieces and BOM. The caller builds it from the store.
type TechCardContext struct {
	TechCardID   int
	StyleName    string
	StyleNumber  string
	Category     string // resolved garment type / category name
	Gender       string
	Brand        string
	Notes        string
	Concept      string
	Pieces       []PieceContext
	BOM          []BOMItemContext
	Construction *ConstructionContext
}

// PieceContext is one structural cut-piece of the garment.
type PieceContext struct {
	Name             string
	PiecesPerGarment int
	Mirrored         bool
	Grainline        string
	Fused            bool
	Note             string
}

// BOMItemContext is one bill-of-materials line (fabric / thread / trim / …).
type BOMItemContext struct {
	Section     string
	Name        string
	Composition string
	Color       string
	Spec        string
	Supplier    string
}

// ConstructionContext is the card's default construction description, if any.
type ConstructionContext struct {
	MainStitchType  string
	StitchDensity   string
	OverlockThreads string
	SeamAllowances  string
}

// Operation is one drafted sewing operation as returned by the model. Numeric-ish
// fields are captured as jsonNum (tolerating both JSON numbers and strings); the
// caller parses/validates them when mapping to the persisted operation shape.
type Operation struct {
	OperationNumber jsonNum `json:"operation_number"`
	Node            string  `json:"node"`
	Description     string  `json:"description"`
	SeamType        string  `json:"seam_type"`
	OperationType   string  `json:"operation_type"`
	Machine         string  `json:"machine"`
	StitchesPerCm   jsonNum `json:"stitches_per_cm"`
	TopstitchWidth  string  `json:"topstitch_width"`
	SeamAllowance   string  `json:"seam_allowance"`
	Thread          string  `json:"thread"`
	Needle          string  `json:"needle"`
	Attachment      string  `json:"attachment"`
	TimeNormMinutes jsonNum `json:"time_norm_minutes"`
	Zone            string  `json:"zone"`
	CalloutNumber   jsonNum `json:"callout_number"`
	Placement       string  `json:"placement"`
	Note            string  `json:"note"`
}

// Result is the parsed model output: drafted operations plus optional free-text notes.
type Result struct {
	Operations []Operation `json:"operations"`
	Notes      string      `json:"notes"`
}

// jsonNum captures a JSON number OR string as its literal string form, so a model
// that emits 0.5 and one that emits "0.5" both decode. Empty when null/absent.
type jsonNum string

// UnmarshalJSON accepts a JSON number, a JSON string, or null.
func (n *jsonNum) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	if len(b) == 0 || string(b) == "null" {
		*n = ""
		return nil
	}
	if b[0] == '"' {
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		*n = jsonNum(strings.TrimSpace(s))
		return nil
	}
	*n = jsonNum(strings.TrimSpace(string(b)))
	return nil
}

// String returns the captured literal (canonical-ish; the caller validates).
func (n jsonNum) String() string { return string(n) }

// --- OpenRouter wire types (OpenAI-compatible chat/completions) ---

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type responseFormat struct {
	Type string `json:"type"`
}

type chatRequest struct {
	Model          string          `json:"model"`
	Messages       []chatMessage   `json:"messages"`
	Temperature    float64         `json:"temperature"`
	ResponseFormat *responseFormat `json:"response_format,omitempty"`
}

type apiError struct {
	Message string `json:"message"`
	Code    any    `json:"code"`
	Type    string `json:"type"`
}

type chatResponse struct {
	Choices []struct {
		Message      chatMessage `json:"message"`
		FinishReason string      `json:"finish_reason"`
	} `json:"choices"`
	Model string    `json:"model"`
	Error *apiError `json:"error"`
}

// GenerateOperations asks the model to draft sewing operations for the given tech
// card context and free-text description. It returns a clear error on: missing key
// (ErrNotConfigured), transport failure, non-2xx API response, or malformed JSON.
func (c *Client) GenerateOperations(ctx context.Context, tcx TechCardContext, description string) (*Result, error) {
	if !c.Enabled() {
		return nil, ErrNotConfigured
	}
	if strings.TrimSpace(description) == "" {
		return nil, fmt.Errorf("openrouter: description is required")
	}

	reqBody := chatRequest{
		Model: c.cfg.Model,
		Messages: []chatMessage{
			{Role: "system", Content: systemPrompt},
			{Role: "user", Content: buildUserPrompt(tcx, description)},
		},
		Temperature:    generationTemperature,
		ResponseFormat: &responseFormat{Type: "json_object"},
	}
	payload, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("openrouter: marshal request: %w", err)
	}

	endpoint := strings.TrimRight(c.cfg.BaseURL, "/") + "/chat/completions"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("openrouter: build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+strings.TrimSpace(c.cfg.APIKey))
	// Optional OpenRouter attribution headers (used for their dashboards/rankings).
	httpReq.Header.Set("X-Title", "grbpwr-products-manager")
	httpReq.Header.Set("HTTP-Referer", "https://admin.grbpwr.com")

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("openrouter: request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return nil, fmt.Errorf("openrouter: read response: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("openrouter: API error (HTTP %d): %s", resp.StatusCode, apiErrorMessage(body))
	}

	var cr chatResponse
	if err := json.Unmarshal(body, &cr); err != nil {
		return nil, fmt.Errorf("openrouter: could not decode API response envelope: %w", err)
	}
	if cr.Error != nil && strings.TrimSpace(cr.Error.Message) != "" {
		return nil, fmt.Errorf("openrouter: API error: %s", cr.Error.Message)
	}
	if len(cr.Choices) == 0 {
		return nil, fmt.Errorf("openrouter: API response contained no choices")
	}
	content := strings.TrimSpace(cr.Choices[0].Message.Content)
	if content == "" {
		return nil, fmt.Errorf("openrouter: model returned an empty message")
	}

	result, err := parseResult(content)
	if err != nil {
		return nil, err
	}
	if len(result.Operations) > maxOperations {
		result.Operations = result.Operations[:maxOperations]
	}
	return result, nil
}

// parseResult extracts the JSON object from the model content (tolerating a ```json
// fenced block or surrounding prose) and unmarshals it into a Result.
func parseResult(content string) (*Result, error) {
	js := extractJSON(content)
	if js == "" {
		return nil, fmt.Errorf("openrouter: model output contained no JSON object: %q", truncate(content, 200))
	}
	var r Result
	if err := json.Unmarshal([]byte(js), &r); err != nil {
		return nil, fmt.Errorf("openrouter: model output was not valid operations JSON: %w", err)
	}
	return &r, nil
}

// extractJSON returns the outermost {...} object in s, first stripping a Markdown
// code fence if the model wrapped the JSON in one. Returns "" when no object found.
func extractJSON(s string) string {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "```") {
		s = strings.TrimPrefix(s, "```")
		if i := strings.IndexByte(s, '\n'); i >= 0 {
			s = s[i+1:] // drop an optional language tag line (e.g. "json")
		}
		if j := strings.LastIndex(s, "```"); j >= 0 {
			s = s[:j]
		}
		s = strings.TrimSpace(s)
	}
	start := strings.IndexByte(s, '{')
	end := strings.LastIndexByte(s, '}')
	if start < 0 || end < 0 || end < start {
		return ""
	}
	return s[start : end+1]
}

// apiErrorMessage best-effort pulls a human message out of an OpenRouter error body,
// falling back to the raw (truncated) body when it is not the expected shape.
func apiErrorMessage(body []byte) string {
	var env struct {
		Error   *apiError `json:"error"`
		Message string    `json:"message"`
	}
	if err := json.Unmarshal(body, &env); err == nil {
		if env.Error != nil && strings.TrimSpace(env.Error.Message) != "" {
			return env.Error.Message
		}
		if strings.TrimSpace(env.Message) != "" {
			return env.Message
		}
	}
	return truncate(strings.TrimSpace(string(body)), 300)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
