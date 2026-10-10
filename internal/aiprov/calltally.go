package aiprov

import (
	"context"
	"sync"

	"github.com/shopspring/decimal"
)

// CallSpend is ONE physical provider call as the router booked it in the ledger: who answered, the
// row's status (entity.AICall*: ok, free, charged_failed, unknown, failed) and its price when one
// is known. A door that must tell the person what a press cost reads these instead of the final
// ChatResult, which only describes the LAST call of a fallback chain.
type CallSpend struct {
	Provider         string
	Model            string
	Status           string
	PromptTokens     int
	CompletionTokens int
	CostUSD          decimal.NullDecimal
}

// CallTally collects the CallSpend of every physical call made under a context (WithCallTally):
// every candidate of every router chain, every retry of the caller. Safe for concurrent use; the
// zero value works.
type CallTally struct {
	mu    sync.Mutex
	calls []CallSpend
}

// Add records one call; a nil tally ignores it.
func (t *CallTally) Add(c CallSpend) {
	if t == nil {
		return
	}
	t.mu.Lock()
	t.calls = append(t.calls, c)
	t.mu.Unlock()
}

// Calls returns a copy of what was recorded, in call order.
func (t *CallTally) Calls() []CallSpend {
	if t == nil {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]CallSpend(nil), t.calls...)
}

type callTallyKey struct{}

// WithCallTally returns ctx whose provider calls are recorded into t.
func WithCallTally(ctx context.Context, t *CallTally) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, callTallyKey{}, t)
}

// CallTallyFrom returns the tally WithCallTally put into ctx, or nil.
func CallTallyFrom(ctx context.Context) *CallTally {
	if ctx == nil {
		return nil
	}
	t, _ := ctx.Value(callTallyKey{}).(*CallTally)
	return t
}
