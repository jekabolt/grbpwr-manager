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

// falThreedProvider is the 3D route the owner asked for by name: hitem3d's multi-view-to-3d, reached
// through fal.ai's queue (K-10 — «для 3d как референсы должны использоваться
// hitem3d/hi3d/v3.0/multi-view-to-3d и нам нужна интеграция с fal.ai и что бы мы могли туда подавать
// наши фронт бэк и так далее»).
//
// WHAT IT DOES THAT THE MESHY ROUTE CANNOT. It sends the plates BY NAME — front, back, left, right —
// instead of as an ordered list whose first member is taken on faith to be the face of the garment.
// The bench has always known which plate is which; this is the first route that can be told.
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
func falViews(job Job) (fal.Request3D, error) {
	var req fal.Request3D
	for i, u := range job.References {
		view := ""
		if i < len(job.ReferenceViews) {
			view = job.ReferenceViews[i]
		}
		switch view {
		case entity.DesignViewFront:
			req.FrontURL = u
		case entity.DesignViewBack:
			req.BackURL = u
		case entity.DesignViewSideL:
			req.LeftURL = u
		case entity.DesignViewSideR:
			req.RightURL = u
		default:
			return fal.Request3D{}, fmt.Errorf(
				"%w: reference %d shows no addressable side of the silhouette (view %q), and a named-slot "+
					"build has nowhere to put it", fal.ErrNoFrontView, i+1, view)
		}
	}
	if req.FrontURL == "" {
		// The provider's own local refusal would say this too, but saying it here names the run's
		// own vocabulary («the front plate of the render bench») rather than the provider's field.
		return fal.Request3D{}, fmt.Errorf("%w: this run has no front plate", fal.ErrNoFrontView)
	}
	return req, nil
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
		// the day's cap never sees the spend, and nobody can say what the failures cost.
		//
		// ok = false is NOT a charge of zero. It means nobody could say, so an unpriced failure
		// still returns a nil Outcome.
		if units, ok := fal.Charge(err); ok {
			if usd := p.c.CostUSD(units); usd.IsPositive() {
				return &Outcome{
					RequestID: requestID,
					Model:     p.c.Model(),
					Price:     decimal.NullDecimal{Decimal: usd, Valid: true},
				}, err
			}
		}
		return nil, err
	}

	out := &Outcome{RequestID: res.RequestID, Model: p.c.Model()}
	if usd := p.c.CostUSD(res.BillableUnits); usd.IsPositive() {
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
