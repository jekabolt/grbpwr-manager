package aiprov

import "errors"

// EngagedMarker is how a transport that does NOT yet speak CallError says "the request was written".
// Since B-11 nothing in the tree produces it — the openrouter chat client, its only producer, now
// speaks CallError through oaichat — and it stays so a transport ported later can mark its errors
// before it adopts CallError, without Engaged losing the answer. aiprov imports NO client package, so
// the mark travels as an interface, not as a type.
type EngagedMarker interface{ ProviderEngaged() bool }

// Sentinels of the provider-neutral layer. They name the situation, never a provider: a transport's
// own sentinel (fal.ErrNotConfigured, …) travels inside CallError.Err. The chat transport's four
// (openrouter.ErrNotConfigured / ErrModelUnavailable / ErrBudgetExhausted / ErrResponseTooLarge) ARE
// these — the openrouter vars are aliases since B-11 — so errors.Is answers from either name.
//
// ⚠ THE THREE A TRANSPORT RAISES MID-SENTENCE CARRY NO PREFIX, AND THAT IS WHAT KEEPS THE SENTENCES.
// A transport writes them as "<provider>: %w: API error (HTTP 404): …" (oaichat), so the text a log
// or a person reads is exactly the one the openrouter client wrote before the move — "openrouter: the
// configured model is not available at the provider: API error (HTTP 404): …". An "ai: " prefix here
// would print "openrouter: ai: …". The two the ROUTER raises on its own keep "ai: ".
var (
	ErrNotConfigured       = errors.New("ai: no enabled provider for this purpose")
	ErrAllCandidatesFailed = errors.New("ai: every candidate failed")

	// ErrModelUnavailable — the provider answered 404: the model slug is not served (retired, renamed,
	// never existing) or, with a custom base URL, the endpoint is not there. Both are a SETTING, not
	// weather; classified by status alone, never by the provider's prose.
	ErrModelUnavailable = errors.New("the configured model is not available at the provider")
	// ErrBudgetExhausted — finish_reason=length with NO content: the completion ceiling was spent
	// (reasoning tokens count against it) before a single character of answer. Deterministic — the
	// next press burns the same money for the same nothing — so it is a setting, not weather.
	ErrBudgetExhausted = errors.New("the model spent the whole completion budget without answering")
	// ErrResponseTooLarge — the response body exceeded the transport's read ceiling. The ceiling
	// REFUSES rather than trims: a prefix that happens to parse would be a silently shortened answer.
	ErrResponseTooLarge = errors.New("the provider's response exceeded the read ceiling")
)

// Codes of CallError.Code — the ledger's error_code and the provider badge's words. One vocabulary
// for every transport, so the panel reads "key rejected" the same way whichever provider said it.
const (
	CodeKeyRejected     = "key_rejected"     // 401 / 403
	CodeOutOfCredits    = "out_of_credits"   // 402
	CodeModelUnknown    = "model_unknown"    // 404 (ErrModelUnavailable)
	CodeBadRequest      = "bad_request"      // 400 / 422 / any other 4xx, and a request refused before the wire
	CodeRateLimited     = "rate_limited"     // 429
	CodeProviderError   = "provider_error"   // 5xx / 408, and a 2xx whose envelope is broken or carries an error
	CodeTransport       = "transport"        // the HTTP round trip failed (no status)
	CodeTimeout         = "timeout"          // a deadline expired — ours (the call budget) or the caller's
	CodeCanceled        = "canceled"         // the CALLER cancelled (a closed tab); not the provider's fault
	CodeTooLarge        = "too_large"        // ErrResponseTooLarge
	CodeEmptyAnswer     = "empty_answer"     // a 2xx with no choices or an empty message
	CodeBudgetExhausted = "budget_exhausted" // ErrBudgetExhausted
	CodeNotConfigured   = "not_configured"   // no key (ErrNotConfigured)
)

// CallError is the ONE error shape every transport must return for a failed provider call (02-PLAN
// rev.1 A2). The router, the breaker and the ledger decide from its fields, never from error text:
// strings of net/http and of the providers are not a contract and change between releases.
//
//	Provider    the provider key (entity.AIProvider*) of the transport that failed;
//	Code        a short machine word for the ledger's error_code and the provider badge
//	            ("key_rejected", "out_of_credits", "model_unknown", "timeout", …), "" when none fits;
//	HTTPStatus  the provider's status, 0 when no response arrived;
//	Engaged     THE REQUEST WAS WRITTEN: the provider received it and may be billing it. Set by the
//	            transport (httptrace WroteRequest, or its own rule — fal: an unconfirmed submit is
//	            engaged). An engaged failure is terminal for the fallback loop: trying the next
//	            candidate after money moved pays twice for one answer;
//	Retryable   the same request may succeed later (5xx, 429, a transport hiccup) — it feeds the
//	            breaker; a configuration refusal (401/402/404/422) is NOT retryable;
//	Err         the underlying error, the transport's own sentinel kept for errors.Is.
type CallError struct {
	Provider   string
	Code       string
	HTTPStatus int
	Engaged    bool
	Retryable  bool
	Err        error
}

// Error is the TRANSPORT'S OWN SENTENCE, verbatim — Err.Error() and nothing added. Code, HTTPStatus
// and Engaged are FIELDS, read with AsCallError; they are not prose.
//
// ⚠ NOTHING IS PREPENDED ON PURPOSE (B-11). Some of these errors reach a person whole (design_run.go:
// designDraftCallError), and some consumers still read the sentence until B-18 repoints them at the
// fields — techcard_ai_enhance.go's providerHTTPStatusRe is anchored at the START of err.Error()
// ("^openrouter: API error \(HTTP ([0-9]{3})\):"); a "[code, HTTP 502]" tag in front would blind it.
// With no Err the text still names the provider, so a bare CallError is never an empty line.
func (e *CallError) Error() string {
	if e == nil {
		return "<nil>"
	}
	if e.Err != nil {
		return e.Err.Error()
	}
	if e.Provider != "" {
		return e.Provider + ": call failed"
	}
	return "ai: call failed"
}

// Unwrap exposes the transport's error, so errors.Is(err, openrouter.ErrModelUnavailable) and
// friends keep answering through the wrapper. Nil-safe.
func (e *CallError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// AsCallError finds the first *CallError in err's chain. A typed-nil *CallError is not an answer.
func AsCallError(err error) (*CallError, bool) {
	var ce *CallError
	if errors.As(err, &ce) && ce != nil {
		return ce, true
	}
	return nil, false
}

// Engaged reports "money may have moved for this call": true when ANY *CallError in err's chain
// (joined errors included) says Engaged, and ALSO when any error in the chain is an EngagedMarker
// that answers true (a transport not yet speaking CallError). openrouter.ProviderEngaged IS this
// function since B-11.
//
// ⚠ ANY, NOT FIRST. errors.As stops at the outermost CallError; a router that wraps a candidate's
// engaged failure into its own non-engaged one, or joins several candidates' errors, would read
// "not engaged" and fall back after money moved. Over-answering "engaged" costs a fallback; under-
// answering costs a second payment — so the walk looks at every layer.
func Engaged(err error) bool {
	if err == nil {
		return false
	}
	if anyEngagedCallError(err) {
		return true
	}
	var m EngagedMarker
	return errors.As(err, &m) && m != nil && m.ProviderEngaged()
}

func anyEngagedCallError(err error) bool {
	for err != nil {
		if ce, ok := err.(*CallError); ok && ce != nil && ce.Engaged {
			return true
		}
		switch u := err.(type) {
		case interface{ Unwrap() []error }:
			for _, inner := range u.Unwrap() {
				if anyEngagedCallError(inner) {
					return true
				}
			}
			return false
		case interface{ Unwrap() error }:
			err = u.Unwrap()
		default:
			return false
		}
	}
	return false
}
