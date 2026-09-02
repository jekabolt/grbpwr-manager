package designgen

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/fal"
	"github.com/shopspring/decimal"
)

// Spellings DESIGN_THREED_PROVIDER accepts. They live here, next to the two routes they choose
// between, so «which word turns on which provider» is one expression rather than a string compared
// in app.go and documented in a comment somewhere else.
const (
	ThreedProviderFal   = "fal"
	ThreedProviderMeshy = "meshy"
)

// falThreedProvider is the 3D route reached through fal.ai's queue (K-10 — «для 3d как референсы
// должны использоваться hitem3d/hi3d/v3.0/multi-view-to-3d и нам нужна интеграция с fal.ai и что бы
// мы могли туда подавать наши фронт бэк и так далее»).
//
// WHAT IT DOES THAT THE DIRECT MESHY ROUTE CANNOT. It hands the transport the plates BY NAME —
// front, back, left, right — instead of as an ordered list whose first member is taken on faith to
// be the face of the garment. The bench has always known which plate is which; this is the route
// that can be told.
//
// ⚠ WHETHER THE NAMES SURVIVE THE WIRE IS THE MODEL'S BUSINESS, NOT THIS ROUTE'S. hitem3d takes
// named slots; `meshy/v7/multi-image-to-3d` — the configured default since the owner asked for it —
// takes an unnamed list, and the fal client flattens front-first. Keeping the statement named ALL
// THE WAY DOWN TO THE TRANSPORT is what makes that flattening one line in one file instead of a
// property of the whole band.
//
// IT IS THE SAME TWO-HALVED SHAPE as the Meshy route (Execute submits and pays, Collect looks up and
// is free), because it is the same problem, and because the worker's resume logic reads that shape.
type falThreedProvider struct{ c *fal.Client }

// NewFalThreedProvider wires the fal 3D route. A nil client is a disabled route, not a panic.
func NewFalThreedProvider(c *fal.Client) Provider { return falThreedProvider{c: c} }

// Name is what lands in design_run_attempt.provider. It names the ROUTE, not the model: the slug is
// configuration and may move, while «this money went to fal» is the fact a person reconciling a
// bill needs.
func (p falThreedProvider) Name() string { return ThreedProviderFal }

func (p falThreedProvider) Enabled() bool { return p.c != nil && p.c.Enabled() }

// MissingCredential is the sentence the DOOR shows when the route is off — see CredentialNamer. It
// is the same wording fal.ErrNotConfigured carries, because a person who reads one and then the
// other must not have to work out that they are the same fact.
func (p falThreedProvider) MissingCredential() string { return "FAL_KEY is not set" }

// Produces names BOTH artifacts, because the pass refuses up front unless the sink can store every
// one of them: the model itself, and the raster thumbnail that stands in for it wherever a list has
// to draw a tile.
func (p falThreedProvider) Produces() []string { return []string{ContentTypeGLB, ContentTypePNG} }

// Execute SUBMITS and returns immediately with the provider's request id.
//
// THE SPLIT INTO SUBMIT AND COLLECT IS THE WHOLE POINT, exactly as on the Meshy route. The submit is
// the payment; the collect is a free lookup. Closing the attempt as `accepted` with the request id
// the instant the submit returns means a worker that dies during the minutes hitem3d takes resumes
// for nothing instead of buying a second model.
func (p falThreedProvider) Execute(ctx context.Context, job Job) (*Outcome, error) {
	if !p.Enabled() {
		return nil, fmt.Errorf("%w: %s", errProviderDisabled, p.MissingCredential())
	}
	req, err := falViews(job)
	if err != nil {
		return nil, err
	}
	// THE ONLY WORDS THIS ROUTE SENDS, and they describe the SURFACE — see Job.SurfaceSteer. On a
	// model family with nowhere to put them (hitem3d) the transport drops them; that is why the
	// history row asks AcceptsTexturePrompt rather than assuming the text travelled.
	req.TexturePrompt = job.SurfaceSteer
	if job.SurfaceSteer != "" && !p.c.AcceptsTexturePrompt() {
		// ⚠ THE ONE PLACE THE TWO KINDS OF SILENCE ARE TOLD APART — see SentPrompt on why the
		// COLUMN does not tell them apart and must not. «This run said nothing about its surface»
		// and «this model has nowhere to put what the run said» are one empty column and two
		// different facts, and the second one is a configuration a person can change.
		slog.Default().InfoContext(ctx, "3D: the configured model has no text field, so the run's "+
			"surface steer was composed and not sent",
			slog.Int("run_id", job.RunID), slog.String("model", p.c.Model()),
			slog.Int("steer_runes", len([]rune(job.SurfaceSteer))))
	}
	id, err := p.c.Submit(ctx, req)
	if err != nil {
		return nil, err
	}
	// No price yet, and NULL is the schema's word for that. fal reports what a request billed on
	// the RESULT fetch, so the charge is recorded by the collect — writing a zero here would say
	// the model was free.
	return &Outcome{RequestID: id, Model: p.c.Model(), Pending: true}, nil
}

// falViews turns the run's plates into the provider's NAMED slots.
//
// ⚠ IT MAPS BY VIEW KEY AND NEVER BY POSITION, AND THAT IS THE ENTIRE REASON THIS ROUTE EXISTS. An
// ordinal rule («the first one is the front») is true only while the list happens to be sorted and
// complete; the moment a card has a back plate and no front — an ordinary state halfway through a
// studio session — it silently sends the BACK as the face of the garment, buys a model turned the
// wrong way round, and closes the run `done`. The bench already knows which side each plate is;
// threedPictures has already narrowed the list to silhouette plates and already refused a run with
// no front. This function reads that knowledge instead of re-deriving it.
//
// AN UNNAMED PICTURE IS REFUSED, NOT GUESSED AT. Reaching here it would mean the narrowing above
// let through something that is not a plate, and inventing a side for it is how the run becomes
// unaccountable.
//
// A SIDE CLAIMED TWICE IS REFUSED, NOT OVERWRITTEN. `req.FrontURL = u` used to take the LAST plate
// of a repeated view in silence, which is a paid build of a garment nobody chose: two plates of the
// front are two different drawings, and picking one of them by list order is not a decision this
// function is entitled to make. Today's bench cannot produce a duplicate (a slot is unique by
// view × kind × colourway), but a RERUN executes a FROZEN snapshot, and snapshots frozen before the
// colourway scope existed legally carry two plates of one view. The refusal costs nothing — it
// happens before the submit — and it names the side, which is the one thing a person needs in order
// to narrow the run.
func falViews(job Job) (fal.Request3D, error) {
	var req fal.Request3D
	claim := func(dst *string, view, u string) error {
		if *dst != "" {
			return fmt.Errorf("%w: the %s of this garment is claimed by two input plates, and a "+
				"named-slot build has one place for it — narrow the run to a single %s plate",
				errDuplicateView, view, view)
		}
		*dst = u
		return nil
	}
	for i, u := range job.References {
		view := ""
		if i < len(job.ReferenceViews) {
			view = job.ReferenceViews[i]
		}
		var err error
		switch view {
		case entity.DesignViewFront:
			err = claim(&req.FrontURL, view, u)
		case entity.DesignViewBack:
			err = claim(&req.BackURL, view, u)
		case entity.DesignViewSideL:
			err = claim(&req.LeftURL, view, u)
		case entity.DesignViewSideR:
			err = claim(&req.RightURL, view, u)
		default:
			err = fmt.Errorf(
				"%w: reference %d shows no addressable side of the silhouette (view %q), and a named-slot "+
					"build has nowhere to put it", fal.ErrNoFrontView, i+1, view)
		}
		if err != nil {
			return fal.Request3D{}, err
		}
	}
	if req.FrontURL == "" {
		// The provider's own local refusal would say this too, but saying it here names the run's
		// own vocabulary («the front plate of the render bench») rather than the provider's field.
		return fal.Request3D{}, fmt.Errorf("%w: this run has no front plate", fal.ErrNoFrontView)
	}
	return req, nil
}

// SentPrompt is what this route will actually put in front of the provider — see PromptCarrier.
//
// ⚠ IT ANSWERS WITH THE MODEL FAMILY'S TRUTH, NOT WITH THE JOB'S WISH. hitem3d's payload has no
// text field at all, so on that slug the steer is composed, ignored and must be recorded as the
// nothing it was: the run panel showing an operator a paragraph the provider never received is a
// false statement about money that has already been spent, and it is the exact defect measured on
// beta (both 3D runs there sent four urls and not one word, while the history showed the full
// composed prompt).
//
// ⚠ AND THE EMPTY STRING IT RETURNS CARRIES TWO DIFFERENT FACTS — «this run states nothing about
// its surface» and «this model has nowhere to put what it stated» — WHICH IS DELIBERATE, AND THE
// ARGUMENT IS NOT «close enough». The column is read as ONE question: what words did this run put
// in front of the provider it paid? Both cases answer «none», truthfully, and any wording that
// distinguished them would have to be prose OUR side invented and stored in a column a person reads
// as the model's instruction — a second class of text in a field whose whole repair was that it
// stops containing text nobody sent. The pair of facts is separable where it is actionable and
// nowhere else: Execute logs it, because «the model has no text field» is a CONFIGURATION a person
// can change (DESIGN_THREED_PROVIDER / FAL_MODEL_3D), while «the colourway says nothing» is a
// property of the run, visible in its own params on the same panel.
func (p falThreedProvider) SentPrompt(job Job) string {
	if !p.c.AcceptsTexturePrompt() {
		return ""
	}
	return job.SurfaceSteer
}

// Collect is the FREE half: one status lookup, then — once the request has completed — the bytes.
//
// THE BYTES ARE TAKEN IMMEDIATELY AND THE LINKS ARE NEVER STORED, for the reason the Meshy route
// gives: a provider's result urls expire, and a stored link is a model that quietly stops existing.
func (p falThreedProvider) Collect(ctx context.Context, job Job, requestID string) (*Outcome, error) {
	if !p.Enabled() {
		return nil, fmt.Errorf("%w: %s", errProviderDisabled, p.MissingCredential())
	}
	var model, thumb bytes.Buffer
	res, err := p.c.Await(ctx, requestID, fal.Sink{Model: &model, Thumbnail: &thumb})
	if err != nil {
		// «PAID, AND NOTHING CAME OF IT» HAS A CARRIER HERE, exactly as on the Meshy and vector
		// routes: the transport attaches what a failed call billed when it knew, and Charge reads
		// it back. Without this the money of a terminal failure — a COMPLETED request with no
		// model file, a model past the size cap — vanishes: the attempt closes with a NULL price,
		// the day's ledger never sees the spend, and nobody can say what the failures cost.
		//
		// ok = false is NOT a charge of zero. It means nobody could say, so an unpriced failure
		// still returns a nil Outcome.
		//
		// ⚠ AND IT IS PRICED AGAINST THE MODEL THAT WAS POLLED, NOT THE ONE CONFIGURED NOW — see
		// the success path below for the whole argument. A billed failure on a RECOVERED build is
		// money about an old model.
		if units, ok := fal.Charge(err); ok {
			model := fal.ChargedModel(err)
			if usd := p.c.CostUSDFor(model, units); usd.IsPositive() {
				return &Outcome{
					RequestID: requestID,
					Model:     model,
					Price:     decimal.NullDecimal{Decimal: usd, Valid: true},
				}, err
			}
		}
		return nil, err
	}

	// ⚠ THE MODEL COMES OFF THE RESULT, NEVER OFF THE CLIENT, AND THAT IS A MONEY DECISION. The
	// transport finds a build submitted before a model move under the slug it was BOUGHT at (see
	// fal.locateRequest), so `p.c.Model()` here would name a model this build never touched — and
	// with no tariff configured it would then book an hitem3d turntable, estimated at $0.60, at
	// meshy's $1.20. That silently rewrites the money of a run frozen before the deploy: the panel
	// ends up showing a price_actual that disagrees with its own price_estimate for a reason nobody
	// can reconstruct from the row. What a build was worth is a property of the request, not of the
	// configuration that outlived it.
	out := &Outcome{RequestID: res.RequestID, Model: res.Model}
	if usd := p.c.CostUSDFor(res.Model, res.BillableUnits); usd.IsPositive() {
		out.Price = decimal.NullDecimal{Decimal: usd, Valid: true}
	} else {
		out.Price = decimal.NullDecimal{}
	}
	if res.UnitsAssumed {
		// ⚠ SAID OUT LOUD, EVERY TIME. The ledger gets a number either way — a paid build recorded
		// as free is the worse lie — but «the provider named this» and «we assumed one unit» are
		// different claims, and the second one must never harden into the first by being invisible.
		// The knob that makes the assumption right is FAL_UNIT_USD.
		slog.Default().WarnContext(ctx, "3D: fal reported no billable units; the attempt's price is this "+
			"deployment's own per-request estimate, not the provider's charge",
			slog.Int("run_id", job.RunID), slog.String("request_id", requestID),
			slog.String("price_usd", out.Price.Decimal.String()), slog.String("knob", "FAL_UNIT_USD"))
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
