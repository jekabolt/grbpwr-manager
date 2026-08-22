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
	"log/slog"
	"net/http"
	"strings"
	"time"
)

const (
	// defaultModel is the OpenRouter model slug used when none is configured. IT IS LOAD-BEARING:
	// OPENROUTER_MODEL is unset on both beta and prod, so this constant is the model every AI
	// feature actually runs on. The previous value (anthropic/claude-3.5-sonnet) was retired by the
	// provider and the calls started coming back as HTTP 404 in 0.2 s — see ErrModelUnavailable.
	// Anything put here must be verified against the live https://openrouter.ai/api/v1/models list
	// before it is committed; guessing a plausible slug is exactly how the outage happened.
	defaultModel = "anthropic/claude-sonnet-5"
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
	// modelProbeTimeout bounds the startup model probe. It is short on purpose: the probe is a
	// courtesy, and a provider that is slow to answer at boot is not news worth waiting for.
	modelProbeTimeout = 3 * time.Second
)

// ErrNotConfigured is returned when GenerateOperations is called with no API key.
// Callers should surface it as a clear "not configured" precondition failure.
var ErrNotConfigured = errors.New("openrouter: OPENROUTER_API_KEY is not set")

// ErrModelUnavailable is returned when the provider answers 404: the configured model slug is not
// served by it — retired, renamed, or never existing — or, with a custom OPENROUTER_BASE_URL, the
// endpoint itself is not there. Both are the SAME KIND of fault, and that is why this sentinel
// stands apart from a transport failure: nothing about either is transient, so the caller owes the
// human "the setting is wrong", not "try again in a moment".
//
// CLASSIFICATION IS BY STATUS ALONE. No substring of the provider's English sentence is matched,
// so a reworded provider message cannot silently reclassify the fault. That costs exactly one
// thing: when a model that does exist is momentarily unroutable, OpenRouter also answers 404, and
// we will then call a passing outage a configuration fault. That is the cheap direction — it sends
// somebody to read OPENROUTER_MODEL once. The opposite direction is what actually shipped: a
// retired slug reported as weather, retried forever by a person the interface had promised it was
// temporary.
var ErrModelUnavailable = errors.New("openrouter: the configured model is not available at the provider")

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

// BaseURL returns the effective API root. It exists for LOG LINES: a 404 can mean the model slug
// is gone or that the base URL points somewhere without this route, and a log that names only the
// slug sends the reader to the wrong knob. Nil-safe.
func (c *Client) BaseURL() string {
	if c == nil {
		return ""
	}
	return c.cfg.BaseURL
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
	// Works — КАТАЛОГ РАБОТ (0329/0331), уже отфильтрованный от снятых пунктов вызывающим. Он не
	// свойство карточки, а словарь, и едет тем же сообщением по одной причине: словарь читается из
	// БАЗЫ, а системный промпт этого пакета собирается один раз на процесс из статических словарей
	// entity. Пустой срез — законное состояние («этот сервер каталога не загрузил»), и промпт тогда
	// не говорит о работах ВОВСЕ: спросить токен, не показав списка, значило бы попросить выдумать.
	Works []WorkContext
	// RequiredSeamAllowanceMm is the card's allowance standard in MILLIMETRES ("" = none set). Stated
	// so a draft does not propose per-step allowances that contradict the card.
	RequiredSeamAllowanceMm string
}

// PieceContext is one structural cut-piece of the garment.
type PieceContext struct {
	Name             string
	PiecesPerGarment int
	// CutSymmetry is the 0275 marking (identical|mirrored|fold), EMPTY when the piece is not marked.
	// Empty must stay empty in the prompt: telling the model "identical" about a piece nobody has
	// classified would invent a fact for it to reason from, and this context exists to describe the
	// card, not to guess at it.
	CutSymmetry string
	Grainline   string
	Fused       bool
	Note        string
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

// ConstructionContext is the card's DEFAULTS, if any — what a drafted step inherits rather than
// restates. Empty strings stay empty in the prompt: naming a default nobody configured would invent
// a fact for the model to reason from.
//
// MachineProfiles / PressProfiles are the card's equipment park, ALREADY RENDERED as one summary
// line each («overlock: 4 threads, ballpoint needle Nm 90, 4 st/cm»). They replace the single
// OverlockThreadCount, which could only ever describe one overlock on a card that may run several —
// and, more to the point, a step now says «machine» + «on an overlock», so the park is what tells
// the model which machines this style is actually sewn on and which settings it may leave out.
//
// The lines carry NO profile keys, and never will: the model does not create profiles and cannot
// pick between two identical overlocks — it answers with the machine or the equipment TYPE. The
// caller attaches the profile afterwards, and only where that type names exactly one of them — for a
// pressing line, exactly one FOR THE STEP'S PROCESS, since a profile declared for ironing is not a
// fusing recipe and is not attached to a fusing step. Where the question has several answers, the
// rendered line says so (the caller marks it) and the prompt then asks for the settings outright
// instead of promising an inheritance nothing would deliver.
type ConstructionContext struct {
	DefaultSeamClass     string
	DefaultStitchesPerCm string
	MachineProfiles      []string
	PressProfiles        []string
}

// WorkContext is one row of the work catalog as the prompt shows it: WHAT the step is, in the word
// a technologist says at the machine.
//
// СИНОНИМЫ ЗДЕСЬ — НЕСУЩАЯ ЧАСТЬ, А НЕ УКРАШЕНИЕ. Вход этой функции — РЕЧЬ ТЕХНОЛОГА («подогнуть
// низ московским», «поставить закрепку»), и ярлык каталога написан по-английски. Без цеховых слов
// модели пришлось бы переводить с русского на английский и обратно угадывать токен — то есть ровно
// тот способ, которым сто прод-строк оказались в неразличимой свалке. С ними задача становится
// СОПОСТАВЛЕНИЕМ: слово из описания стоит в списке рядом с токеном.
//
// Verb и Machines едут не для красоты: работа НЕСЁТ глагол, и правила когерентности 0330 отвергают
// шаг, чей глагол ей не равен, а при machine_mode = ask — и машинку вне её списка. Модель, которая
// видит и то и другое, отвечает согласованно; модель, которая видит один токен, — как повезёт.
type WorkContext struct {
	Token string
	Label string
	Verb  string
	// Machines пуст у работ, у которых ось «на чём» не машинная вовсе (ВТО, фурнитура, финиш), и у
	// работ режима fixed он несёт ровно одну машинку — ту, что и так следует из работы.
	Machines []string
	Syn      []string
}

// Operation is one drafted sewing operation as returned by the model. Numeric-ish fields are
// captured as jsonNum (tolerating both JSON numbers and strings); the caller parses/validates them
// when mapping to the persisted operation shape.
//
// EVERY DESCRIPTIVE FIELD IS NOW A DICTIONARY TOKEN, and that is the point of the shape: the old
// struct held twelve bare strings, so the model answered «оверлок 4-нит.» or «overlock 4 thread» or
// anything else, and whatever came back was stored verbatim because there was nothing to check it
// against. A token either resolves to an enum value or becomes UNKNOWN for a human to fix.
//
// The two axes are why machine_type exists beside operation_type: the type says WHAT the step does
// (machine work, pressing, fusing, handwork) and the machine says WHAT IT IS DONE ON. One word could
// not carry both, which is why a draft used to answer «overlock» and leave the ВТО steps with no
// vocabulary at all — a press step had nothing to say about the iron, the temperature or the cloth.
type Operation struct {
	OperationNumber  jsonNum `json:"operation_number"`
	OperationType    string  `json:"operation_type"`
	Zone             string  `json:"zone"`
	SeamClass        string  `json:"seam_class"`
	StitchesPerCm    jsonNum `json:"stitches_per_cm"`
	SeamAllowanceMm  jsonNum `json:"seam_allowance_mm"`
	TopstitchMode    string  `json:"topstitch_mode"`
	TopstitchWidthMm jsonNum `json:"topstitch_width_mm"`
	TopstitchRows    jsonNum `json:"topstitch_rows"`
	AttachmentKind   string  `json:"attachment_kind"`
	SmvMinutes       jsonNum `json:"smv_minutes"`
	CalloutNumber    jsonNum `json:"callout_number"`
	Note             string  `json:"note"`

	// Work — ТРЕТЬЯ ОСЬ ШАГА (0330): КАКАЯ это работа, токеном каталога. Строка, а не член
	// перечисления, ровно по той же причине, по которой она строка на проводе: каталог — ДАННЫЕ,
	// он растёт INSERT-миграцией, а незнакомый член enum protojson выбросил бы молча.
	//
	// Выдуманный токен здесь стоит ровно одно поле: вызывающий сверяет его с каталогом и на промах
	// оставляет работу пустой, не трогая остальной шаг. Промпт просит об этом прямо — «назови
	// работу токеном или промолчи».
	Work string `json:"work"`

	// The machine step: «on what», plus the settings that deviate from the card's profile.
	MachineType   string  `json:"machine_type"`
	ThreadCount   jsonNum `json:"thread_count"`
	NeedleType    string  `json:"needle_type"`
	NeedleSizeNm  jsonNum `json:"needle_size_nm"`
	ThreadTension string  `json:"thread_tension"`
	// The qualifier that makes the scale usable. The scale is CLOSED (looser / normal / tighter /
	// other) precisely because a free string could not be compared between two machines — and
	// «other» is then an answer with nothing in it unless this field travels beside it. Without the
	// field in the shape the model has no way to say what «other» meant, and a model that says it
	// anyway has the sentence silently dropped by the decoder.
	ThreadTensionNote string  `json:"thread_tension_note"`
	StitchWidthMm     jsonNum `json:"stitch_width_mm"` // zigzag amplitude / overlock bite, NOT the topstitch width

	// The ВТО block: press / press_open / fusing. No profile key here either — see ConstructionContext.
	PressEquipment    string  `json:"press_equipment"`
	PressTemperatureC jsonNum `json:"press_temperature_c"`
	PressDwellSec     jsonNum `json:"press_dwell_sec"`
	PressPressureNCm2 jsonNum `json:"press_pressure_n_cm2"` // pressure on the cloth, N/cm²
	// Three-valued, hence jsonBool and not bool: absent = the model said nothing, false = «без пара»,
	// which is a real instruction a two-valued field would quietly turn back into a default.
	PressSteam jsonBool `json:"press_steam"`
	PressCloth string   `json:"press_cloth"`
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

// jsonBool is an OPTIONAL boolean with presence: it has to hold «not stated», «yes» and «no» as
// three answers, because press_steam does.
//
// It NEVER fails to unmarshal, and that is the point rather than laxity: a plain *bool would return
// an UnmarshalTypeError on `"press_steam": "yes"`, and json.Unmarshal reports that for the whole
// document — one hedged word in one step would throw away the entire draft. Anything unrecognised
// is simply «not stated», which is what an unanswered question means everywhere else in this file.
type jsonBool struct {
	set   bool
	value bool
}

// UnmarshalJSON accepts true/false, the same words quoted, the usual yes/no/1/0 spellings, and null.
func (b *jsonBool) UnmarshalJSON(data []byte) error {
	*b = jsonBool{}
	s := strings.ToLower(strings.Trim(strings.TrimSpace(string(data)), `"`))
	switch strings.TrimSpace(s) {
	case "true", "yes", "y", "1", "on":
		*b = jsonBool{set: true, value: true}
	case "false", "no", "n", "0", "off":
		*b = jsonBool{set: true, value: false}
	}
	return nil
}

// Ptr renders the three states as the wire's optional bool: nil when the model said nothing.
func (b jsonBool) Ptr() *bool {
	if !b.set {
		return nil
	}
	v := b.value
	return &v
}

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

	content, err := c.chat(ctx, chatRequest{
		Model: c.cfg.Model,
		Messages: []chatMessage{
			{Role: "system", Content: systemPrompt},
			{Role: "user", Content: buildUserPrompt(tcx, description)},
		},
		Temperature:    generationTemperature,
		ResponseFormat: &responseFormat{Type: "json_object"},
	})
	if err != nil {
		return nil, err
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

// Complete runs a single chat completion and returns the assistant's raw message content. It is
// the generic primitive behind feature-specific methods (e.g. translation). jsonMode requests a
// JSON-object response from the model. Returns ErrNotConfigured when no API key is set.
func (c *Client) Complete(ctx context.Context, systemPrompt, userPrompt string, jsonMode bool) (string, error) {
	if !c.Enabled() {
		return "", ErrNotConfigured
	}
	req := chatRequest{
		Model: c.cfg.Model,
		Messages: []chatMessage{
			{Role: "system", Content: systemPrompt},
			{Role: "user", Content: userPrompt},
		},
		Temperature: generationTemperature,
	}
	if jsonMode {
		req.ResponseFormat = &responseFormat{Type: "json_object"}
	}
	return c.chat(ctx, req)
}

// chat performs one chat/completions request and returns the assistant message content, or a
// clear error on transport failure, non-2xx status, or an empty/malformed envelope.
func (c *Client) chat(ctx context.Context, reqBody chatRequest) (string, error) {
	payload, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("openrouter: marshal request: %w", err)
	}
	endpoint := strings.TrimRight(c.cfg.BaseURL, "/") + "/chat/completions"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return "", fmt.Errorf("openrouter: build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+strings.TrimSpace(c.cfg.APIKey))
	httpReq.Header.Set("X-Title", "grbpwr-products-manager")
	httpReq.Header.Set("HTTP-Referer", "https://admin.grbpwr.com")

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return "", fmt.Errorf("openrouter: request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return "", fmt.Errorf("openrouter: read response: %w", err)
	}
	// A 404 is the one non-2xx that is NOT weather — see ErrModelUnavailable. It is wrapped rather
	// than replaced: the provider's own sentence and the status still reach the log, they simply
	// stop being the thing a caller has to pattern-match to know a retry is pointless.
	if resp.StatusCode == http.StatusNotFound {
		return "", fmt.Errorf("%w: API error (HTTP %d): %s", ErrModelUnavailable, resp.StatusCode, apiErrorMessage(body))
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("openrouter: API error (HTTP %d): %s", resp.StatusCode, apiErrorMessage(body))
	}
	var cr chatResponse
	if err := json.Unmarshal(body, &cr); err != nil {
		return "", fmt.Errorf("openrouter: could not decode API response envelope: %w", err)
	}
	if cr.Error != nil && strings.TrimSpace(cr.Error.Message) != "" {
		return "", fmt.Errorf("openrouter: API error: %s", cr.Error.Message)
	}
	if len(cr.Choices) == 0 {
		return "", fmt.Errorf("openrouter: API response contained no choices")
	}
	content := strings.TrimSpace(cr.Choices[0].Message.Content)
	if content == "" {
		return "", fmt.Errorf("openrouter: model returned an empty message")
	}
	return content, nil
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

// --- startup model probe ---
//
// WHY THIS EXISTS. A retired model slug is invisible until somebody presses a button, and on beta
// that took weeks: three features were dead and the one complaint that surfaced arrived by hand.
// The provider will publish the fact for free — so ask it once at boot, and put the answer where
// somebody is already looking.
//
// WHY IT IS SHAPED LIKE THIS. A boot-time check that can delay or fail a start is a worse defect
// than the one it reports; this project has already had a deploy halted by a check that looked
// harmless. So: own goroutine, short timeout, log-only, no client state, nothing refused on its
// basis, and silence on every outcome that is not a clear "this slug has no endpoints".

// modelEndpointsResponse is the shape of GET /models/{slug}/endpoints.
//
// EVERY FIELD IS A POINTER, and that is the whole safety of the probe. `endpoints: []` is the
// alarm, so a response that simply does not CARRY that key — a reshaped API, an HTML error page
// from a proxy, a body we do not understand — must not decode to "zero endpoints" and shout. Absent
// and empty have to be different values here, and with a plain slice they would not be.
type modelEndpointsResponse struct {
	Data *struct {
		Endpoints *[]json.RawMessage `json:"endpoints"`
	} `json:"data"`
}

// CheckModel asks the provider whether the effective model slug has any live endpoint, via the
// public GET {base}/models/{slug}/endpoints route.
//
// THE ROUTE AND THE VERDICT WERE BOTH MEASURED AGAINST THE LIVE API, because the obvious versions
// of each are wrong:
//   - GET /models/{slug} answers 404 for EVERY slug, live ones included. A probe built on it would
//     have shouted on every boot of every deployment — an alarm that is always on is an alarm
//     nobody reads.
//   - GET /models/{slug}/endpoints answers 200 for a retired slug too. `anthropic/claude-3.5-sonnet`
//     — the slug that caused the outage — still returns 200; what distinguishes it is that its
//     endpoints array is EMPTY, while a live slug carries several. So the verdict is the array, not
//     the status, and 404 is kept only for a slug that never existed at all.
//
// Returns ErrModelUnavailable when the slug has no endpoints (or does not exist), nil when it has
// at least one, ErrNotConfigured when the client is disabled, and an ordinary error for every
// "could not find out" — which callers are expected to treat as silence, not as bad news.
func (c *Client) CheckModel(ctx context.Context) error {
	if !c.Enabled() {
		return ErrNotConfigured
	}
	endpoint := strings.TrimRight(c.cfg.BaseURL, "/") + "/models/" + strings.TrimSpace(c.cfg.Model) + "/endpoints"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return fmt.Errorf("openrouter: build model probe: %w", err)
	}
	// No Authorization header: the route is public, and sending the key would only add ways to get
	// a 401 that says nothing about the model. A private proxy that demands auth simply answers
	// 401/403, which lands in the silent branch.
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("openrouter: model probe failed: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return fmt.Errorf("openrouter: read model probe: %w", err)
	}
	if resp.StatusCode == http.StatusNotFound {
		return fmt.Errorf("%w: %q is not a model the provider knows", ErrModelUnavailable, c.cfg.Model)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("openrouter: model probe (HTTP %d): %s", resp.StatusCode, apiErrorMessage(body))
	}
	var mr modelEndpointsResponse
	if err := json.Unmarshal(body, &mr); err != nil {
		return fmt.Errorf("openrouter: could not decode model probe: %w", err)
	}
	if mr.Data == nil || mr.Data.Endpoints == nil {
		return fmt.Errorf("openrouter: model probe carried no endpoints field")
	}
	if len(*mr.Data.Endpoints) == 0 {
		return fmt.Errorf("%w: %q has no live endpoints at the provider", ErrModelUnavailable, c.cfg.Model)
	}
	return nil
}

// WarnIfModelRetired probes the effective model slug in the BACKGROUND and shouts in the log if the
// provider serves no endpoint for it. It returns immediately and is safe to call from a start-up
// path: the goroutine is owned here rather than by the caller precisely so no call site can forget
// the `go`, the timeout, or the recover.
//
// It changes nothing and refuses nothing — the client keeps working and every feature keeps its own
// error handling. Anything other than a clear verdict is silence: a boot that cannot reach the
// network is not evidence that a model is gone, and a false alarm on that line would teach people
// to ignore the true one.
func (c *Client) WarnIfModelRetired() {
	if !c.Enabled() {
		return // no key: nothing is calling the provider anyway, so there is nothing to warn about
	}
	go func() {
		// The probe touches only its own request; a panic here would still take the whole process
		// down, and this runs at start-up. A check that can stop a deploy is worse than the fault
		// it reports.
		defer func() {
			if r := recover(); r != nil {
				slog.Default().Warn("openrouter model probe panicked", slog.Any("recovered", r))
			}
		}()
		ctx, cancel := context.WithTimeout(context.Background(), modelProbeTimeout)
		defer cancel()
		if err := c.CheckModel(ctx); errors.Is(err, ErrModelUnavailable) {
			slog.Default().Error(
				"OPENROUTER MODEL IS NOT SERVED — note formatting, tech-card operation drafts and campaign auto-translation will all refuse",
				slog.String("model", c.Model()), slog.String("base_url", c.BaseURL()),
				slog.String("err", err.Error()))
		}
	}()
}
