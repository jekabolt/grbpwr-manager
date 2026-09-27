// Package probe checks an AI provider key against a FREE endpoint of that provider — list-models,
// balance, account, a cost report — and says what the answer means: the key works, the key is
// refused, the account is empty, or the provider could not be asked (02-PLAN rev.1 A5, B-17).
//
// ⚠ A PROBE NEVER CALLS A COMPLETION OR A GENERATION ENDPOINT. Saving a key must not cost money, and
// a "check" button pressed ten times must not buy ten answers. Every URL a probe can reach is in the
// endpoints table below, and the tests walk that table: a path that looks like a paid call
// (chat/completions, /messages, generate, generations) fails them, with ONE named exception —
// runblob's STATUS read of a generation that cannot exist (the zero uuid), which buys nothing.
//
// ⚠ THE BASE URLS ARE CONSTANTS, AND THERE IS NO OVERRIDE — no base_url field, no env variable, no
// argument. A probe sends a secret to the host it names; a host that an admin form, a database row or
// an environment could change is a way to post every stored key to an address of somebody's choosing
// (and a server-side request to wherever that is). Tests reach their httptest servers through the
// http.Client they pass, never through a knob in this package.
//
// ⚠ NO KEY MATERIAL LEAVES THIS PACKAGE. Result.Message is built from this package's own sentences,
// a status code and — for an unexplained 4xx only — the provider's own sentence with anything that
// could be a key scrubbed out (some providers quote the key they refused, masked or not).
package probe

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/shopspring/decimal"
)

// Codes of Result.Code — the provider badge's words; "" when none fits (the key may be fine and the
// probe itself was refused, or no probe exists).
const (
	// CodeKeyRejected — the provider refused the key (401/403), or there is no usable key to send.
	CodeKeyRejected = "key_rejected"
	// CodeOutOfCredits — the key is good and the account has nothing left to spend (402).
	CodeOutOfCredits = "out_of_credits"
	// CodeUnreachable — no answer that says anything about the key: a timeout, a refused connection,
	// a 5xx.
	CodeUnreachable = "unreachable"
)

// Result is the verdict of one probe. The handler maps it onto the AiProbeResult message.
//
//	OK       the provider accepted the key (a 2xx, a 429, runblob's 404 on the zero uuid);
//	Code     one of the Code* words, "" when none fits;
//	Message  a short lowercase sentence for a person; never any part of the key;
//	Balance  what the account has left where the free endpoint says so ("24.50 USD", "1200
//	         credits"), "" everywhere else — unknown is never shown as zero.
type Result struct {
	OK      bool
	Code    string
	Message string
	Balance string
}

const (
	// Timeout bounds ONE probe, whatever client the caller passes: a person is waiting on the "save
	// key" button, and a provider that cannot list its models in eight seconds is not answering.
	Timeout = 8 * time.Second

	// maxBody caps what is read of an answer. The balance bodies are a few hundred bytes; a list of
	// models is tens of kilobytes at most and is read only to be drained.
	maxBody = 64 << 10

	// maxMessage is the longest Result.Message, in bytes.
	maxMessage = 120
)

// Base URLs — constants on purpose; see the package comment.
const (
	openAIBase     = "https://api.openai.com"
	anthropicBase  = "https://api.anthropic.com"
	googleBase     = "https://generativelanguage.googleapis.com"
	openRouterBase = "https://openrouter.ai"
	apibostBase    = "https://apibost.com"
	falBase        = "https://api.fal.ai"
	meshyBase      = "https://api.meshy.ai"
	runblobBase    = "https://platform.runblob.io"
	recraftBase    = "https://external.api.recraft.ai"

	// anthropicVersion is the API version header every Anthropic request carries.
	anthropicVersion = "2023-06-01"

	// runblobZeroGeneration is a generation id nobody can own. runblob documents no free
	// list/balance/account endpoint, so its probe asks for the STATUS of this id: the key is checked
	// before the id is looked up, 401 means the key is refused and 404 means it was accepted.
	runblobZeroGeneration = "00000000-0000-0000-0000-000000000000"
)

// auth is how a provider wants the key.
type auth int

const (
	authBearer    auth = iota + 1 // Authorization: Bearer <key>
	authAnthropic                 // x-api-key: <key> + anthropic-version
	authGoogle                    // x-goog-api-key: <key> (never ?key=: a query string lands in logs)
	authFal                       // Authorization: Key <key>
)

type probeKey struct {
	provider string
	kind     entity.AIKeyKind
}

// endpoint is one free probe.
type endpoint struct {
	// url is absolute and constant, static query included.
	url string
	// query builds the time-dependent query of a cost-report probe from "now"; nil = none.
	query func(now time.Time) string
	auth  auth
	// balance reads what the account has left from a 2xx body; nil = the endpoint reports none.
	// ok=false means the body could not be read as the provider documents it.
	balance func(body []byte) (balance string, ok bool)
	// notFoundIsOK — a 404 proves the key was accepted (runblob's status read of the zero uuid).
	notFoundIsOK bool
}

// endpoints — every probe there is, one per (provider, key kind). A pair that is not here has no
// probe. Nothing below may be a completion or a generation call (see the package comment).
var endpoints = map[probeKey]endpoint{
	// ── api keys: the key that serves generations ──
	{entity.AIProviderOpenAI, entity.AIKeyAPI}: {
		url: openAIBase + "/v1/models", auth: authBearer,
	},
	{entity.AIProviderAnthropic, entity.AIKeyAPI}: {
		url: anthropicBase + "/v1/models", auth: authAnthropic,
	},
	{entity.AIProviderGoogle, entity.AIKeyAPI}: {
		url: googleBase + "/v1beta/models?pageSize=1", auth: authGoogle,
	},
	{entity.AIProviderOpenRouter, entity.AIKeyAPI}: {
		url: openRouterBase + "/api/v1/key", auth: authBearer, balance: openRouterBalance,
	},
	{entity.AIProviderApibost, entity.AIKeyAPI}: {
		url: apibostBase + "/v1/models", auth: authBearer,
	},
	// fal's account endpoint may want the ADMIN key, so the api key is checked against the price of
	// the endpoint this shop actually calls (fal.DefaultModelCutout).
	{entity.AIProviderFal, entity.AIKeyAPI}: {
		url: falBase + "/v1/models/pricing?endpoint_id=fal-ai/birefnet/v2", auth: authFal,
	},
	{entity.AIProviderMeshy, entity.AIKeyAPI}: {
		url: meshyBase + "/openapi/v1/balance", auth: authBearer, balance: meshyBalance,
	},
	{entity.AIProviderRunblob, entity.AIKeyAPI}: {
		url: runblobBase + "/v1/kling/generations/" + runblobZeroGeneration, auth: authBearer, notFoundIsOK: true,
	},
	{entity.AIProviderRecraft, entity.AIKeyAPI}: {
		url: recraftBase + "/v1/users/me", auth: authBearer,
	},

	// ── admin keys: the reconciliation key for the provider's cost API (D-06) ──
	{entity.AIProviderOpenAI, entity.AIKeyAdmin}: {
		url: openAIBase + "/v1/organization/costs", auth: authBearer, query: openAICostsQuery,
	},
	{entity.AIProviderAnthropic, entity.AIKeyAdmin}: {
		url: anthropicBase + "/v1/organizations/cost_report", auth: authAnthropic, query: anthropicCostQuery,
	},
	{entity.AIProviderFal, entity.AIKeyAdmin}: {
		url: falBase + "/v1/account/billing?expand=credits", auth: authFal, balance: falBalance,
	},
}

// clock is "now" for the cost-report queries; tests pin it.
var clock = time.Now

// openAICostsQuery asks for one bucket starting a day ago.
func openAICostsQuery(now time.Time) string {
	return fmt.Sprintf("start_time=%d&limit=1", now.Add(-24*time.Hour).Unix())
}

// anthropicCostQuery asks for yesterday's bucket (UTC days: the report snaps to them).
func anthropicCostQuery(now time.Time) string {
	today := now.UTC().Truncate(24 * time.Hour)
	return fmt.Sprintf("starting_at=%s&ending_at=%s",
		today.Add(-24*time.Hour).Format(time.RFC3339), today.Format(time.RFC3339))
}

// target is the full URL of the probe at "now".
func (e endpoint) target(now time.Time) string {
	if e.query == nil {
		return e.url
	}
	sep := "?"
	if strings.Contains(e.url, "?") {
		sep = "&"
	}
	return e.url + sep + e.query(now)
}

// defaultClient serves a nil client.
var defaultClient = &http.Client{Timeout: Timeout, CheckRedirect: refuseRedirect}

// refuseRedirect keeps a redirect as the answer instead of following it. net/http drops
// Authorization on a redirect to another host but forwards x-api-key and x-goog-api-key to wherever
// the Location points; a probe has no reason to go anywhere but the one URL it was built for.
func refuseRedirect(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

// httpClient is the client a probe uses: the default for nil, otherwise a shallow copy of the
// caller's (same transport and pool) that refuses redirects. The caller's client is not changed.
func httpClient(c *http.Client) *http.Client {
	if c == nil {
		return defaultClient
	}
	cp := *c
	cp.CheckRedirect = refuseRedirect
	return &cp
}

// Probe checks key against the free endpoint of (providerKey, kind) and never returns an error:
// every failure is a Result. An unknown provider, an unsupported (provider, kind) pair and an empty
// key are answered without a request. client nil = a default with the 8 s timeout; either way ONE
// probe is bounded by Timeout.
func Probe(ctx context.Context, providerKey string, kind entity.AIKeyKind, key string, client *http.Client) Result {
	ep, ok := endpoints[probeKey{provider: providerKey, kind: kind}]
	if !ok {
		return Result{Message: fmt.Sprintf("no probe for %s/%s", providerName(providerKey), kindName(kind))}
	}
	if strings.TrimSpace(key) == "" {
		return Result{Code: CodeKeyRejected, Message: "no key"}
	}
	if !headerSafe(key) {
		// A pasted newline would make net/http refuse the header and the probe would report the
		// provider as unreachable; the key is what is wrong, and no provider accepts it.
		return Result{Code: CodeKeyRejected, Message: "key contains spaces or control characters"}
	}

	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, Timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ep.target(clock()), nil)
	if err != nil {
		// Only a malformed constant URL can land here; the tests build every one.
		return Result{Message: "probe could not be built"}
	}
	req.Header.Set("Accept", "application/json")
	switch ep.auth {
	case authBearer:
		req.Header.Set("Authorization", "Bearer "+key)
	case authAnthropic:
		req.Header.Set("x-api-key", key)
		req.Header.Set("anthropic-version", anthropicVersion)
	case authGoogle:
		req.Header.Set("x-goog-api-key", key)
	case authFal:
		req.Header.Set("Authorization", "Key "+key)
	}

	resp, err := httpClient(client).Do(req)
	if err != nil {
		return transportFailure(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	return classify(ep, resp.StatusCode, body, key)
}

// classify turns an answer into a verdict. It decides from the status code only; the body is read
// for a balance (2xx) and quoted for an unexplained 4xx, never used to classify.
func classify(ep endpoint, status int, body []byte, key string) Result {
	switch {
	case status >= 200 && status < 300:
		return accepted(ep, body)
	case status == http.StatusNotFound && ep.notFoundIsOK:
		return Result{OK: true, Message: "key accepted"}
	case status == http.StatusUnauthorized:
		return Result{Code: CodeKeyRejected, Message: "key rejected (http 401)"}
	case status == http.StatusForbidden:
		return Result{Code: CodeKeyRejected, Message: "key refused: no permission for this check (http 403)"}
	case status == http.StatusPaymentRequired:
		return Result{Code: CodeOutOfCredits, Message: "out of credits (http 402)"}
	case status == http.StatusTooManyRequests:
		// The provider looked at the key before it counted the request against it.
		return Result{OK: true, Message: "rate limited"}
	case status >= 500:
		return Result{Code: CodeUnreachable, Message: fmt.Sprintf("provider error (http %d)", status)}
	case status >= 400:
		msg := fmt.Sprintf("probe refused (http %d)", status)
		if s := providerSentence(body, key); s != "" {
			msg += ": " + s
		}
		return Result{Message: clip(msg)}
	case status >= 300:
		return Result{Message: fmt.Sprintf("unexpected redirect (http %d)", status)}
	}
	return Result{Message: fmt.Sprintf("unexpected answer (http %d)", status)}
}

// accepted is a 2xx: the key works; the balance is read where the endpoint reports one.
func accepted(ep endpoint, body []byte) Result {
	if ep.balance == nil {
		return Result{OK: true, Message: "key accepted"}
	}
	b, ok := ep.balance(body)
	if !ok {
		return Result{OK: true, Message: "key accepted; balance not readable"}
	}
	return Result{OK: true, Message: "key accepted", Balance: b}
}

// transportFailure is a request that got no answer. The error text is not quoted: it is net/http's,
// not a person's, and it names the URL.
func transportFailure(err error) Result {
	var ne net.Error
	switch {
	case errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &ne) && ne.Timeout()):
		return Result{Code: CodeUnreachable, Message: "provider did not answer in time"}
	case errors.Is(err, context.Canceled):
		return Result{Code: CodeUnreachable, Message: "probe cancelled"}
	}
	return Result{Code: CodeUnreachable, Message: "could not reach the provider"}
}

// ───────────────────────── balances ─────────────────────────

// openRouterBalance reads GET /api/v1/key: data.limit_remaining is what THIS key may still spend.
// null = the key has no limit, and data.usage alone is spend, not balance — so "" (known, not
// shown), not a zero.
func openRouterBalance(body []byte) (string, bool) {
	var v struct {
		Data *struct {
			LimitRemaining *json.Number `json:"limit_remaining"`
		} `json:"data"`
	}
	if json.Unmarshal(body, &v) != nil || v.Data == nil {
		return "", false
	}
	if v.Data.LimitRemaining == nil {
		return "", true
	}
	d, err := decimal.NewFromString(v.Data.LimitRemaining.String())
	if err != nil {
		return "", false
	}
	return d.StringFixed(2) + " USD", true
}

// falBalance reads GET /v1/account/billing?expand=credits: credits.current_balance in
// credits.currency (USD when fal leaves it out).
func falBalance(body []byte) (string, bool) {
	var v struct {
		Credits *struct {
			CurrentBalance *json.Number `json:"current_balance"`
			Currency       string       `json:"currency"`
		} `json:"credits"`
	}
	if json.Unmarshal(body, &v) != nil || v.Credits == nil || v.Credits.CurrentBalance == nil {
		return "", false
	}
	d, err := decimal.NewFromString(v.Credits.CurrentBalance.String())
	if err != nil {
		return "", false
	}
	cur := strings.ToUpper(strings.TrimSpace(v.Credits.Currency))
	if cur == "" {
		cur = "USD"
	}
	if !currencyCode.MatchString(cur) {
		return "", false
	}
	return d.StringFixed(2) + " " + cur, true
}

var currencyCode = regexp.MustCompile(`^[A-Z]{3}$`)

// meshyBalance reads GET /openapi/v1/balance: {"balance": <credits>}.
func meshyBalance(body []byte) (string, bool) {
	var v struct {
		Balance *json.Number `json:"balance"`
	}
	if json.Unmarshal(body, &v) != nil || v.Balance == nil {
		return "", false
	}
	d, err := decimal.NewFromString(v.Balance.String())
	if err != nil {
		return "", false
	}
	if !d.IsInteger() {
		return d.StringFixed(2) + " credits", true
	}
	return d.String() + " credits", true
}

// ───────────────────────── messages ─────────────────────────

// providerName and kindName echo only known vocabulary into a message: a caller that swapped the
// provider and the key arguments must not get the key printed back.
func providerName(p string) string {
	if entity.IsAIProviderKey(p) {
		return p
	}
	return "unknown provider"
}

func kindName(k entity.AIKeyKind) string {
	if k == entity.AIKeyAPI || k == entity.AIKeyAdmin {
		return string(k)
	}
	return "unknown kind"
}

// headerSafe reports whether key is printable ASCII with no spaces — the only shape a provider key
// has, and the only one net/http will put in a header unchanged.
func headerSafe(key string) bool {
	for i := 0; i < len(key); i++ {
		if key[i] <= ' ' || key[i] >= 0x7f {
			return false
		}
	}
	return true
}

// tokenLike matches a run that could be a secret: 20+ key-alphabet characters in a row.
var tokenLike = regexp.MustCompile(`[A-Za-z0-9_\-]{20,}`)

// providerSentence is the provider's own sentence from an error body, scrubbed and lowercased, or ""
// when there is none. A word is dropped when it contains the key, its first or last characters, a
// masked run ("***") or anything token-shaped: OpenAI, for one, quotes the key it refused.
func providerSentence(body []byte, key string) string {
	s := errorText(body)
	if s == "" {
		return ""
	}
	var prefix, suffix string
	if len(key) >= 8 {
		prefix, suffix = key[:6], key[len(key)-4:]
	}
	words := strings.Fields(s)
	for i, w := range words {
		if strings.Contains(w, key) || strings.Contains(w, "**") || tokenLike.MatchString(w) ||
			(prefix != "" && (strings.Contains(w, prefix) || strings.Contains(w, suffix))) {
			words[i] = "[redacted]"
		}
	}
	return strings.ToLower(strings.Join(words, " "))
}

// errorText pulls the sentence out of the common error shapes: {"error":{"message"}},
// {"error":"…"}, {"message":"…"}, {"detail":"…"}; a short plain-text body as it is; nothing for
// HTML or anything else.
func errorText(body []byte) string {
	var v struct {
		Error   json.RawMessage `json:"error"`
		Message string          `json:"message"`
		Detail  json.RawMessage `json:"detail"`
	}
	if json.Unmarshal(body, &v) == nil {
		var nested struct {
			Message string `json:"message"`
		}
		var flat string
		switch {
		case json.Unmarshal(v.Error, &nested) == nil && strings.TrimSpace(nested.Message) != "":
			return strings.TrimSpace(nested.Message)
		case json.Unmarshal(v.Error, &flat) == nil && strings.TrimSpace(flat) != "":
			return strings.TrimSpace(flat)
		case strings.TrimSpace(v.Message) != "":
			return strings.TrimSpace(v.Message)
		case json.Unmarshal(v.Detail, &flat) == nil && strings.TrimSpace(flat) != "":
			return strings.TrimSpace(flat)
		}
		return ""
	}
	s := strings.TrimSpace(string(body))
	if s == "" || strings.HasPrefix(s, "<") {
		return ""
	}
	return s
}

// clip cuts s to maxMessage bytes on a rune boundary, marking the cut.
func clip(s string) string {
	if len(s) <= maxMessage {
		return s
	}
	cut := maxMessage - len("…")
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "…"
}
