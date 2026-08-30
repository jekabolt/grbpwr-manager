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
	// maxResponseBytes caps how much of an API response we read (defensive). It stays at 4 MiB
	// because everything THIS package reads is text: a completion that big is already an order of
	// magnitude past any prompt here, and raising it would only buy a bigger allocation on a
	// 0.5 GiB box. What changed is what happens at the wall — see ErrResponseTooLarge: the cap
	// refuses, it no longer trims in silence.
	//
	// GENERATED PICTURES DO NOT COME THROUGH HERE. A single base64 PNG is bigger than this whole
	// ceiling, and it arrives on a DIFFERENT endpoint from a DIFFERENT catalogue — see
	// internal/orimages, which carries its own (much larger, configurable) ceiling for exactly
	// that reason. Raising this constant would not have made images fit; it would have made a
	// text path allocate for a body it can never receive.
	maxResponseBytes = 4 << 20 // 4 MiB
	// maxOperations caps how many drafted operations we return (runaway guard).
	maxOperations = 200
	// generationTemperature keeps drafts fairly deterministic/consistent.
	generationTemperature = 0.2
	// modelProbeTimeout bounds the startup model probe. It is short on purpose: the probe is a
	// courtesy, and a provider that is slow to answer at boot is not news worth waiting for.
	modelProbeTimeout = 3 * time.Second
	// analysisReasoningEffort switches EXTENDED THINKING OFF for the tech-card analysis pass.
	//
	// WHY OFF. Reasoning tokens are billed and budgeted as output tokens, and they are spent BEFORE
	// the answer. The whole analysis chain is sized around a non-reasoning completion — the cap is
	// 2500 tokens (§5: a real card answers in 1.5–2.5k), the server allows 60 s, the screen waits
	// 55 s. Pointing that chain at a model which thinks by default produced exactly one outcome on
	// the first live run: 2500 completion tokens, zero content, 42 s, ~$0.11. Turning thinking on
	// instead would mean moving the cap, the server budget, the client budget and the price of
	// every press — a product decision, not a bug fix.
	//
	// Opus with thinking off is still a stronger reader than the default slug; that is what the
	// analysis override is for. To trade money and waiting for depth later, this becomes
	// `{"max_tokens": N}` and analysisMaxTokens grows by N — in that order, and neither alone.
	//
	// Models that do not reason ignore the field; a model that marks reasoning MANDATORY would
	// refuse to turn it off, and the empty answer would then come back as ErrBudgetExhausted rather
	// than as silence.
	analysisReasoningEffort = "none"
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

// ErrBudgetExhausted is returned when the model spends the whole completion budget and hands back
// an EMPTY message — finish_reason=length with no content at all.
//
// IT IS A CONFIGURATION FAULT, NOT WEATHER, and that is the entire reason it is a sentinel. A
// reasoning model charges its thinking to the same completion budget as its answer (OpenRouter:
// "reasoning tokens are considered output tokens"), so a cap sized for a non-reasoning model is
// spent before the answer begins. Nothing about that is transient: the next press produces the
// same empty message, at the same price. This shipped once already — the first live run on prod
// burned ~$0.11 and told the human "this one is weather: retry", which is an invitation to burn it
// again, forever.
//
// The distinction from a TRUNCATED answer is finish_reason plus emptiness: content that got cut
// off is a short review and the verifier refuses it on its own terms; NO content means the budget
// never reached the answer.
var ErrBudgetExhausted = errors.New("openrouter: the model spent the whole completion budget without answering")

// ErrResponseTooLarge is returned when the provider's response body exceeds the read ceiling.
//
// IT IS AN ERROR ON PURPOSE, AND THAT IS A BEHAVIOUR CHANGE. Until now the ceiling was an
// io.LimitReader and nothing else: a body one byte over the cap came back as a TRUNCATED PREFIX,
// which then failed to unmarshal as "unexpected end of JSON input" — a sentence that names the
// wrong culprit (the provider's JSON is fine; ours is a knife). Worse, a prefix that happens to
// parse would have been accepted as a complete answer, and a silently shortened answer is the one
// failure mode nobody can see from the outside.
//
// So the cap now REFUSES rather than trims: too big is a fault with a name, and the name says
// which knob (the ceiling) is the one to turn.
var ErrResponseTooLarge = errors.New("openrouter: the provider's response exceeded the read ceiling")

// readCapped reads at most limit bytes and REFUSES anything longer instead of handing back a
// prefix. It reads limit+1 on purpose: that one extra byte is the entire difference between "the
// body is exactly at the ceiling" and "the body is over it", and without it the two are
// indistinguishable.
//
// `what` names the body in the error, so a log line says which of the package's two routes
// (completion vs model probe) hit the wall.
func readCapped(r io.Reader, limit int64, what string) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > limit {
		return nil, fmt.Errorf("%w: %s is larger than %d bytes", ErrResponseTooLarge, what, limit)
	}
	return body, nil
}

// Config is the OpenRouter client configuration. Bound in config/cfg.go; every
// field is optional except APIKey (without which the client is disabled).
type Config struct {
	APIKey      string        `mapstructure:"api_key"`      // OPENROUTER_API_KEY; empty = disabled
	Model       string        `mapstructure:"model"`        // OPENROUTER_MODEL; empty = defaultModel
	BaseURL     string        `mapstructure:"base_url"`     // OPENROUTER_BASE_URL; empty = defaultBaseURL
	HTTPTimeout time.Duration `mapstructure:"http_timeout"` // OPENROUTER_HTTP_TIMEOUT; <=0 = defaultTimeout
	// ModelAnalysis is the OPTIONAL slug for the tech-card analysis pass (OPENROUTER_MODEL_ANALYSIS).
	// EMPTY IS THE NORMAL STATE and means "the shared slug": the override exists so escalating the
	// quality of that one pass costs an env var instead of a deploy.
	//
	// IT DELIBERATELY HAS NO DEFAULT CONSTANT OF ITS OWN. A second baked-in slug would be a second
	// thing that rots silently at the provider, and one such constant (defaultModel) already carries
	// every AI feature; the outage it documents is exactly what a forgotten second one would repeat.
	//
	// It only reaches the process because config/cfg.go binds the name EXPLICITLY: AutomaticEnv is
	// intentionally off in this repo, so an unbound variable is silently empty — and silently empty
	// is indistinguishable from the correct default, which is why the binding has its own test.
	ModelAnalysis string `mapstructure:"model_analysis"`
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
	// Trimmed, but NOT defaulted: empty stays empty and is resolved to the shared slug at read time
	// by AnalysisModel, so there is exactly one place that decides what "unset" means.
	cfg.ModelAnalysis = strings.TrimSpace(cfg.ModelAnalysis)
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

// AnalysisModel returns the effective model slug for the tech-card analysis pass: the optional
// OPENROUTER_MODEL_ANALYSIS override, or the shared slug when that override is unset — which is the
// normal state on every deployment. Nil-safe.
//
// Callers that REPORT which model answered (the analysis response carries the slug so a
// "model_unavailable" verdict names the knob to turn) must use this, not Model(), or the panel will
// name a slug that was never called.
func (c *Client) AnalysisModel() string {
	if c == nil {
		return ""
	}
	if m := strings.TrimSpace(c.cfg.ModelAnalysis); m != "" {
		return m
	}
	return c.cfg.Model
}

// effectiveModel is one slug this client can actually send, paired with what stops working if the
// provider no longer serves it. The pairing is the whole value of the boot warning: "a model is
// gone" sends nobody anywhere, "tech-card analysis will refuse" does.
type effectiveModel struct {
	slug     string
	features string
}

const (
	sharedModelFeatures   = "note formatting, tech-card operation drafts and campaign auto-translation"
	analysisModelFeatures = "tech-card construction analysis"
)

// effectiveModels returns the DISTINCT slugs this client can send. It is a set on purpose: with
// OPENROUTER_MODEL_ANALYSIS unset — again, the normal state — both roles are the same string, and
// probing it twice would double the boot traffic and shout twice about a single fault.
func (c *Client) effectiveModels() []effectiveModel {
	if c == nil {
		return nil
	}
	shared := strings.TrimSpace(c.cfg.Model)
	analysis := strings.TrimSpace(c.AnalysisModel())
	if shared != "" && analysis == shared {
		return []effectiveModel{{slug: shared, features: sharedModelFeatures + ", " + analysisModelFeatures}}
	}
	out := make([]effectiveModel, 0, 2)
	if shared != "" {
		out = append(out, effectiveModel{slug: shared, features: sharedModelFeatures})
	}
	if analysis != "" {
		out = append(out, effectiveModel{slug: analysis, features: analysisModelFeatures})
	}
	return out
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

// reasoningSpec is OpenRouter's `reasoning` object. Only ONE of effort/max_tokens may be set — the
// provider documents them as alternatives, not as a pair. Omitted entirely (nil) the provider's own
// default stands, which is what every feature except the analysis pass wants.
type reasoningSpec struct {
	Effort string `json:"effort,omitempty"`
}

type chatRequest struct {
	Model    string        `json:"model"`
	Messages []chatMessage `json:"messages"`
	// MaxTokens caps the COMPLETION. Omitted when zero, which leaves the provider's own default in
	// force — the behaviour every caller had before the field existed. The analysis pass sets it
	// explicitly, because a default this codebase does not own is not a budget it can reason about.
	MaxTokens      int             `json:"max_tokens,omitempty"`
	Temperature    float64         `json:"temperature"`
	ResponseFormat *responseFormat `json:"response_format,omitempty"`
	// Reasoning is sent only by the analysis pass. Nil leaves the provider default in force, which
	// is the behaviour every other caller had before the field existed.
	Reasoning *reasoningSpec `json:"reasoning,omitempty"`
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
	Usage Usage     `json:"usage"`
	Error *apiError `json:"error"`
}

// Usage is the token accounting the provider returns for one completion. The field names are the
// OpenAI-compatible ones OpenRouter answers with; the Go names are shortened because the "_tokens"
// suffix on a type called Usage says nothing.
//
// A MISSING OR MISSPELLED TAG HERE IS SILENT: every field simply stays zero, the call still
// succeeds, and the only symptom is that the price of every run reads as free in the log. That is
// why the test asserts NON-ZERO numbers rather than "no error".
type Usage struct {
	Prompt     int `json:"prompt_tokens"`
	Completion int `json:"completion_tokens"`
	Total      int `json:"total_tokens"`
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

	content, _, _, err := c.chat(ctx, chatRequest{
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
//
// It is a thin wrapper over the shared path with the SHARED slug and no explicit token cap —
// exactly what it did before the response metadata existed — so callers that do not care about the
// finish reason or the token bill keep the behaviour they had.
func (c *Client) Complete(ctx context.Context, systemPrompt, userPrompt string, jsonMode bool) (string, error) {
	// nil reasoning: the provider default, i.e. exactly what these features did before the field
	// existed. Only the analysis pass has a budget tight enough to care.
	text, _, _, err := c.complete(ctx, c.Model(), systemPrompt, userPrompt, jsonMode, 0, nil)
	return text, err
}

// CompleteWithMeta runs a single chat completion for the TECH-CARD ANALYSIS pass and returns the
// assistant content together with what the caller needs in order to judge it.
//
// WHY THE METADATA IS NOT OPTIONAL. Without finishReason a reply truncated by the token cap is
// indistinguishable from a model that emitted broken JSON, and those two owe the human different
// sentences ("it was cut off, ask again" vs "the model misbehaved"). Without usage the price of a
// run is invisible, and a per-press LLM call whose cost nobody can see is a bill nobody notices.
//
// maxTokens caps the completion; <= 0 omits the field and leaves the provider default in force.
//
// THE SLUG IS AnalysisModel(), NOT Model(). This method exists for the analysis pass, and
// OPENROUTER_MODEL_ANALYSIS exists to escalate exactly that pass without dragging the features
// behind Complete onto a different model. With the override unset the two are the same string.
func (c *Client) CompleteWithMeta(ctx context.Context, systemPrompt, userPrompt string, jsonMode bool, maxTokens int) (text string, finishReason string, usage Usage, err error) {
	// THE ONLY CALLER THAT SETS `reasoning`. See analysisReasoningEffort for why it is off here and
	// nowhere else: this is the one pass whose token cap, server budget and screen budget were all
	// sized for a completion that is answer and nothing but answer.
	return c.complete(ctx, c.AnalysisModel(), systemPrompt, userPrompt, jsonMode, maxTokens,
		&reasoningSpec{Effort: analysisReasoningEffort})
}

// complete is the shared body of Complete and CompleteWithMeta: one place that decides the request
// shape, so the slug, the temperature and the JSON-mode flag cannot drift between the two entry
// points. The model is a parameter precisely because the two differ in that one value.
func (c *Client) complete(ctx context.Context, model, systemPrompt, userPrompt string, jsonMode bool, maxTokens int, reasoning *reasoningSpec) (string, string, Usage, error) {
	if !c.Enabled() {
		return "", "", Usage{}, ErrNotConfigured
	}
	req := chatRequest{
		Model: model,
		Messages: []chatMessage{
			{Role: "system", Content: systemPrompt},
			{Role: "user", Content: userPrompt},
		},
		Temperature: generationTemperature,
	}
	if jsonMode {
		req.ResponseFormat = &responseFormat{Type: "json_object"}
	}
	if maxTokens > 0 {
		req.MaxTokens = maxTokens
	}
	req.Reasoning = reasoning
	return c.chat(ctx, req)
}

// chat performs one chat/completions request from the TEXT-ONLY request shape and returns the
// assistant message content plus the response metadata (finish reason, token usage), or a clear
// error on transport failure, non-2xx status, or an empty/malformed envelope.
//
// GenerateOperations, Complete and CompleteWithMeta all funnel through it. The multimodal entry
// point (see multimodal.go) has its OWN request struct — because the wire shape of `content` is
// genuinely different there and merging the two would mean typing this one's Content as `any` —
// but it shares the transport below, so the auth header, the 404 classification and the
// response-size cap still live in exactly one place.
func (c *Client) chat(ctx context.Context, reqBody chatRequest) (string, string, Usage, error) {
	payload, err := json.Marshal(reqBody)
	if err != nil {
		return "", "", Usage{}, fmt.Errorf("openrouter: marshal request: %w", err)
	}
	return c.postChatCompletion(ctx, payload)
}

// postChatCompletion is THE ONLY PLACE THIS PACKAGE TALKS TO /chat/completions. It takes an
// already-marshalled body precisely so the two request shapes (text-only chatRequest, multimodal
// multimodalRequest) can differ in structure without duplicating the auth header, the status
// classification, the size ceiling or the envelope rules.
func (c *Client) postChatCompletion(ctx context.Context, payload []byte) (string, string, Usage, error) {
	endpoint := strings.TrimRight(c.cfg.BaseURL, "/") + "/chat/completions"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return "", "", Usage{}, fmt.Errorf("openrouter: build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+strings.TrimSpace(c.cfg.APIKey))
	httpReq.Header.Set("X-Title", "grbpwr-products-manager")
	httpReq.Header.Set("HTTP-Referer", "https://admin.grbpwr.com")

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return "", "", Usage{}, fmt.Errorf("openrouter: request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := readCapped(resp.Body, maxResponseBytes, "chat/completions response")
	if err != nil {
		return "", "", Usage{}, fmt.Errorf("openrouter: read response: %w", err)
	}
	// A 404 is the one non-2xx that is NOT weather — see ErrModelUnavailable. It is wrapped rather
	// than replaced: the provider's own sentence and the status still reach the log, they simply
	// stop being the thing a caller has to pattern-match to know a retry is pointless.
	if resp.StatusCode == http.StatusNotFound {
		return "", "", Usage{}, fmt.Errorf("%w: API error (HTTP %d): %s", ErrModelUnavailable, resp.StatusCode, apiErrorMessage(body))
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", "", Usage{}, fmt.Errorf("openrouter: API error (HTTP %d): %s", resp.StatusCode, apiErrorMessage(body))
	}
	var cr chatResponse
	if err := json.Unmarshal(body, &cr); err != nil {
		return "", "", Usage{}, fmt.Errorf("openrouter: could not decode API response envelope: %w", err)
	}
	if cr.Error != nil && strings.TrimSpace(cr.Error.Message) != "" {
		return "", "", Usage{}, fmt.Errorf("openrouter: API error: %s", cr.Error.Message)
	}
	if len(cr.Choices) == 0 {
		return "", "", Usage{}, fmt.Errorf("openrouter: API response contained no choices")
	}
	content := strings.TrimSpace(cr.Choices[0].Message.Content)
	if content == "" {
		// The usage still rides along: an empty message is not a free call, and the log line that
		// reports the failure is the one place the spend would otherwise vanish from.
		//
		// EMPTY-BECAUSE-THE-BUDGET-RAN-OUT IS ITS OWN FAULT. finish_reason=length with no content
		// says the cap was reached before a single character of answer — deterministic, and the
		// caller owes the human "the setting is wrong", not "try again". Empty for any other reason
		// stays an unclassified fault: it is genuinely a misbehaving provider.
		if strings.EqualFold(strings.TrimSpace(cr.Choices[0].FinishReason), "length") {
			return "", cr.Choices[0].FinishReason, cr.Usage, fmt.Errorf(
				"%w (%d completion tokens spent, none of them answer)", ErrBudgetExhausted, cr.Usage.Completion)
		}
		return "", cr.Choices[0].FinishReason, cr.Usage, fmt.Errorf("openrouter: model returned an empty message")
	}
	return content, cr.Choices[0].FinishReason, cr.Usage, nil
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
	return c.checkModel(ctx, c.cfg.Model)
}

// checkModel is CheckModel for ONE named slug. It exists because a client now has a SET of
// effective slugs (the shared one and, when OPENROUTER_MODEL_ANALYSIS is set, the analysis one),
// and the boot warning has to ask about each — with the same route, the same verdict rule and the
// same silence contract, from one body.
func (c *Client) checkModel(ctx context.Context, model string) error {
	endpoint := strings.TrimRight(c.cfg.BaseURL, "/") + "/models/" + strings.TrimSpace(model) + "/endpoints"
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
	body, err := readCapped(resp.Body, maxResponseBytes, "model probe response")
	if err != nil {
		return fmt.Errorf("openrouter: read model probe: %w", err)
	}
	if resp.StatusCode == http.StatusNotFound {
		return fmt.Errorf("%w: %q is not a model the provider knows", ErrModelUnavailable, model)
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
		return fmt.Errorf("%w: %q has no live endpoints at the provider", ErrModelUnavailable, model)
	}
	return nil
}

// WarnIfModelRetired probes EVERY effective model slug in the BACKGROUND and shouts in the log if the
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
	// The set is taken on the calling goroutine so the work is decided by the configuration as it
	// stands at boot, not as it might be read later.
	models := c.effectiveModels()
	go func() {
		// The probe touches only its own request; a panic here would still take the whole process
		// down, and this runs at start-up. A check that can stop a deploy is worse than the fault
		// it reports.
		defer func() {
			if r := recover(); r != nil {
				slog.Default().Warn("openrouter model probe panicked", slog.Any("recovered", r))
			}
		}()
		// Sequential, each with its OWN short timeout: the slugs are one or two, the budget stays
		// bounded per probe, and nothing downstream waits on any of it.
		for _, m := range models {
			func() {
				ctx, cancel := context.WithTimeout(context.Background(), modelProbeTimeout)
				defer cancel()
				if err := c.checkModel(ctx, m.slug); errors.Is(err, ErrModelUnavailable) {
					slog.Default().Error(
						"OPENROUTER MODEL IS NOT SERVED — the features on this slug will refuse",
						slog.String("model", m.slug), slog.String("affects", m.features),
						slog.String("base_url", c.BaseURL()), slog.String("err", err.Error()))
				}
			}()
		}
	}()
}
