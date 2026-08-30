// Package designgen is the EXECUTOR of the DESIGN band's paid generation queue.
//
// WHAT IT IS. One background worker, built on the pattern of internal/campaigndispatch: a ticker,
// a bounded tick, saferun.Recover, a health.Tracker. Every tick it revives runs whose lease died,
// claims what is ready, and takes each claimed run through exactly one pass:
//
//	StartAttempt → provider call (OUTSIDE any transaction) → bytes into the bucket (BEFORE any
//	transaction) → FinishAttempt (the money) → CompleteRun | FailRun (the result) → sweep orphans
//
// WHAT IT IS NOT. It is not the queue: the state machine lives in internal/store/design/queue.go
// and this package only turns its verbs. It is not a retry layer: a paid call is never repeated
// inside a provider client or inside this worker; FailRun schedules the next attempt through
// next_attempt_at, and the attempt cap is a MONEY figure held by the store. It is not the text
// route: kind=draft_idea is executed synchronously by the handler and is excluded from the claim
// predicate, because a worker that picked it up would pay a second time for the same answer.
//
// # THE FOUR BOUNDARIES THAT COST THE OWNER MONEY WHEN CROSSED
//
//  1. THE PROVIDER CALL HAPPENS OUTSIDE EVERY TRANSACTION. Generation takes tens of seconds to
//     minutes; a database connection held across it is a connection the rest of the process does
//     not have, on an instance with a pool of a handful. Nothing in this package opens a
//     transaction — it calls store verbs, and each of those is its own short transaction.
//
//  2. THE BYTES GO INTO THE BUCKET BEFORE THE TRANSACTION, AND WHAT NOBODY ADOPTS IS SWEPT.
//     CompleteRun takes media ids that already exist and returns the pictures it actually filed;
//     design.OrphanedMedia(minted, adopted) names the difference and mediaSink.Drop removes it.
//     ⚠ The case that makes the sweep necessary is err == nil: an idempotent re-file returns the
//     pictures of an EARLIER pass, so this pass's fresh uploads were adopted by nothing at all.
//     "It returned no error" is not "what I uploaded was taken".
//
//  3. THE CLAIM TOKEN TRAVELS WITH EVERY WRITE. It stands in the WHERE clause of CompleteRun and
//     FailRun, so a worker whose lease expired cannot overwrite the result of the worker that took
//     the job over. entity.ErrDesignClaimLost is therefore a NORMAL outcome here, not an incident:
//     somebody else owns the row, we drop our orphans and say so at info level.
//
//  4. NOTHING IS PAID FOR THAT CANNOT BE STORED. Before StartAttempt — that is, before any money
//     moves — the pass checks that the provider is configured AND that the media sink accepts the
//     content type the provider produces. Today the sink stores raster only, so the vector (SVG)
//     and 3D (GLB) routes refuse for free instead of buying a file with nowhere to live.
//
// # WHY A FAILURE'S CLASSIFICATION IS A MONEY DECISION
//
// The queue retries five times with an exponential backoff. Five retries against a rejected API
// key is five ticks of noise; five retries against a rate limit is the correct behaviour; and five
// retries against a call that was BILLED but produced nothing is five payments. So each provider
// sentinel is mapped, in classify.go, onto three separate answers: is it retryable, what does the
// history row call it, and what state does the attempt close in — where `unknown` means precisely
// "the money may be gone and we have nothing to show", which is a thing a person must be able to
// read rather than infer from a blank.
//
// A price that arrives TOGETHER WITH an error is still recorded. Both image and vector transports
// can fail after the meter ran (orimages returns a *Result beside its error, recraft wraps a
// ChargedError), and a ledger that only records successes under-reports spend in exactly the case
// where the spend was wasted.
package designgen
