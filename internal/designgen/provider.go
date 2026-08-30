package designgen

import (
	"context"
	"fmt"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/shopspring/decimal"
)

// Content types the band deals in. They are named here rather than spelled at each call site
// because the sink's answer to "can you store this" and the provider's answer to "what will I
// return" have to be comparisons of the same strings.
const (
	ContentTypePNG  = "image/png"
	ContentTypeJPEG = "image/jpeg"
	ContentTypeWEBP = "image/webp"
	ContentTypeGIF  = "image/gif"
	ContentTypeSVG  = "image/svg+xml"
	ContentTypeGLB  = "model/gltf-binary"
)

// Job is one run, decoded from its frozen snapshot and ready to be sent to a provider.
//
// IT CARRIES NO MOODBOARD MEDIA, AND THAT IS A REQUIREMENT (W-15), NOT AN OVERSIGHT. The
// moodboard is the mood, not the prompt; a picture reaches the model only by a person moving it
// into REFERENCES. The screen says so, but the screen is a promise — the guarantee is that
// referenceMediaIDs never reads inputs.mood, and TestMoodboardNeverReachesTheProvider is what
// keeps that true.
type Job struct {
	RunID      int
	TechCardID int
	// Kind is the run kind: flat | render | vector | threed. draft_idea never reaches here.
	Kind string
	// Prompt is the composed instruction: the ask, the garment description, the fit, the roles and
	// notes of the references. Composed from the SNAPSHOT rather than from today's card, because
	// an async run must send what it was launched with, not what the card says when it happens to
	// be picked up.
	Prompt string
	// References are public URLs the provider fetches itself. Order is meaning for the 3D route,
	// where the first is the front view.
	References []string
	// Views and Layout come from the frozen params: `one` asks for a single composite sheet,
	// `per_view` for one picture per view — which is one paid call per view, not one call with n.
	Views  []string
	Layout string
	// Outputs is design_run.requested_outputs: how many pictures the history row expects.
	Outputs int
	// Quality is the price dial for the image route.
	Quality string
}

// Artifact is one file a provider produced, already in memory and not yet stored.
type Artifact struct {
	Bytes       []byte
	ContentType string
	// GhostView is the view this picture is believed to show. Empty leaves the store's own guess
	// in force (requested views handed out by ordinal); it is set explicitly only where the worker
	// KNOWS, i.e. on the per-view route where each call was made for a named side.
	GhostView string
	// Kind overrides the picture kind derived from the run kind. Empty = the derived one. It
	// exists for the 3D route, whose thumbnail is a raster tile standing in for a model.
	Kind string
}

// Outcome is what one pass through a provider produced.
//
// AN OUTCOME MAY ARRIVE TOGETHER WITH AN ERROR, and both halves matter. A charged failure returns
// Price with no artifacts: the money is real and has to reach the ledger. A partial success
// returns fewer artifacts than asked plus the error of the call that failed: the store files what
// came and the run closes `done · 2 of 3`, because repeating the pass would pay again for the
// pictures that already arrived.
type Outcome struct {
	Artifacts []Artifact
	// Price is the provider's own charge for this pass, in USD. Invalid (not zero) means the
	// provider did not say — "unknown" and "free" must never read the same.
	Price decimal.NullDecimal
	// RequestID is the provider's id for the call, stored on the attempt row.
	RequestID string
	// Model is the slug that actually answered.
	Model string
	// Pending marks a provider that ACCEPTED the job and will deliver later (Meshy). The worker
	// closes the attempt as `accepted` with RequestID, then collects — and a collect is free, so a
	// worker that dies between the two costs nothing to resume.
	Pending bool
}

// Provider is one paid route out of this process.
//
// Execute performs ONE pass and NEVER RETRIES: idempotency is not promised by any of the three
// transports, so a hidden retry is a second charge outside the attempt accounting that is supposed
// to count and cap it. Retrying is the queue's decision, made from the classification in
// classify.go.
type Provider interface {
	// Name is what lands in design_run_attempt.provider. It names the ROUTE, since that is what a
	// person reading a history row needs in order to know where to look.
	Name() string
	// Enabled reports whether credentials exist. Checked BEFORE an attempt is opened, so a missing
	// key closes the run for free instead of burning five paid-looking attempts on nothing.
	Enabled() bool
	// Produces lists every content type this route may hand back. The pass refuses before spending
	// anything if the sink cannot store one of them.
	Produces() []string
	Execute(ctx context.Context, job Job) (*Outcome, error)
}

// Collector is the second half of an asynchronous route. Only the 3D route implements it: Meshy
// answers a submit with a task id and builds the model for minutes afterwards.
//
// COLLECT IS FREE. That is the entire reason the two halves are separate verbs — the submit is the
// payment, the collect is a lookup, and a worker resuming after a crash must be able to do the
// second without repeating the first.
type Collector interface {
	Collect(ctx context.Context, job Job, requestID string) (*Outcome, error)
}

// Providers is the kind → route table.
//
// draft_idea IS DELIBERATELY ABSENT. The text route is executed synchronously by the handler, and
// the store excludes it from the claim predicate for the same reason: a worker that picked it up
// would pay a second time for an answer the person already has.
type Providers struct {
	// Image serves flat and render.
	Image Provider
	// Vector serves the vector kind.
	Vector Provider
	// Threed serves the threed kind.
	Threed Provider
}

// forKind returns the route for a run kind, or an error naming the kind.
func (p Providers) forKind(kind string) (Provider, error) {
	switch kind {
	case entity.DesignRunKindFlat, entity.DesignRunKindRender:
		if p.Image == nil {
			return nil, fmt.Errorf("%w: no image route is wired", errRouteMissing)
		}
		return p.Image, nil
	case entity.DesignRunKindVector:
		if p.Vector == nil {
			return nil, fmt.Errorf("%w: no vector route is wired", errRouteMissing)
		}
		return p.Vector, nil
	case entity.DesignRunKindThreed:
		if p.Threed == nil {
			return nil, fmt.Errorf("%w: no 3D route is wired", errRouteMissing)
		}
		return p.Threed, nil
	case entity.DesignRunKindDraftIdea:
		// Reachable only if the claim predicate ever stops excluding it. Refusing loudly is the
		// cheap half of that mistake; paying twice is the expensive half.
		return nil, fmt.Errorf("%w: draft_idea is executed by the handler, not by the worker", errRouteMissing)
	default:
		return nil, fmt.Errorf("%w: unknown run kind %q", errRouteMissing, kind)
	}
}
