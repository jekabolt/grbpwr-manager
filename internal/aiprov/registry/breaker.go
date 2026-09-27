package registry

import (
	"sync"
	"time"
)

// breakerConfig — one breaker per (provider, capability). Three consecutive transient faults open it
// for five minutes; then ONE probe decides. Terminal faults never reach it (RecordFailure).
var breakerConfig = struct {
	MaxFailures int
	OpenTimeout time.Duration
}{MaxFailures: 3, OpenTimeout: 5 * time.Minute}

// probeBreaker is the registry's own circuit breaker, and it is not internal/circuitbreaker on
// purpose (Codex A1 #1). Two things were wrong with borrowing that one:
//
// TWO CLOCKS DISAGREED. internal/circuitbreaker judges its open window with time.Now; the registry
// judged the same window with its own clock. Candidates could call a provider "half-open, the next
// call is the probe" while the breaker underneath still thought itself open — and with an injected
// clock not even a success could close it. Here there is ONE clock: every method that needs the time
// takes the instant from the registry (r.clock()), and nothing in this file reads time.Now.
//
// NOTHING RESERVED THE PROBE. Candidates only REPORTED half-open, so every caller that arrived past
// the window made a physical — possibly paid — call before any of them came back with a verdict.
// Here Admit hands the probe to exactly ONE caller (probeInFlight); every other caller is refused
// until that probe ends in Success, Fault or Release.
//
// A PROBE THAT ENDS WITHOUT A VERDICT MUST STILL END. A 401/402/404/422, an engaged error, or an
// admitted caller that never called says nothing about the provider being up or down, so it neither
// closes nor re-opens the breaker — but it frees the reservation (Release); otherwise the provider
// would stay reserved by a probe nobody is running, i.e. out for good.
//
// Known limit of a bool Admit: a call admitted while the breaker was CLOSED that is still in flight
// after the breaker opened AND its whole window passed can, on a verdict-less end, Release the probe
// somebody else holds. That needs one call to outlive the five-minute window; the cost is one extra
// concurrent probe, once.
type probeBreaker struct {
	mu            sync.Mutex
	state         string    // BreakerClosed | BreakerOpen | BreakerHalfOpen; "" reads as closed
	failures      int       // consecutive transient faults while closed
	openedAt      time.Time // the registry-clock instant it last opened
	probeInFlight bool      // half-open only: the one probe has been handed out and has not ended
}

// Admit reports whether a caller may make the physical call now. Closed: yes. Open inside its window:
// no. Open past its window: it turns half-open and THIS caller is the probe. Half-open: yes to the
// first caller only; everybody else waits for that probe's verdict.
func (b *probeBreaker) Admit(now time.Time) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	switch b.state {
	case BreakerOpen:
		if now.Sub(b.openedAt) < breakerConfig.OpenTimeout {
			return false
		}
		b.state = BreakerHalfOpen
		fallthrough
	case BreakerHalfOpen:
		if b.probeInFlight {
			return false
		}
		b.probeInFlight = true
		return true
	}
	return true
}

// Success — a call went through: closed, the fault count cleared (so three faults a week apart never
// open it), and no probe reserved any more.
func (b *probeBreaker) Success() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.state = BreakerClosed
	b.failures = 0
	b.probeInFlight = false
}

// Fault — a transient fault nobody paid for. Closed: one more; the MaxFailures-th opens it. Half-open:
// the probe failed, so it opens again for a FULL window counted from now. Open: nothing — a straggler
// admitted before it opened must not stretch the window.
func (b *probeBreaker) Fault(now time.Time) {
	b.mu.Lock()
	defer b.mu.Unlock()
	switch b.state {
	case BreakerOpen:
		return
	case BreakerHalfOpen:
		b.state = BreakerOpen
		b.openedAt = now
		b.probeInFlight = false
	default:
		b.failures++
		if b.failures >= breakerConfig.MaxFailures {
			b.state = BreakerOpen
			b.openedAt = now
			b.failures = 0
		}
	}
}

// Release — the admitted call ended WITHOUT a verdict (see the type's doc): a half-open breaker frees
// its probe for the next caller; every other state has nothing reserved.
func (b *probeBreaker) Release() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.state == BreakerHalfOpen {
		b.probeInFlight = false
	}
}

// State is what the panel and Candidates see at now: open only inside its window; past the window it
// is already half-open, whether or not anybody has taken the probe yet.
func (b *probeBreaker) State(now time.Time) string {
	b.mu.Lock()
	defer b.mu.Unlock()
	switch b.state {
	case BreakerOpen:
		if now.Sub(b.openedAt) < breakerConfig.OpenTimeout {
			return BreakerOpen
		}
		return BreakerHalfOpen
	case BreakerHalfOpen:
		return BreakerHalfOpen
	}
	return BreakerClosed
}

// Reset closes it and forgets everything, the probe reservation included — a rotated key is a new
// chance, not a probe still owed.
func (b *probeBreaker) Reset() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.state = BreakerClosed
	b.failures = 0
	b.openedAt = time.Time{}
	b.probeInFlight = false
}
