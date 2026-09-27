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
// A COMPLETION ACTS ONLY ON THE ADMISSION IT COMPLETES (Codex second review, P1). Keyed by
// (provider, capability) alone, an end could not tell whose call it was ending: a call admitted
// while CLOSED that outlived the opening and the whole window could Release the probe somebody else
// holds (M such stragglers → M extra concurrent probes), or — worse — its Success closed a
// half-open breaker while the real probe was still out; and a call made with a rotated-away key
// could still count against the breaker ResetBreakers had just cleared for the new one. So Admit
// hands out an Admission stamped with the breaker's generation, and every end brings it back:
//   - gen moves on every opening (from closed or from half-open), on every Reset, and whenever a
//     probe ends (closed by its success, freed by its release) — so an Admission from before any of
//     those is simply not recognised, and a probe admission is good for one end only;
//   - an end whose gen is not the breaker's is ignored;
//   - HALF-OPEN: only the admission that holds the probe (probe=true) closes it, re-opens it, or
//     frees it; any other end is ignored;
//   - CLOSED: a fault counts, a success clears the count;
//   - OPEN: nothing — a straggler must not stretch the window.
//
// The zero Admission is what a caller gets for a provider with no breaker yet (the registry creates
// none on Admit). A breaker is born at gen 0 and leaves it on its first opening or Reset, so the
// zero Admission is accepted exactly while the breaker has never opened nor been reset — which is
// what lets the first three faults ever, made with no breaker in place, create it and open it.
// Known limit: Reset can retire only a breaker that exists. A call admitted (zero Admission) before
// ResetBreakers of a provider that had no breaker yet can still count its transient fault against
// the fresh one — a fault about the provider, not the key (a rejected old key is a 401, never
// counted), and never more than the calls already in flight at the rotation.
type probeBreaker struct {
	mu            sync.Mutex
	state         string    // BreakerClosed | BreakerOpen | BreakerHalfOpen; "" reads as closed
	failures      int       // consecutive transient faults while closed
	openedAt      time.Time // the registry-clock instant it last opened
	probeInFlight bool      // half-open only: the one probe has been handed out and has not ended
	gen           uint64    // the generation Admissions are stamped with; see the type's doc
}

// Admission is the ticket Admit hands out and every end of the call brings back. Its fields are
// unexported: a caller cannot forge one, only return the one it was given. The zero value is "no
// breaker at admission time" (see probeBreaker).
type Admission struct {
	gen   uint64
	probe bool // this admission holds the half-open breaker's one probe
}

// Admit reports whether a caller may make the physical call now, and the Admission that call's end
// must bring back. Closed: yes. Open inside its window: no. Open past its window: it turns half-open
// and THIS caller is the probe. Half-open: yes to the first caller only; everybody else waits for
// that probe's verdict.
func (b *probeBreaker) Admit(now time.Time) (Admission, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	switch b.state {
	case BreakerOpen:
		if now.Sub(b.openedAt) < breakerConfig.OpenTimeout {
			return Admission{}, false
		}
		b.state = BreakerHalfOpen
		fallthrough
	case BreakerHalfOpen:
		if b.probeInFlight {
			return Admission{}, false
		}
		b.probeInFlight = true
		return Admission{gen: b.gen, probe: true}, true
	}
	return Admission{gen: b.gen}, true
}

// current reports whether a is an admission of the breaker's present generation. Caller holds mu.
func (b *probeBreaker) current(a Admission) bool { return a.gen == b.gen }

// Success — a's call went through. Closed: the fault count cleared (so three faults a week apart
// never open it). Half-open, a holding the probe: closed, the probe ended. Anything else: ignored.
func (b *probeBreaker) Success(a Admission) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.current(a) {
		return
	}
	switch b.state {
	case BreakerHalfOpen:
		if !a.probe {
			return
		}
		b.state = BreakerClosed
		b.failures = 0
		b.probeInFlight = false
		b.gen++ // the probe's admission is spent
	case BreakerOpen:
		return
	default:
		b.failures = 0
	}
}

// Fault — a's call hit a transient fault nobody paid for. Closed: one more; the MaxFailures-th opens
// it. Half-open, a holding the probe: the probe failed, so it opens again for a FULL window counted
// from now. Open, or not a's generation, or half-open without the probe: nothing — a straggler must
// neither stretch the window nor judge somebody else's probe.
func (b *probeBreaker) Fault(a Admission, now time.Time) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.current(a) {
		return
	}
	switch b.state {
	case BreakerOpen:
		return
	case BreakerHalfOpen:
		if !a.probe {
			return
		}
		b.state = BreakerOpen
		b.openedAt = now
		b.probeInFlight = false
		b.gen++ // a new window: every admission handed out before it is retired
	default:
		b.failures++
		if b.failures >= breakerConfig.MaxFailures {
			b.state = BreakerOpen
			b.openedAt = now
			b.failures = 0
			b.gen++ // a new window: every admission handed out before it is retired
		}
	}
}

// Release — a's call ended WITHOUT a verdict (see the type's doc): a half-open breaker frees its
// probe for the next caller, when a is the admission holding it. Every other state has nothing
// reserved, and an end that is not the probe's frees nothing.
func (b *probeBreaker) Release(a Admission) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.current(a) || b.state != BreakerHalfOpen || !a.probe {
		return
	}
	b.probeInFlight = false
	b.gen++ // the probe's admission is spent: a second end of it must not free the NEXT probe
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
// chance, not a probe still owed — and retires every admission handed out before it: a call made
// with the old key must not count against the new one.
func (b *probeBreaker) Reset() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.state = BreakerClosed
	b.failures = 0
	b.openedAt = time.Time{}
	b.probeInFlight = false
	b.gen++
}
