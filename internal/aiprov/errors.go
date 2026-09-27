package aiprov

import (
	"errors"
	"strconv"
	"strings"
)

// EngagedMarker is how a transport that does NOT yet speak CallError says "the request was written":
// the openrouter chat client's engaged wrapper implements it. aiprov deliberately imports NO client
// package (the clients will import aiprov when their transports are ported in commit C), so the
// mark travels as an interface, not as a type.
type EngagedMarker interface{ ProviderEngaged() bool }

// Sentinels of the provider-neutral layer. They name the situation, never a provider: a transport's
// own sentinel (openrouter.ErrModelUnavailable, fal.ErrNotConfigured, …) travels inside CallError.Err.
var (
	ErrNotConfigured       = errors.New("ai: no enabled provider for this purpose")
	ErrModelUnavailable    = errors.New("ai: model not served")
	ErrBudgetExhausted     = errors.New("ai: answer budget exhausted")
	ErrAllCandidatesFailed = errors.New("ai: every candidate failed")
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

// Error renders "<provider> [<code>, HTTP <status>]: <err>", dropping the parts that are empty. The
// wrapped error's text is kept verbatim: some of these errors reach a person, and the sentence a
// transport wrote for them must survive the wrapping.
func (e *CallError) Error() string {
	if e == nil {
		return "<nil>"
	}
	var b strings.Builder
	if e.Provider != "" {
		b.WriteString(e.Provider)
	} else {
		b.WriteString("ai")
	}
	var tags []string
	if e.Code != "" {
		tags = append(tags, e.Code)
	}
	if e.HTTPStatus != 0 {
		tags = append(tags, "HTTP "+strconv.Itoa(e.HTTPStatus))
	}
	if len(tags) > 0 {
		b.WriteString(" [")
		b.WriteString(strings.Join(tags, ", "))
		b.WriteString("]")
	}
	b.WriteString(": ")
	if e.Err != nil {
		b.WriteString(e.Err.Error())
	} else {
		b.WriteString("call failed")
	}
	return b.String()
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
// that answers true — the existing chat client marks its errors with its own wrapper and keeps
// doing so until its transport is ported, and a caller that switched to this helper must not lose
// that answer.
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
