package designgen

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/meshy"
	"github.com/shopspring/decimal"
)

// threedProvider is the 3D route: Meshy's multi-image-to-3d, reached DIRECTLY.
//
// It is the one place the band departs from "everything through OpenRouter", and not by
// preference: OpenRouter has no 3D modality at all — `3d` is not a value its catalogue accepts —
// so there is nothing there to route to.
type threedProvider struct{ c *meshy.Client }

// NewThreedProvider wires the 3D route. A nil client is a disabled route.
func NewThreedProvider(c *meshy.Client) Provider { return threedProvider{c: c} }

func (p threedProvider) Name() string { return "meshy" }

func (p threedProvider) Enabled() bool { return p.c != nil && p.c.Enabled() }

// MissingCredential is the sentence the DOOR shows when the route is off — see CredentialNamer.
func (p threedProvider) MissingCredential() string { return "MESHY_API_KEY is not set" }

// Produces names BOTH artifacts, because the pass refuses up front unless the sink can store every
// one of them: the model itself, and the raster thumbnail that stands in for it wherever a list
// has to draw a tile (a GLB is not something a list view can render).
func (p threedProvider) Produces() []string { return []string{ContentTypeGLB, ContentTypePNG} }

// Execute SUBMITS and returns immediately with the provider's task id.
//
// THE SPLIT INTO SUBMIT AND COLLECT IS THE WHOLE POINT. The submit is the payment; the collect is
// a free lookup. Closing the attempt as `accepted` with the task id the instant the submit returns
// means a worker that dies during the minutes Meshy takes to build the model resumes for nothing —
// the next pass reads the id off the attempt row and collects, instead of buying a second model.
func (p threedProvider) Execute(ctx context.Context, job Job) (*Outcome, error) {
	if !p.Enabled() {
		return nil, fmt.Errorf("%w: MESHY_API_KEY is not set", errProviderDisabled)
	}
	refs := job.References
	if len(refs) == 0 {
		return nil, fmt.Errorf("%w: a turntable needs at least the front view", meshy.ErrImageCount)
	}
	// ORDER IS MEANING: the provider reads image_urls[0] as the primary frontal reference, which
	// is why buildJob sorts the bench plates front, back, side_l, side_r before anything else.
	//
	// ⚠ AND ORDER IS NOT PERMISSION TO CUT. This used to be `refs = refs[:meshy.MaxImages]` — a
	// silent trim, without even the image route's warn line. A turntable built from four of the
	// nine pictures the snapshot lists is a model nobody can account for: the run closes `done`,
	// the history says nine went, and which five were dropped is written down nowhere.
	//
	// meshy.Submit refuses the count itself, LOCALLY, before the request leaves — so the refusal
	// costs no money, arrives with a sentinel the classifier knows is not weather, and names the
	// ceiling. The person narrows the run with fix_targets / fix_slot_ids, which is the selection
	// mechanism the bench already has, instead of the provider choosing for them in silence.
	id, err := p.c.Submit(ctx, meshy.Request{
		ImageURLs:     refs,
		TexturePrompt: textureSteer(ctx, job),
	})
	if err != nil {
		return nil, err
	}
	// No price yet, and NULL is the schema's word for that. Meshy reports consumed_credits on the
	// finished task, so the charge is recorded by the collect — writing a zero here would say the
	// model was free.
	return &Outcome{RequestID: id, Pending: true}, nil
}

// textureSteer is the run's prompt CUT DOWN to what `texture_prompt` may carry.
//
// WHY A CUT IS LEGITIMATE HERE, AND ONLY HERE. This band's rule is that a ceiling REFUSES and never
// trims quietly: a trimmed value is a claim nobody made, frozen where nobody can see it was
// trimmed. `texture_prompt` is the one field that rule does not fit, because it is not the order —
// it is a HINT ABOUT THE SURFACE. The substance of a 3D job travels as four approved pictures, and
// those are never cut (see Execute: the count is refused, not trimmed). A steer that reaches the
// provider one sentence short still describes the same garment; a run refused for it would refuse
// the whole 3D route on every card whose prompt is longer than 600 characters — that is, all of
// them.
//
// ⚠ AND THE ROUTE IS DEAD WITHOUT THIS. meshy.Submit now refuses TexturePrompt above
// meshy.MaxTexturePrompt locally, before the network, and classify() reads that refusal as a
// terminal provider_bad_request. The run's composed prompt (ask + garment + fit + colour +
// presentation + every reference line) is several times that on real data, so every realistic 3D
// run died instantly at the door. Not a regression — before the local ceiling it died after five
// paid-looking attempts as provider_unavailable — but a dead route either way.
//
// THE CUT LANDS ON A SECTION BOUNDARY when there is one. composePrompt joins its sections with a
// blank line, so cutting there means the steer carries WHOLE sections — the ask, the garment, the
// fit, the colour — instead of half a sentence about a reference. Only when the first section
// alone is already too long does it fall back to cutting at the rune ceiling.
//
// ⚠ THE PROPER FIX IS ELSEWHERE, and it is a short steer composed FROM THE COLOUR AND 3D PARAMS
// rather than a cut of the whole prompt — the surface is what `texture_prompt` steers, and the run
// already knows its colourway recipe and its presentation. That belongs in snapshot.go (a field of
// its own on Job, filled by buildJob), which this task does not own; until then this keeps the
// route alive and says so out loud.
func textureSteer(ctx context.Context, job Job) string {
	steer := strings.TrimSpace(job.Prompt)
	if len([]rune(steer)) <= meshy.MaxTexturePrompt {
		return steer
	}
	cut := string([]rune(steer)[:meshy.MaxTexturePrompt])
	if at := strings.LastIndex(cut, "\n\n"); at > 0 {
		cut = cut[:at]
	}
	cut = strings.TrimSpace(cut)
	// LOUD, because a silent trim is what this comment argues against everywhere else: the operator
	// has to be able to see that the provider was told less than the run says.
	slog.Default().WarnContext(ctx, "3D: the texture steer was cut to the provider's ceiling",
		slog.Int("run_id", job.RunID), slog.Int("prompt_runes", len([]rune(steer))),
		slog.Int("ceiling_runes", meshy.MaxTexturePrompt), slog.Int("sent_runes", len([]rune(cut))))
	return cut
}

// Collect is the FREE half: one status lookup and, once the task has succeeded, the bytes.
//
// THE BYTES ARE TAKEN IMMEDIATELY AND THE LINKS ARE NEVER STORED. Meshy's result urls live three
// days; a stored link is a model that quietly stops existing on the fourth. meshy.Result carries
// no url at all, by construction, and this function keeps that property by writing straight into
// buffers on its way to the bucket.
func (p threedProvider) Collect(ctx context.Context, job Job, requestID string) (*Outcome, error) {
	if !p.Enabled() {
		return nil, fmt.Errorf("%w: MESHY_API_KEY is not set", errProviderDisabled)
	}
	var model, thumb bytes.Buffer
	res, err := p.c.Await(ctx, requestID, meshy.Sink{Model: &model, Thumbnail: &thumb})
	if err != nil {
		// «PAID, AND NOTHING CAME OF IT» HAS A CARRIER HERE, exactly as it does on the vector route
		// (see vector.go): the transport attaches what a failed call consumed when it knew, and
		// Charge reads it back. Without this the money of a terminal failure — a SUCCEEDED task
		// with no glb, a model past the size cap — vanished: the attempt closed with a NULL price,
		// the day's cap never saw the spend, and nobody could say what the failures cost.
		//
		// ok = false is NOT a charge of zero. It means nobody could say, and the ledger has to keep
		// that difference, so an unpriced failure still returns a nil Outcome.
		if credits, ok := meshy.Charge(err); ok {
			if usd := p.c.CostUSD(credits); usd.IsPositive() {
				// THE OUTCOME TRAVELS BESIDE THE ERROR, which is what Outcome documents and what
				// settle() expects: the price is written first, from the provider's own answer, and
				// the run then fails with no artifacts. RequestID rides along so the ledger line
				// names the task the money went to.
				return &Outcome{
					RequestID: requestID,
					Price:     decimal.NullDecimal{Decimal: usd, Valid: true},
				}, err
			}
		}
		return nil, err
	}

	out := &Outcome{RequestID: res.TaskID}
	if usd := p.c.CostUSD(res.ConsumedCredits); usd.IsPositive() {
		out.Price = decimal.NullDecimal{Decimal: usd, Valid: true}
	} else {
		out.Price = decimal.NullDecimal{}
	}
	out.Artifacts = append(out.Artifacts, Artifact{
		Bytes:       model.Bytes(),
		ContentType: ContentTypeGLB,
		Kind:        entity.DesignPictureKindThreed,
	})
	if thumb.Len() > 0 {
		// The thumbnail is the tile the band shows for a threed picture. It is a courtesy, not what
		// was paid for, so its absence never fails the pass.
		out.Artifacts = append(out.Artifacts, Artifact{
			Bytes:       thumb.Bytes(),
			ContentType: ContentTypePNG,
			Kind:        entity.DesignPictureKindThreed,
		})
	}
	return out, nil
}
