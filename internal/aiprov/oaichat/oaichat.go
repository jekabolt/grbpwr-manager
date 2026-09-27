// Package oaichat is THE transport for every OpenAI-shaped chat completions API this stack talks to:
// OpenRouter today, OpenAI and apibost once they are wired (commit E). One POST to
// {base}/chat/completions, spoken in two dialects that differ in a handful of request keys and one
// response field — and in NOTHING that decides money or time.
//
// WHY ONE TRANSPORT AND NOT ONE PER PROVIDER. Everything that has ever gone wrong on this path went
// wrong in the transport, not in the prompt: a call budget that did not grow with the answer ceiling
// (NULL prices for answers that were cut off), an engaged flag that invented or hid money, a read
// ceiling that trimmed an answer in silence, a retired slug reported as weather. Each of those fixes
// lives here exactly once. A second copy for the next OpenAI-compatible provider would be a copy in
// which each of them can be forgotten — and the router's fallback decision is only as honest as the
// flags the transport sets.
//
// WHAT IT GUARDS (moved from internal/openrouter: postChatCompletion, B-11):
//
//   - the TIME BUDGET of every request — aiprov.CompletionBudget(HTTPTimeout, max_tokens), set per
//     request from the ceiling that goes on the wire, never one number per client;
//   - the ENGAGED boundary — aiprov.ObserveWrite (httptrace), and the table below of which failure
//     falls on which side;
//   - the READ CEILING — MaxResponseBytes, refused by name, never trimmed;
//   - the STATUS CLASSIFICATION — by status alone, never by the provider's prose (404 is a setting,
//     429/5xx are weather, 401/402/403 are refusals at the gate);
//   - the SENTENCES — "<provider>: API error (HTTP 502): …" is what logs, people and (until B-18) one
//     regex read; the sentinels travel inside them for errors.Is.
//
// Every failure is an *aiprov.CallError; its Error() is the sentence, its fields are the facts.
package oaichat

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/shopspring/decimal"

	"github.com/jekabolt/grbpwr-manager/internal/aiprov"
)

// Dialect is the spelling of the OpenAI chat wire a provider speaks. The zero value is DialectOpenAI:
// the plain shape, which is what an "OpenAI-compatible" endpoint promises and the lowest common
// denominator of the two.
type Dialect string

const (
	// DialectOpenRouter — `max_tokens`, `reasoning:{effort}`, `usage:{include:true}` (the provider's
	// charge comes back as usage.cost, in USD), and the X-Title / HTTP-Referer attribution headers.
	DialectOpenRouter Dialect = "openrouter"
	// DialectOpenAI — `max_completion_tokens`, `reasoning_effort`, no usage object, no attribution
	// headers. apibost speaks it too.
	DialectOpenAI Dialect = "openai"
)

const (
	// MaxImageParts caps how many pictures one request may carry.
	//
	// It is a guard on OUR side, not the provider's limit. A moodboard is a user-controlled list, and
	// an unbounded one turns a single button press into a prompt of arbitrary size and arbitrary cost —
	// images are billed as input tokens, so "how many" is "how much". openrouter.MaxImageParts is this
	// number (the DraftDesignIdea door refuses with it BEFORE StartRun, so the door and the transport
	// can never disagree), and it is deliberately NOT tied to orimages.MaxInputReferences: that is a
	// different limit of a different endpoint, and today's equality is a coincidence.
	MaxImageParts = 16

	// MaxResponseBytes caps how much of a response is read. Everything this transport reads is text:
	// a completion this big is already an order of magnitude past any prompt here, and raising it
	// would only buy a bigger allocation on a 0.5 GiB box. The cap REFUSES (aiprov.ErrResponseTooLarge)
	// rather than trims — see readCapped. Generated pictures never come through here (internal/orimages
	// carries its own, much larger ceiling for exactly that reason).
	MaxResponseBytes = 4 << 20 // 4 MiB

	// temperature keeps answers fairly deterministic and consistent; the same on both dialects.
	temperature = 0.2
)

// Config configures one transport for ONE provider account.
//
//	Provider     the billing provider key (entity.AIProviderOpenRouter, …): CallError.Provider,
//	             ChatResult.Provider and the "<provider>: " prefix of every sentence;
//	BaseURL      the API root; "/chat/completions" is appended;
//	Dialect      see Dialect;
//	KeyFunc      asked for the key on EVERY request and by Enabled() — the registry's hook, so a key
//	             saved in the admin panel reaches the next request; "" (or a nil func) = disabled.
//	             Never serialised, never printed;
//	HTTPTimeout  the BASE of every request's budget (aiprov.CompletionBudget); <= 0 = the default;
//	Title,
//	Referer      OpenRouter's attribution headers (X-Title, HTTP-Referer); sent only in that dialect
//	             and only when set.
type Config struct {
	Provider    string
	BaseURL     string
	Dialect     Dialect
	KeyFunc     func() string
	HTTPTimeout time.Duration
	Title       string
	Referer     string
}

// Client is one configured chat transport. A nil *Client is valid and permanently disabled.
type Client struct {
	cfg Config
	// budgetBase — БАЗА бюджета одного вызова: всё, что не есть печать ответа. Печать добавляется
	// поверх, по потолку токенов ЭТОГО запроса (aiprov.CompletionBudget).
	budgetBase time.Duration
	// ⚠ У http.Client НЕТ СОБСТВЕННОГО Timeout, И ЭТО ОБЯЗАТЕЛЬНО. http.Client.Timeout — одно число на
	// все запросы, поставленное до того, как стал известен размер ответа; именно оно и обрывало
	// вызов, у которого потолок был поднят. Срок ставится НА КАЖДЫЙ ЗАПРОС в post.
	http *http.Client
}

var _ aiprov.Chatter = (*Client)(nil)

// New builds a transport. It validates nothing: a missing key just leaves it disabled.
func New(cfg Config) *Client {
	base := cfg.HTTPTimeout
	if base <= 0 {
		base = aiprov.DefaultBudgetBase
	}
	return &Client{cfg: cfg, budgetBase: base, http: &http.Client{}}
}

// Enabled reports whether a key is configured right now. Nil-safe.
func (c *Client) Enabled() bool {
	return c != nil && c.key() != ""
}

// BaseURL returns the configured API root, for log lines (a 404 may mean the base URL, not the slug).
// Nil-safe.
func (c *Client) BaseURL() string {
	if c == nil {
		return ""
	}
	return c.cfg.BaseURL
}

// CompletionBase is the base this transport puts into aiprov.CompletionBudget on EVERY request. A
// lease that has to outlive a call must be derived from THIS number (openrouter.CompletionBase
// delegates here), so the lease and the wire read one field of one object and cannot drift. Nil-safe.
func (c *Client) CompletionBase() time.Duration {
	if c == nil || c.budgetBase <= 0 {
		return aiprov.DefaultBudgetBase
	}
	return c.budgetBase
}

func (c *Client) key() string {
	if c == nil || c.cfg.KeyFunc == nil {
		return ""
	}
	return strings.TrimSpace(c.cfg.KeyFunc())
}

// provider is the sentence prefix and the CallError.Provider. Nil-safe.
func (c *Client) provider() string {
	if c == nil || c.cfg.Provider == "" {
		return "ai"
	}
	return c.cfg.Provider
}

// Options are the knobs a LEGACY caller still needs and the provider-neutral ChatRequest deliberately
// does not have. Chat uses the zero value.
type Options struct {
	// PartsAlways sends the user turn as a list of parts EVEN WITH NO PICTURES —
	// `[{"type":"text","text":…}]` instead of a plain string. openrouter.CompleteWithImages has always
	// sent that shape for an empty moodboard, and its golden (multimodal_test.go) pins those bytes; a
	// ChatRequest with no ImageURLs is, by the seam's own contract, a plain-string text turn.
	PartsAlways bool
}

// Reply is one answered call: the provider-neutral result plus the one wire number a legacy caller
// still reads (openrouter.Usage.Total, logged as total_tokens) and TokenUsage deliberately lacks.
type Reply struct {
	aiprov.ChatResult
	// TotalTokens is usage.total_tokens as the provider reported it (0 when absent).
	TotalTokens int
}

// Chat implements aiprov.Chatter: one completion of req on model.
//
// ⚠ ON AN ENGAGED FAILURE WHOSE ANSWER DID ARRIVE — an empty message, ErrBudgetExhausted — Chat
// returns BOTH the partial result (Usage, FinishReason, Model, RequestID, CostUSD; Text "") AND the
// error. The call was paid for and its size is known; dropping the usage with the answer is how a spend
// vanished from the log before. Every other failure returns a nil result.
func (c *Client) Chat(ctx context.Context, model string, req aiprov.ChatRequest) (*aiprov.ChatResult, error) {
	reply, err := c.Send(ctx, model, req, Options{})
	if reply == nil {
		return nil, err
	}
	return &reply.ChatResult, err
}

// Send is Chat with Options and the wire-level Reply. Same failure contract as Chat.
func (c *Client) Send(ctx context.Context, model string, req aiprov.ChatRequest, opt Options) (*Reply, error) {
	// ONE KEY PER REQUEST (Codex C review, P3): the key is read exactly once here and travels with the
	// request; a rotation or a switch-off that lands mid-flight changes the NEXT request, never the
	// Authorization header of this one.
	key := c.key()
	if key == "" {
		return nil, c.fail(aiprov.CodeNotConfigured, 0, false, false,
			fmt.Errorf("%s: no API key is set: %w", c.provider(), aiprov.ErrNotConfigured))
	}
	if strings.TrimSpace(model) == "" {
		// "" would reach the provider as a 400 that reads as the provider's fault rather than ours.
		return nil, c.fail(aiprov.CodeBadRequest, 0, false, false,
			fmt.Errorf("%s: a completion needs a model slug", c.provider()))
	}
	payload, ceiling, err := c.buildRequest(model, req, opt)
	if err != nil {
		return nil, c.fail(aiprov.CodeBadRequest, 0, false, false, err)
	}
	return c.post(ctx, model, payload, ceiling, key)
}

// ─── request ─────────────────────────────────────────────────────────────────────────────────────

// textMessage is a turn whose content is a STRING. The system turn is always one, and so is the user
// turn of a request without pictures: a prompt that already works keeps producing identical bytes.
type textMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// partsMessage is a turn whose content is a LIST OF PARTS — the user turn of a picture prompt:
//
//	{"role":"user","content":[
//	   {"type":"text","text":"..."},
//	   {"type":"image_url","image_url":{"url":"https://… | data:image/png;base64,…"}}]}
//
// It is a type of its own, and Messages is []any, precisely so textMessage.Content can stay a string:
// retyping that field to `any` compiles everywhere and turns every text feature into a runtime shape
// nobody checks (the compile-time guard lives in oaichat_test.go).
type partsMessage struct {
	Role    string        `json:"role"`
	Content []contentPart `json:"content"`
}

// contentPart is one member of a parts list; exactly one of Text / ImageURL is set, by Type.
type contentPart struct {
	Type     string        `json:"type"`
	Text     string        `json:"text,omitempty"`
	ImageURL *imagePartURL `json:"image_url,omitempty"`
}

// imagePartURL is the nested object the wire format wants: {"image_url":{"url":"…"}}. Flattening it
// is a 400.
type imagePartURL struct {
	URL string `json:"url"`
}

type responseFormat struct {
	Type string `json:"type"`
}

// reasoningSpec is OpenRouter's `reasoning` object. Only effort is ever set (the provider documents
// effort and max_tokens as alternatives, not as a pair).
type reasoningSpec struct {
	Effort string `json:"effort,omitempty"`
}

// usageAccounting is OpenRouter's `usage:{include:true}`: ask for the charge (usage.cost, USD) and the
// cached / reasoning token details in the same answer.
type usageAccounting struct {
	Include bool `json:"include"`
}

// openRouterRequest — ⚠ THE FIELD ORDER IS THE BYTE ORDER, AND IT IS PINNED. Up to Reasoning it is
// field for field what openrouter.chatRequest / multimodalRequest sent before the move (goldens in
// internal/openrouter/multimodal_test.go); Usage is LAST on purpose, so the only difference on the wire
// is one inserted key.
type openRouterRequest struct {
	Model    string `json:"model"`
	Messages []any  `json:"messages"`
	// MaxTokens caps the COMPLETION (reasoning included). Omitted when zero: the provider's own default
	// stands, which is what every caller without a ceiling had before the field existed.
	MaxTokens      int              `json:"max_tokens,omitempty"`
	Temperature    float64          `json:"temperature"`
	ResponseFormat *responseFormat  `json:"response_format,omitempty"`
	Reasoning      *reasoningSpec   `json:"reasoning,omitempty"`
	Usage          *usageAccounting `json:"usage,omitempty"`
}

// openAIRequest is the plain OpenAI chat body.
//
// max_completion_tokens, NOT max_tokens — source: the official SDK's request params, generated from the
// API spec (github.com/openai/openai-python, src/openai/types/chat/completion_create_params.py, read
// 2026-09-27): max_tokens "is now deprecated in favor of `max_completion_tokens`, and is not compatible
// with o-series models"; max_completion_tokens is "an upper bound … including visible output tokens and
// reasoning tokens". The same file lists reasoning_effort values none | minimal | low | medium | high |
// xhigh | max, "not all reasoning models support every value" — the word is passed through as asked.
type openAIRequest struct {
	Model               string          `json:"model"`
	Messages            []any           `json:"messages"`
	MaxCompletionTokens int             `json:"max_completion_tokens,omitempty"`
	Temperature         float64         `json:"temperature"`
	ResponseFormat      *responseFormat `json:"response_format,omitempty"`
	ReasoningEffort     string          `json:"reasoning_effort,omitempty"`
}

// buildRequest marshals req in this client's dialect and returns the ceiling that went on the wire —
// the SAME number the budget is computed from (post), so the two cannot drift apart.
func (c *Client) buildRequest(model string, req aiprov.ChatRequest, opt Options) ([]byte, int, error) {
	p := c.provider()
	var user any = textMessage{Role: "user", Content: req.User}
	parts := len(req.ImageURLs) > 0 || opt.PartsAlways
	if parts {
		if strings.TrimSpace(req.User) == "" {
			return nil, 0, fmt.Errorf("%s: a multimodal request needs a prompt, pictures alone say nothing", p)
		}
		content, err := buildContentParts(p, req.User, req.ImageURLs)
		if err != nil {
			return nil, 0, err
		}
		user = partsMessage{Role: "user", Content: content}
	}
	messages := []any{textMessage{Role: "system", Content: req.System}, user}

	ceiling := 0
	if req.MaxTokens > 0 {
		ceiling = req.MaxTokens
	}
	var format *responseFormat
	if req.JSONMode {
		format = &responseFormat{Type: "json_object"}
	}

	var body any
	switch c.cfg.Dialect {
	case DialectOpenRouter:
		r := openRouterRequest{
			Model:          model,
			Messages:       messages,
			MaxTokens:      ceiling,
			Temperature:    temperature,
			ResponseFormat: format,
			Usage:          &usageAccounting{Include: true},
		}
		if req.Effort != "" {
			r.Reasoning = &reasoningSpec{Effort: req.Effort}
		}
		body = r
	default:
		body = openAIRequest{
			Model:               model,
			Messages:            messages,
			MaxCompletionTokens: ceiling,
			Temperature:         temperature,
			ResponseFormat:      format,
			ReasoningEffort:     req.Effort,
		}
	}
	payload, err := json.Marshal(body)
	if err != nil {
		what := "request"
		if parts {
			what = "multimodal request"
		}
		return nil, 0, fmt.Errorf("%s: marshal %s: %w", p, what, err)
	}
	return payload, ceiling, nil
}

// buildContentParts turns a prompt plus picture addresses into the content array, refusing anything it
// cannot vouch for. The text part comes FIRST on purpose: it is the instruction, and a model reading
// instructions after sixteen pictures is a model that has already started guessing.
func buildContentParts(provider, userPrompt string, imageURLs []string) ([]contentPart, error) {
	if len(imageURLs) > MaxImageParts {
		return nil, fmt.Errorf("%s: %d pictures exceeds the %d-picture limit for one request",
			provider, len(imageURLs), MaxImageParts)
	}
	parts := make([]contentPart, 0, len(imageURLs)+1)
	parts = append(parts, contentPart{Type: "text", Text: userPrompt})
	for i, raw := range imageURLs {
		u := strings.TrimSpace(raw)
		if err := validateImageURL(u); err != nil {
			return nil, fmt.Errorf("%s: picture %d: %w", provider, i+1, err)
		}
		parts = append(parts, contentPart{Type: "image_url", ImageURL: &imagePartURL{URL: u}})
	}
	return parts, nil
}

// validateImageURL admits exactly two forms and rejects the rest.
//
// THE REJECTION IS THE POINT. The provider fetches whatever address we hand it, from its own network —
// so an unvalidated string here is an outbound fetch we authored on someone else's behalf. `file://`,
// `gopher://` and friends have no business in a picture list, and an empty string would become a 400
// that reads as a provider fault rather than as our own missing value.
func validateImageURL(u string) error {
	switch {
	case u == "":
		return fmt.Errorf("empty picture address")
	case strings.HasPrefix(u, "https://"), strings.HasPrefix(u, "http://"):
		return nil
	case strings.HasPrefix(u, "data:image/"):
		// A data URI must actually carry the payload; "data:image/png" alone is a 400 waiting to be
		// blamed on the provider.
		if !strings.Contains(u, ",") {
			return fmt.Errorf("data URI carries no payload")
		}
		return nil
	default:
		return fmt.Errorf("picture address must be http(s):// or a data:image/… URI, got %q", truncate(u, 40))
	}
}

// ─── the wire ────────────────────────────────────────────────────────────────────────────────────
//
// ───────────────── ГРАНИЦА «КУПЛЕНО / НЕ КУПЛЕНО» (CallError.Engaged) ─────────────────
//
// Engaged отвечает на ОДИН вопрос: успел ли запрос доехать до поставщика ДО того, как всё сломалось.
// Ответ — не диагностика, а ДЕНЬГИ: пока его не было, вызывающий (design_run.go: designFailDraft)
// закрывал ЛЮБОЙ провал вызова ценой NULL, и поставщик, уже напечатавший 22k входных токенов на доске
// из двенадцати кадров, попадал в регистр нулём. Человек видел codes.Unavailable — новость,
// неотличимую от погоды, — жал ещё раз и покупал тот же ноль второй раз. Для роутера тот же флаг
// решает, можно ли звать следующего кандидата: после вовлечённого провала — нельзя, это вторая оплата
// одного ответа (02-PLAN A2). Граница найдена на проводе — см. aiprov.ObserveWrite.
//
// ЧТО ПО КАКУЮ СТОРОНУ ГРАНИЦЫ ОКАЗАЛОСЬ:
//
//	НЕ ВОВЛЕЧЁН (NULL в регистре — правда):
//	  · ErrNotConfigured, пустой слуг, пустой промпт, отказ сборки частей, marshal, build request — до провода;
//	  · DNS, отказ в соединении, TLS, оборванная ЗАПИСЬ запроса — WroteRequest не сработал;
//	  · ЛЮБОЙ non-2xx, включая 404/ErrModelUnavailable, 401, 402, 429, 5xx. Это поставщик, ОТКАЗАВШИЙ
//	    НА ВОРОТАХ, а отказ не тарифицируется. Здесь и только здесь граница выбрана НЕ в сторону
//	    «записать»: протухший слуг модели отвечает 404 за 0.2 с на КАЖДОЕ нажатие, и пометить это
//	    тратой значило бы выдумать деньги ровно в тот день, когда фича целиком мертва.
//
//	ВОВЛЕЧЁН (деньги ушли, сумму знает вызывающий):
//	  · запрос дописан, а дальше срок вышел / контекст отменён / край разорвал соединение —
//	    поставщик считает прямо сейчас, а мы не узнаем, чем он кончил;
//	  · тело ответа не дочиталось или переросло потолок (ErrResponseTooLarge) — ответ БЫЛ;
//	  · 2xx, у которого конверт не разобрался, пуст, без choices или с error внутри — поставщик
//	    принял запрос и отработал его.
//
// RETRYABLE (CallError.Retryable — корм для предохранителя реестра) — только то, что тот же запрос
// может пережить позже И за что никто не заплатил: транспортная ошибка или срок ДО записи, 408, 429,
// 5xx. Не повторяемо: 400/401/402/403/404/422, отмена вызывающим (закрытая вкладка — не вина
// поставщика) и ЛЮБОЙ вовлечённый исход.

// chatResponse is the envelope. The fields added by B-11 (id, usage.cost, the token details) are read
// LENIENTLY — as raw JSON, parsed best-effort — so a provider that spells one of them unexpectedly
// costs that one number, never the answer: a decode failure here is an engaged, paid-for fault.
type chatResponse struct {
	ID      json.RawMessage `json:"id"`
	Model   string          `json:"model"`
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage *wireUsage `json:"usage"`
	Error *apiError  `json:"error"`
}

// wireUsage is the usage block. ⚠ A MISSING OR MISSPELLED TAG HERE IS SILENT: every count stays zero,
// the call still succeeds, and the only symptom is a run that reads as free — the tests assert
// NON-ZERO numbers for exactly that reason.
type wireUsage struct {
	PromptTokens            int             `json:"prompt_tokens"`
	CompletionTokens        int             `json:"completion_tokens"`
	TotalTokens             int             `json:"total_tokens"`
	Cost                    json.RawMessage `json:"cost"`
	PromptTokensDetails     json.RawMessage `json:"prompt_tokens_details"`
	CompletionTokensDetails json.RawMessage `json:"completion_tokens_details"`
}

type apiError struct {
	Message string `json:"message"`
	Code    any    `json:"code"`
	Type    string `json:"type"`
}

// post is THE ONLY PLACE THE STACK TALKS TO /chat/completions. ceiling is the max_tokens that went on
// the wire (buildRequest returns it), and the call's deadline is computed from it and nowhere else.
func (c *Client) post(ctx context.Context, model string, payload []byte, ceiling int, key string) (*Reply, error) {
	p := c.provider()
	// СРОК СТАВИТСЯ НА КАЖДЫЙ ЗАПРОС, А НЕ НА КЛИЕНТА: он зависит от того, сколько токенов у этого
	// запроса попрошено, и никакое одно число на всех этого выразить не может. Срок вызывающего
	// по-прежнему сильнее — context.WithTimeout не удлиняет чужой дедлайн, только укорачивает.
	ctx, cancel := context.WithTimeout(ctx, aiprov.CompletionBudget(c.budgetBase, ceiling))
	defer cancel()
	ctx, wroteRequest := aiprov.ObserveWrite(ctx)

	endpoint := strings.TrimRight(c.cfg.BaseURL, "/") + "/chat/completions"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, c.fail(aiprov.CodeBadRequest, 0, false, false, fmt.Errorf("%s: build request: %w", p, err))
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+key)
	if c.cfg.Dialect == DialectOpenRouter {
		if c.cfg.Title != "" {
			httpReq.Header.Set("X-Title", c.cfg.Title)
		}
		if c.cfg.Referer != "" {
			httpReq.Header.Set("HTTP-Referer", c.cfg.Referer)
		}
	}

	resp, err := c.http.Do(httpReq)
	if err != nil {
		// ТОТ ЖЕ ТЕКСТ ОШИБКИ ПО ОБЕ СТОРОНЫ ГРАНИЦЫ, РАЗНАЯ ТОЛЬКО ПОМЕТКА. Срок вышел на 143-й секунде
		// ожидания ответа и «connection refused» на 0-й приезжают из Do неотличимо похоже — оба как
		// *url.Error, — и решает между ними НЕ проза, а флаг записи.
		engaged := wroteRequest()
		code := interruption(ctx, err)
		retryable := !engaged && code != aiprov.CodeCanceled
		return nil, c.fail(code, 0, engaged, retryable, fmt.Errorf("%s: request failed: %w", p, err))
	}
	defer resp.Body.Close()

	// ОТВЕТ УЖЕ ЕДЕТ: заголовки пришли, значит поставщик отработал. Не дочитать его — наша беда, а не
	// его бесплатность.
	body, err := readCapped(resp.Body, MaxResponseBytes, "chat/completions response", p)
	// THE STATUS IS JUDGED BEFORE THE BODY'S FATE (Codex C review, P2). A non-2xx is the provider's
	// refusal at the gate — no money moved, whatever happened to the error body afterwards (cut,
	// oversized, timed out): it stays a not-engaged refusal with its own code and its 404 sentinel, so
	// the router can still fall back (D-09). The body, when it did arrive, only lends the sentence.
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		if err != nil {
			// readCapped hands back no body on a failed read, and a nil body reads as "" — the sentence
			// would end in a dangling «API error (HTTP 404): » that hides WHY there is no excuse
			// (REVIEW-FIXD P3 #5). apiErrorMessage takes this non-JSON text as the message, bounded.
			body = []byte("response body unavailable: " + err.Error())
		}
		return nil, c.statusError(resp.StatusCode, body)
	}
	if err != nil {
		code := aiprov.CodeTooLarge
		if !errors.Is(err, aiprov.ErrResponseTooLarge) {
			code = interruption(ctx, err)
		}
		return nil, c.fail(code, resp.StatusCode, true, false, fmt.Errorf("%s: read response: %w", p, err))
	}

	// ─── ОТСЮДА И НИЖЕ 2xx: ЗАПРОС ПРИНЯТ И ОТРАБОТАН, ЗНАЧИТ ОПЛАЧЕН ───
	status := resp.StatusCode
	var cr chatResponse
	if err := json.Unmarshal(body, &cr); err != nil {
		return nil, c.fail(aiprov.CodeProviderError, status, true, false,
			fmt.Errorf("%s: could not decode API response envelope: %w", p, err))
	}
	if cr.Error != nil && strings.TrimSpace(cr.Error.Message) != "" {
		return nil, c.fail(aiprov.CodeProviderError, status, true, false,
			fmt.Errorf("%s: API error: %s", p, cr.Error.Message))
	}
	if len(cr.Choices) == 0 {
		return nil, c.fail(aiprov.CodeEmptyAnswer, status, true, false,
			fmt.Errorf("%s: API response contained no choices", p))
	}
	reply := c.reply(model, &cr)
	if reply.Text == "" {
		// The usage still rides along (see Chat): an empty message is not a free call.
		//
		// EMPTY-BECAUSE-THE-BUDGET-RAN-OUT IS ITS OWN FAULT. finish_reason=length with no content says
		// the cap was reached before a single character of answer — deterministic, and the caller owes
		// the human "the setting is wrong", not "try again". Empty for any other reason is a
		// misbehaving provider.
		if strings.EqualFold(strings.TrimSpace(reply.FinishReason), "length") {
			return reply, c.fail(aiprov.CodeBudgetExhausted, status, true, false, fmt.Errorf(
				"%s: %w (%d completion tokens spent, none of them answer)", p, aiprov.ErrBudgetExhausted, reply.Usage.Completion))
		}
		return reply, c.fail(aiprov.CodeEmptyAnswer, status, true, false,
			fmt.Errorf("%s: model returned an empty message", p))
	}
	return reply, nil
}

// statusError classifies a non-2xx answer BY STATUS ALONE — no substring of the provider's English is
// matched, so a reworded message cannot reclassify the fault. None of these is engaged: a refusal at
// the gate is not billed.
//
// The SENTENCES are the ones the openrouter client wrote before the move: a 404 opens with the
// ErrModelUnavailable sentence (a setting, not weather — "the configured model is not available at the
// provider: API error (HTTP 404): …"); every other status is the bare "<provider>: API error (HTTP n):
// <provider text>", which techcard_ai_enhance.go's providerHTTPStatusRe reads from the START of the
// string until B-18 moves it onto the fields. The provider's text only ever follows that colon.
func (c *Client) statusError(status int, body []byte) error {
	p := c.provider()
	code, retryable := classifyStatus(status)
	var err error
	if status == http.StatusNotFound {
		err = fmt.Errorf("%s: %w: API error (HTTP %d): %s", p, aiprov.ErrModelUnavailable, status, apiErrorMessage(body))
	} else {
		err = fmt.Errorf("%s: API error (HTTP %d): %s", p, status, apiErrorMessage(body))
	}
	return c.fail(code, status, false, retryable, err)
}

// classifyStatus is the status → (Code, Retryable) table, one row per case on purpose (each row has a
// test and a measured mutation). Retryable = the SAME request may succeed later and nobody paid for
// this one: 408, 429, 5xx. Every other 4xx is the provider refusing the request WE built, or our key or
// balance — re-sending it cannot end differently (orimages.classifyStatus learnt that the expensive way).
func classifyStatus(status int) (code string, retryable bool) {
	switch {
	case status == http.StatusUnauthorized:
		return aiprov.CodeKeyRejected, false
	case status == http.StatusForbidden:
		return aiprov.CodeKeyRejected, false
	case status == http.StatusPaymentRequired:
		return aiprov.CodeOutOfCredits, false
	case status == http.StatusNotFound:
		return aiprov.CodeModelUnknown, false
	case status == http.StatusRequestTimeout:
		// Named before the generic 4xx row, or that row takes it and a transient timeout becomes a
		// terminal refusal from the first attempt.
		return aiprov.CodeProviderError, true
	case status == http.StatusTooManyRequests:
		return aiprov.CodeRateLimited, true
	case status >= 500:
		return aiprov.CodeProviderError, true
	case status >= 400:
		// 400, 422 and any 4xx not named above: the request we built.
		return aiprov.CodeBadRequest, false
	default:
		// A 1xx/3xx that reached us unfollowed: not a refusal we can name, not weather we can bet on.
		return aiprov.CodeProviderError, false
	}
}

// interruption names a round trip that did not complete: our budget or the caller's deadline
// (timeout), the caller leaving (canceled — never the provider's fault, never fed to the breaker), or
// anything else on the wire (transport). Decided by typed errors and the context, not by prose.
func interruption(ctx context.Context, err error) string {
	switch {
	case errors.Is(err, context.DeadlineExceeded), errors.Is(ctx.Err(), context.DeadlineExceeded):
		return aiprov.CodeTimeout
	case errors.Is(err, context.Canceled), errors.Is(ctx.Err(), context.Canceled):
		return aiprov.CodeCanceled
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return aiprov.CodeTimeout
	}
	return aiprov.CodeTransport
}

func (c *Client) fail(code string, status int, engaged, retryable bool, err error) *aiprov.CallError {
	return &aiprov.CallError{
		Provider:   c.providerKey(),
		Code:       code,
		HTTPStatus: status,
		Engaged:    engaged,
		Retryable:  retryable,
		Err:        err,
	}
}

// providerKey is Config.Provider as configured ("" stays ""), nil-safe — the billing key never
// defaults to a word that is not a provider.
func (c *Client) providerKey() string {
	if c == nil {
		return ""
	}
	return c.cfg.Provider
}

// reply turns an envelope with at least one choice into a Reply. Engaged is always true: an answer
// came back, so the request was written and served.
func (c *Client) reply(model string, cr *chatResponse) *Reply {
	choice := cr.Choices[0]
	r := &Reply{ChatResult: aiprov.ChatResult{
		Text:         strings.TrimSpace(choice.Message.Content),
		FinishReason: choice.FinishReason,
		Provider:     c.cfg.Provider,
		Model:        model,
		Engaged:      true,
	}}
	if m := strings.TrimSpace(cr.Model); m != "" {
		r.Model = m
	}
	var id string
	if json.Unmarshal(cr.ID, &id) == nil {
		r.RequestID = strings.TrimSpace(id)
	}
	if u := cr.Usage; u != nil {
		r.Usage = aiprov.TokenUsage{Prompt: u.PromptTokens, Completion: u.CompletionTokens}
		var cached struct {
			CachedTokens int `json:"cached_tokens"`
		}
		if json.Unmarshal(u.PromptTokensDetails, &cached) == nil {
			r.Usage.Cached = cached.CachedTokens
		}
		var reasoning struct {
			ReasoningTokens int `json:"reasoning_tokens"`
		}
		if json.Unmarshal(u.CompletionTokensDetails, &reasoning) == nil {
			r.Usage.Reasoning = reasoning.ReasoningTokens
		}
		r.TotalTokens = u.TotalTokens
		// usage.cost is OpenRouter's charge in USD. Another OpenAI-shaped provider that sends a "cost"
		// says nothing about its unit (credits? cents?), and a wrong number in the ledger is worse than
		// NULL — so it is read in the OpenRouter dialect only.
		if c.cfg.Dialect == DialectOpenRouter {
			r.CostUSD = parseCost(u.Cost)
		}
	}
	return r
}

// parseCost reads usage.cost into a KNOWN price, or NULL. ⚠ 0 IS NOT A KNOWN PRICE (the ledger's
// rule): a provider that reports 0 for a call it served has not told us the call was free, and a zero
// would read in the report as exactly that. Absent, null, unparsable, zero or negative → NULL.
func parseCost(raw json.RawMessage) decimal.NullDecimal {
	s := strings.Trim(strings.TrimSpace(string(raw)), `"`)
	if s == "" || s == "null" {
		return decimal.NullDecimal{}
	}
	d, err := decimal.NewFromString(s)
	if err != nil || !d.IsPositive() {
		return decimal.NullDecimal{}
	}
	return decimal.NullDecimal{Decimal: d, Valid: true}
}

// readCapped reads at most limit bytes and REFUSES anything longer instead of handing back a prefix.
// It reads limit+1 on purpose: that one extra byte is the whole difference between "exactly at the
// ceiling" and "over it". A prefix would fail to unmarshal as "unexpected end of JSON input" — a
// sentence that blames the provider for our knife — or, worse, parse, and be accepted as a complete
// answer.
func readCapped(r io.Reader, limit int64, what, provider string) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > limit {
		return nil, fmt.Errorf("%s: %w: %s is larger than %d bytes", provider, aiprov.ErrResponseTooLarge, what, limit)
	}
	return body, nil
}

// apiErrorMessage best-effort pulls a human message out of an error body, falling back to the raw
// (truncated) body when it is not the expected shape.
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
