// Package openrouter is a small client for the OpenRouter chat/completions API
// (https://openrouter.ai). It carries the admin AI text features — note formatting, campaign
// auto-translation, the design idea draft, the tech-card construction analysis, the `ai ✦`
// rewrite and the playground Ideas door — through a few generic entry points (Complete,
// CompleteWithMeta, CompleteWithImages, CompleteWithImagesOn). Each feature owns its prompt and
// the reading of its answer; this package owns the wire.
//
// The client is optional and degrades gracefully: when no API key is configured
// Enabled() is false and every entry point returns ErrNotConfigured, so the admin
// service keeps working with those features simply unavailable.
//
// THE WIRE IS NO LONGER HERE (B-11). Every chat call goes through the provider-neutral transport
// internal/aiprov/oaichat (Dialect OpenRouter), built in New from this Config: the budget, the engaged
// flag, the read ceiling, the status classification and the error sentences live there, once, for
// every OpenAI-shaped provider. This package keeps what is OpenRouter's or the features' own: the
// slugs and their env knobs, the prompts, the reasoning-effort policy per entry point, the operation
// parser and the startup model probe (OpenRouter's endpoints API). Failures come back as
// *aiprov.CallError — its Error() is the same sentence this client always wrote.
package openrouter

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/shopspring/decimal"

	"github.com/jekabolt/grbpwr-manager/internal/aiprov"
	"github.com/jekabolt/grbpwr-manager/internal/aiprov/oaichat"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
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
	// defaultTimeout is the BASE of a call's budget (connect, upload, pictures, time-to-first-token);
	// the time the answer takes to print is added on top by CompletionBudget. The number and the whole
	// story of why it is a base and not a ceiling now live in aiprov (budget.go: DefaultBudgetBase) —
	// one formula for every transport and for every lease that has to outlive a call.
	defaultTimeout = aiprov.DefaultBudgetBase
	// maxResponseBytes caps how much of a response is read — the chat answer (in the transport:
	// oaichat.MaxResponseBytes, whose comment carries the reasoning) and the model probe below, which
	// reads through this package's own readCapped. One number for both, as before the move.
	maxResponseBytes = oaichat.MaxResponseBytes
	// modelProbeTimeout bounds the startup model probe. It is short on purpose: the probe is a
	// courtesy, and a provider that is slow to answer at boot is not news worth waiting for.
	modelProbeTimeout = 3 * time.Second
	// analysisReasoningEffort switches EXTENDED THINKING OFF wherever a TOKEN CEILING is set — the
	// tech-card analysis pass (CompleteWithMeta) and the picture-carrying call that asks for a
	// structured answer (CompleteWithImages with maxTokens > 0). Имя осталось от первого читателя;
	// правило одно на обоих: КТО СТАВИТ ПОТОЛОК, ТОТ ВЫКЛЮЧАЕТ МЫШЛЕНИЕ.
	//
	// WHY OFF. Reasoning tokens are billed and budgeted as output tokens, and they are spent BEFORE
	// the answer. The whole analysis chain is sized around a non-reasoning completion — the cap is
	// 2500 tokens (§5: a real card answers in 1.5–2.5k), the server allowed 60 s, the screen waits
	// 55 s. (⚠ «60 s» — ИСТОРИЯ: с тех пор бюджет сервера ВЫВОДИТСЯ из потолка — см. defaultTimeout
	// и CompletionBudget, — и для 2500 токенов он теперь 60 s + 2500/30 ≈ 143 s. Клиент по-прежнему
	// сдаётся первым, а это и есть тот инвариант, ради которого 55 s выбраны.) Pointing that chain
	// at a model which thinks by default produced exactly one outcome on
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
	// leastReasoningEffort is what CompleteWithImagesOn asks for under a token ceiling: the LEAST
	// reasoning every reasoning model ACCEPTS, not «none». A per-feature slug may be a model whose
	// reasoning is MANDATORY — the live catalogue (GET https://openrouter.ai/api/v1/models,
	// 2026-09-27) marks openai/gpt-5-mini `reasoning.mandatory: true`, and OpenRouter's reasoning
	// docs (https://openrouter.ai/docs/use-cases/reasoning-tokens) say such a model REJECTS
	// effort "none", while an effort a model lacks is mapped to its nearest supported level. So
	// "minimal" is the one value that is never a 400: it is google/gemini-3.1-flash-lite's own
	// default effort, and gpt-5-mini lists it.
	leastReasoningEffort = "minimal"
)

// The slugs of the `Ideas ▾` door (SuggestPrompts, PLAYGROUND B-15).
//
// ⚠ A SECOND BAKED-IN SLUG, AND THE HEADER OF Config.ModelAnalysis EXPLAINS WHY THAT IS A HAZARD:
// a constant rots silently at the provider. Three things keep this one from rotting silently —
// WarnIfModelRetired probes it at boot (effectiveModels lists it and the fallback), a 404 on it
// retries ONCE on IdeasFallbackModel, and OPENROUTER_MODEL_IDEAS replaces it without a deploy
// (`off` switches the feature off). Both slugs were read on the live catalogue on 2026-09-27
// (GET https://openrouter.ai/api/v1/models): gemini-3.1-flash-lite text+image→text, $0.25/M in,
// $1.50/M out, $0.25/M image, 8 endpoints, response_format supported; gpt-5-mini text+image→text,
// $0.25/M in, $2/M out, 4 endpoints, response_format supported, reasoning mandatory.
const (
	DefaultIdeasModel  = "google/gemini-3.1-flash-lite"
	IdeasFallbackModel = "openai/gpt-5-mini"
	// IdeasModelOff is the kill switch value of OPENROUTER_MODEL_IDEAS (case-insensitive).
	IdeasModelOff = "off"
)

// ErrNotConfigured is returned by every entry point when no API key is set.
// Callers should surface it as a clear "not configured" precondition failure.
//
// It IS aiprov.ErrNotConfigured (an alias since B-11), so errors.Is answers from either name — and
// its text is aiprov's: nothing asserts the old "OPENROUTER_API_KEY is not set" sentence, and every
// handler answers the human with its own recipe (techcard_ai.go: openRouterNoKeyMsg).
var ErrNotConfigured = aiprov.ErrNotConfigured

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
//
// An alias of aiprov.ErrModelUnavailable since B-11; the sentence keeps its "openrouter: " prefix
// because the transport writes it in front of the sentinel.
var ErrModelUnavailable = aiprov.ErrModelUnavailable

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
//
// An alias of aiprov.ErrBudgetExhausted since B-11.
var ErrBudgetExhausted = aiprov.ErrBudgetExhausted

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
//
// An alias of aiprov.ErrResponseTooLarge since B-11.
var ErrResponseTooLarge = aiprov.ErrResponseTooLarge

// ProviderEngaged — «за этот вызов поставщику уже причитаются деньги»: the request was written to the
// wire before the call failed, so the provider may be billing it.
//
// Since B-11 it is aiprov.Engaged: the transport (oaichat) sets CallError.Engaged from the httptrace
// observer (aiprov.ObserveWrite), and the table of which failure falls on which side of the boundary
// lives with it (oaichat.go: «ГРАНИЦА КУПЛЕНО / НЕ КУПЛЕНО»). The package's own engaged wrapper is gone —
// nothing produces it any more.
//
// Отвечает false на nil и на всякую ошибку, поднятую до провода. Вызывающий, который на этом ответе
// списывает, обязан списывать ВЕРХНЮЮ границу: сколько именно напечатал поставщик, здесь не знает никто
// — usage приезжает в том самом ответе, которого не было.
func ProviderEngaged(err error) bool {
	return aiprov.Engaged(err)
}

// readCapped reads at most limit bytes and REFUSES anything longer instead of handing back a
// prefix. It reads limit+1 on purpose: that one extra byte is the entire difference between "the
// body is exactly at the ceiling" and "the body is over it", and without it the two are
// indistinguishable.
//
// Since B-11 only the model probe reads through it here; the chat answer is read by the transport's
// own copy (oaichat.readCapped), with the same sentence. `what` names the body in the error.
func readCapped(r io.Reader, limit int64, what string) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > limit {
		return nil, fmt.Errorf("openrouter: %w: %s is larger than %d bytes", ErrResponseTooLarge, what, limit)
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
	// ModelAnalysis is the OPTIONAL slug of CompleteWithMeta's callers — the tech-card analysis pass
	// and EnhanceText (OPENROUTER_MODEL_ANALYSIS). EMPTY IS THE NORMAL STATE and means "the shared
	// slug": the override exists so escalating the quality of those calls costs an env var instead
	// of a deploy.
	//
	// IT DELIBERATELY HAS NO DEFAULT CONSTANT OF ITS OWN. A second baked-in slug would be a second
	// thing that rots silently at the provider, and one such constant (defaultModel) already carries
	// every AI feature; the outage it documents is exactly what a forgotten second one would repeat.
	//
	// It only reaches the process because config/cfg.go binds the name EXPLICITLY: AutomaticEnv is
	// intentionally off in this repo, so an unbound variable is silently empty — and silently empty
	// is indistinguishable from the correct default, which is why the binding has its own test.
	ModelAnalysis string `mapstructure:"model_analysis"`
	// ModelIdeas is the slug of the PLAYGROUND `Ideas ▾` door (OPENROUTER_MODEL_IDEAS). Empty — the
	// normal state — means DefaultIdeasModel; `off` switches the door off (IdeasModel() == "", and
	// the band's suggest_prompts_model is empty). Resolved only by IdeasModel.
	ModelIdeas string `mapstructure:"model_ideas"`
	// KeyFunc, when set, is asked for the key on EVERY request and by Enabled(): it is the AI
	// providers registry's hook (internal/aiprov/registry), so a key saved in the admin panel — or
	// a provider switched off there — takes effect on the next request without a redeploy. "" means
	// disabled. nil = APIKey above, exactly as before. Never serialised, never printed.
	KeyFunc func() string `mapstructure:"-"`
}

// Client is a configured OpenRouter chat client. A nil *Client is a valid,
// permanently-disabled client (Enabled() == false), so callers need not nil-check.
type Client struct {
	cfg Config
	// chat is THE transport every chat call goes through (oaichat, Dialect OpenRouter). It owns the
	// budget base (CompletionBase delegates to it), the key read per request (KeyFunc = apiKey), the
	// attribution headers and every failure's classification.
	chat *oaichat.Client
	// http serves the startup model probe only (checkModel); it has no Timeout of its own either.
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
	cfg.ModelIdeas = strings.TrimSpace(cfg.ModelIdeas)
	base := cfg.HTTPTimeout
	if base <= 0 {
		base = defaultTimeout
	}
	// ⚠ У http.Client НЕТ СОБСТВЕННОГО Timeout, И ЭТО ОБЯЗАТЕЛЬНАЯ ПОЛОВИНА ПОЧИНКИ. http.Client.Timeout
	// — одно число на все запросы клиента, поставленное до того, как стал известен размер ответа;
	// именно оно и обрывало вызов, у которого потолок был поднят. Бюджет чат-вызова ставится НА КАЖДЫЙ
	// ЗАПРОС транспортом (oaichat.post: эта база плюс время печати запрошенных токенов), а зонд модели
	// носит свой короткий срок (checkModel). Ни одного маршрута без срока здесь не осталось.
	c := &Client{cfg: cfg, http: &http.Client{}}
	c.chat = oaichat.New(oaichat.Config{
		Provider:    entity.AIProviderOpenRouter,
		BaseURL:     cfg.BaseURL,
		Dialect:     oaichat.DialectOpenRouter,
		KeyFunc:     c.apiKey,
		HTTPTimeout: base,
		Title:       "grbpwr-products-manager",
		Referer:     "https://admin.grbpwr.com",
	})
	return c
}

// Transport is the chat transport this client calls through — for the AI router (B-18), so the
// OpenRouter candidate it holds is THIS configuration (base URL, key func, budget base, attribution
// headers) rather than a second one assembled elsewhere that can drift from it. Nil-safe (nil).
func (c *Client) Transport() *oaichat.Client {
	if c == nil {
		return nil
	}
	return c.chat
}

// CompletionBudget — СКОЛЬКО ВРЕМЕНИ ИМЕЕТ ПРАВО ЗАНЯТЬ ОДИН ВЫЗОВ с потолком maxTokens. A one-line
// delegate to aiprov.CompletionBudget since B-11: the formula moved to the provider-neutral layer so
// every transport and every lease ask the same one. Kept here because store/design.HandlerLeaseFor and
// the handlers' lease tests ask it by this name.
func CompletionBudget(base time.Duration, maxTokens int) time.Duration {
	return aiprov.CompletionBudget(base, maxTokens)
}

// DefaultCompletionBudget — CompletionBudget при НЕЗАДАННОМ OPENROUTER_HTTP_TIMEOUT; a delegate to
// aiprov.DefaultCompletionBudget. ⚠ NOT the door for a lease under a live call — that one derives from
// CompletionBase() of the client that makes the call (see aiprov.DefaultCompletionBudget for why).
func DefaultCompletionBudget(maxTokens int) time.Duration {
	return aiprov.DefaultCompletionBudget(maxTokens)
}

// CompletionBase — БАЗА БЮДЖЕТА ЭТОГО КЛИЕНТА: ровно то число, которое транспорт (oaichat) кладёт
// в CompletionBudget на КАЖДОМ вызове. Nil-safe и zero-safe: и у выключенного клиента, и у
// незаданного OPENROUTER_HTTP_TIMEOUT база кодовая, той же нормализацией, что в New.
//
// ⚠ ЭКСПОРТИРОВАНА РАДИ ОДНОГО ЧИТАТЕЛЯ — ТОГО, КТО ВЫДАЁТ ЛИЗУ ПОД ЭТОТ ВЫЗОВ. Лиза обязана
// переживать вызов; «переживать» проверяемо только если оба числа приходят ИЗ ОДНОГО ПОЛЯ ОДНОГО
// ОБЪЕКТА. Пока лиза считалась от defaultTimeout, а провод — от базы клиента, это были два числа
// на оси, которую не спрашивала ни одна проба: они совпадали до тех пор, пока переменную
// окружения не поставят, и разошлись бы МОЛЧА в день, когда её поставят.
func (c *Client) CompletionBase() time.Duration {
	if c == nil {
		return defaultTimeout
	}
	// The transport's own field — the one oaichat.post puts into CompletionBudget on every request.
	return c.chat.CompletionBase()
}

// Enabled reports whether an API key is configured. Nil-safe.
func (c *Client) Enabled() bool {
	return c != nil && c.apiKey() != ""
}

// apiKey is the key a request is built with: Config.KeyFunc when wired (read per call, so a
// rotation reaches the next request), else Config.APIKey.
func (c *Client) apiKey() string {
	if c.cfg.KeyFunc != nil {
		return strings.TrimSpace(c.cfg.KeyFunc())
	}
	return strings.TrimSpace(c.cfg.APIKey)
}

// Model returns the effective model id (for response provenance). Nil-safe.
func (c *Client) Model() string {
	if c == nil {
		return ""
	}
	return c.cfg.Model
}

// AnalysisModel returns the effective model slug of CompleteWithMeta — the tech-card analysis pass
// and EnhanceText: the optional OPENROUTER_MODEL_ANALYSIS override, or the shared slug when that
// override is unset — which is the normal state on every deployment. Nil-safe.
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

// IdeasModel returns the slug the PLAYGROUND `Ideas ▾` door (SuggestPrompts) calls first:
// OPENROUTER_MODEL_IDEAS when set, DefaultIdeasModel when unset, and "" when it is `off` (any
// case) — "" means the door is OFF, and the band then says so with an empty suggest_prompts_model.
// It does not look at the key: Enabled() is the other half of «is the door open», and the caller
// asks both. Nil-safe.
func (c *Client) IdeasModel() string {
	if c == nil {
		return ""
	}
	m := strings.TrimSpace(c.cfg.ModelIdeas)
	switch {
	case m == "":
		return DefaultIdeasModel
	case strings.EqualFold(m, IdeasModelOff):
		return ""
	}
	return m
}

const (
	sharedModelFeatures   = "note formatting, design idea drafts and campaign auto-translation"
	analysisModelFeatures = "tech-card construction analysis and EnhanceText (the ai ✦ rewrite)"
	ideasModelFeatures    = "playground Ideas suggestions"
	ideasFallbackFeatures = "playground Ideas suggestions (the fallback after a 404 on the ideas slug)"
)

// effectiveModels returns the DISTINCT slugs this client can send. It is a set on purpose: with
// OPENROUTER_MODEL_ANALYSIS unset — again, the normal state — both roles are the same string, and
// probing it twice would double the boot traffic and shout twice about a single fault.
//
// The Ideas door adds its slug AND its fallback (both baked in, both able to rot — see
// DefaultIdeasModel), unless OPENROUTER_MODEL_IDEAS=off, when neither is ever called.
func (c *Client) effectiveModels() []effectiveModel {
	if c == nil {
		return nil
	}
	out := make([]effectiveModel, 0, 4)
	add := func(slug, features string) {
		slug = strings.TrimSpace(slug)
		if slug == "" {
			return
		}
		for i := range out {
			if out[i].slug == slug {
				out[i].features += ", " + features
				return
			}
		}
		out = append(out, effectiveModel{slug: slug, features: features})
	}
	add(c.cfg.Model, sharedModelFeatures)
	add(c.AnalysisModel(), analysisModelFeatures)
	if ideas := c.IdeasModel(); ideas != "" {
		add(ideas, ideasModelFeatures)
		add(IdeasFallbackModel, ideasFallbackFeatures)
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

type apiError struct {
	Message string `json:"message"`
	Code    any    `json:"code"`
	Type    string `json:"type"`
}

// Usage is the token accounting of one completion, as the legacy methods return it (B-18 moves the
// callers to aiprov.ChatResult). The field names are the OpenAI-compatible ones OpenRouter answers
// with; the Go names are shortened because the "_tokens" suffix on a type called Usage says nothing.
//
// It is FILLED FROM THE TRANSPORT'S RESULT now (oaichat decodes the wire — and carries the warning that
// a misspelled tag there is silent), and it grew three fields (B-11, additive) now that the request
// asks OpenRouter for them (`usage:{include:true}`):
//
//	Cached     the part of Prompt served from the provider's cache;
//	Reasoning  the part of Completion spent on thinking;
//	Cost       OpenRouter's own charge in USD — NULL when absent or 0 (a 0 is not a known price).
type Usage struct {
	Prompt     int                 `json:"prompt_tokens"`
	Completion int                 `json:"completion_tokens"`
	Total      int                 `json:"total_tokens"`
	Cached     int                 `json:"cached_tokens"`
	Reasoning  int                 `json:"reasoning_tokens"`
	Cost       decimal.NullDecimal `json:"cost"`
}

// Complete runs a single chat completion and returns the assistant's raw message content. It is
// the generic primitive behind feature-specific methods (e.g. translation). jsonMode requests a
// JSON-object response from the model. Returns ErrNotConfigured when no API key is set.
//
// It is a thin wrapper over the shared path with the SHARED slug and no explicit token cap —
// exactly what it did before the response metadata existed — so callers that do not care about the
// finish reason or the token bill keep the behaviour they had.
func (c *Client) Complete(ctx context.Context, systemPrompt, userPrompt string, jsonMode bool) (string, error) {
	// No effort: the provider default, i.e. exactly what these features did before the field
	// existed. Only the analysis pass has a budget tight enough to care.
	text, _, _, err := c.complete(ctx, c.Model(), systemPrompt, userPrompt, jsonMode, 0, "")
	return text, err
}

// CompleteWithMeta runs a single chat completion for the TECH-CARD ANALYSIS pass (and for
// EnhanceText, which needs the same finish reason and token bill) and returns the assistant content
// together with what the caller needs in order to judge it.
//
// WHY THE METADATA IS NOT OPTIONAL. Without finishReason a reply truncated by the token cap is
// indistinguishable from a model that emitted broken JSON, and those two owe the human different
// sentences ("it was cut off, ask again" vs "the model misbehaved"). Without usage the price of a
// run is invisible, and a per-press LLM call whose cost nobody can see is a bill nobody notices.
//
// maxTokens caps the completion; <= 0 omits the field and leaves the provider default in force.
//
// THE SLUG IS AnalysisModel(), NOT Model(). OPENROUTER_MODEL_ANALYSIS exists to escalate the calls
// made through this method without dragging the features behind Complete onto a different model.
// With the override unset the two are the same string.
func (c *Client) CompleteWithMeta(ctx context.Context, systemPrompt, userPrompt string, jsonMode bool, maxTokens int) (text string, finishReason string, usage Usage, err error) {
	// ONE OF THE TWO CALLERS THAT SET `reasoning`, and for the same reason: see
	// analysisReasoningEffort. Мышление выключено там, где стоит ПОТОЛОК ТОКЕНОВ, — этот пас и
	// CompleteWithImages с непустым maxTokens (multimodal.go). Обратное («выключено только здесь»)
	// стояло тут до круга 19 и ровно поэтому черновик конструкции поставил потолок 3000, мышление
	// не выключил и купил бы размышление вместо ответа.
	return c.complete(ctx, c.AnalysisModel(), systemPrompt, userPrompt, jsonMode, maxTokens,
		analysisReasoningEffort)
}

// complete is the shared body of Complete and CompleteWithMeta: one place that decides the request
// shape, so the slug, the JSON-mode flag and the effort cannot drift between the two entry points.
// The model is a parameter precisely because the two differ in that one value. effort "" = the
// provider's default (no `reasoning` key on the wire).
func (c *Client) complete(ctx context.Context, model, systemPrompt, userPrompt string, jsonMode bool, maxTokens int, effort string) (string, string, Usage, error) {
	if !c.Enabled() {
		return "", "", Usage{}, ErrNotConfigured
	}
	return c.send(ctx, model, aiprov.ChatRequest{
		System:    systemPrompt,
		User:      userPrompt,
		JSONMode:  jsonMode,
		MaxTokens: maxTokens, // <= 0 is omitted by the transport: the provider default stands
		Effort:    effort,
	}, oaichat.Options{})
}

// send is THE ONE DOOR of this package to the chat transport: Complete,
// CompleteWithMeta and both picture entry points (multimodal.go) funnel through it, so the legacy
// (text, finishReason, Usage, error) shape is assembled in exactly one place.
//
// On an empty answer the transport returns the partial result WITH the error, and both ride out here
// as they always did: the finish reason and the usage of a paid call that printed nothing. The
// temperature (0.2), the wire shape, the budget and the error sentences are the transport's.
func (c *Client) send(ctx context.Context, model string, req aiprov.ChatRequest, opt oaichat.Options) (string, string, Usage, error) {
	reply, err := c.chat.Send(ctx, model, req, opt)
	if reply == nil {
		return "", "", Usage{}, err
	}
	usage := Usage{
		Prompt:     reply.Usage.Prompt,
		Completion: reply.Usage.Completion,
		Total:      reply.TotalTokens,
		Cached:     reply.Usage.Cached,
		Reasoning:  reply.Usage.Reasoning,
		Cost:       reply.CostUSD,
	}
	if err != nil {
		return "", reply.FinishReason, usage, err
	}
	return reply.Text, reply.FinishReason, usage, nil
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
	// СВОЙ КОРОТКИЙ СРОК, И ОН ЖИВЁТ ЗДЕСЬ, А НЕ У ВЫЗЫВАЮЩЕГО. Раньше зонд был ограничен дважды:
	// собственным сроком в WarnIfModelRetired и общим http.Client.Timeout. Второго больше нет (см.
	// New), поэтому срок переехал в тело: экспортированный CheckModel зовут и с context.Background(),
	// и зонд без срока — это боот, повисший на молчащем поставщике. Три секунды — то самое число,
	// что стояло снаружи: зонд это любезность, и медленный ответ на нём не новость (modelProbeTimeout).
	ctx, cancel := context.WithTimeout(ctx, modelProbeTimeout)
	defer cancel()

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
		return fmt.Errorf("openrouter: %w: %q is not a model the provider knows", ErrModelUnavailable, model)
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
		return fmt.Errorf("openrouter: %w: %q has no live endpoints at the provider", ErrModelUnavailable, model)
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
