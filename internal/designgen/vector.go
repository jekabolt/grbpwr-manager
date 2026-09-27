package designgen

import (
	"context"
	"fmt"

	"github.com/jekabolt/grbpwr-manager/internal/recraft"
	"github.com/shopspring/decimal"
)

// vectorProvider is the vector route: a Recraft V4 vector model, normally reached through the same
// OpenRouter image endpoint as the raster route (owner rule P-5), with Recraft's own API as the
// fallback transport.
type vectorProvider struct{ c *recraft.Client }

// NewVectorProvider wires the vector route. A nil client is a disabled route.
func NewVectorProvider(c *recraft.Client) Provider { return vectorProvider{c: c} }

// Name is the ROUTE, not the transport, so a history row reads the same whichever transport the
// deployment happens to be using — the transport is in the model slug beside it.
func (p vectorProvider) Name() string { return "recraft_vector" }

func (p vectorProvider) Enabled() bool { return p.c != nil && p.c.Enabled() }

// MissingCredential is the sentence the DOOR shows when the route is off — see CredentialNamer.
// TWO NAMES, BECAUSE THE ROUTE HAS TWO TRANSPORTS and either one turns it on: the default path
// borrows the OpenRouter image key, and RECRAFT_ROUTE=direct uses Recraft's own. Naming only one of
// them would send an operator to set a variable that this deployment does not read.
func (p vectorProvider) MissingCredential() string {
	return "neither OPENROUTER_API_KEY (the default transport) nor RECRAFT_API_KEY (RECRAFT_ROUTE=direct) is set"
}

func (p vectorProvider) Produces() []string { return []string{ContentTypeSVG} }

// Execute redraws an approved raster as vector.
//
// ⚠ THIS IS imageToImage, A REDRAW — NOT A TRACE. Recraft's `vectorize` produces exactly the
// "куча полигонов" the owner forbade, so the route this package takes is the vector model drawing
// the garment again with the approved raster as its composition reference. The literal reading of
// the requirement («generate a raster, then convert it to vector») leads to the forbidden verb;
// this is the deliberate departure, named here so a later reader does not "fix" it back.
//
// THE INPUT RASTER IS REQUIRED. A redraw without a source is a generation, which is a different
// press at a different price; refusing here costs nothing, while a paid call refused by the
// provider costs a round trip and reads in the log like a provider fault.
func (p vectorProvider) Execute(ctx context.Context, job Job) (*Outcome, error) {
	if !p.Enabled() {
		return nil, fmt.Errorf("%w: the vector route holds no credentials", errProviderDisabled)
	}
	if len(job.References) == 0 {
		return nil, fmt.Errorf("%w: a vector redraw needs the approved raster it redraws", recraft.ErrBadRequest)
	}

	// ONE PAID CALL, ONE LEDGER ROW (B-07), booked to the account that pays: OpenRouter on the
	// default route, Recraft only on RECRAFT_ROUTE=direct — the route name above stays the ROUTE.
	route := p.c.Route()
	h := job.beginCall(ctx, recraftBillingKey(route), p.c.Model(recraft.TierVector), 1)
	res, err := p.c.ImageToImage(ctx, recraft.ImageToImageRequest{
		Tier:   recraft.TierVector,
		Prompt: job.Prompt,
		Image:  recraft.ImageInput{URL: job.References[0]},
	})
	job.finishCall(ctx, h, vectorCallEnd(route, res, err))
	if err != nil {
		// «Оплачено, но не доехало» has a carrier here: the transport attaches what a failed call
		// cost when it knew, and Charge reads it back. ok=false is NOT a charge of zero — it means
		// nobody could say, and the ledger has to keep the difference.
		if usd, _, ok := recraft.Charge(err); ok && usd > 0 {
			return &Outcome{Price: decimal.NullDecimal{Decimal: decimal.NewFromFloat(usd), Valid: true},
				Provider: recraftBillingKey(route)}, err
		}
		return nil, err
	}

	out := &Outcome{
		Provider: recraftBillingKey(route),
		Model:    res.Model,
		Artifacts: []Artifact{{
			Bytes:       res.SVG,
			ContentType: res.ContentType,
		}},
	}
	if res.CostUSD > 0 {
		out.Price = decimal.NullDecimal{Decimal: decimal.NewFromFloat(res.CostUSD), Valid: true}
	}
	return out, nil
}
