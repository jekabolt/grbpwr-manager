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

	res, err := p.c.ImageToImage(ctx, recraft.ImageToImageRequest{
		Tier:   recraft.TierVector,
		Prompt: job.Prompt,
		Image:  recraft.ImageInput{URL: job.References[0]},
	})
	if err != nil {
		// «Оплачено, но не доехало» has a carrier here: the transport attaches what a failed call
		// cost when it knew, and Charge reads it back. ok=false is NOT a charge of zero — it means
		// nobody could say, and the ledger has to keep the difference.
		if usd, _, ok := recraft.Charge(err); ok && usd > 0 {
			return &Outcome{Price: decimal.NullDecimal{Decimal: decimal.NewFromFloat(usd), Valid: true}}, err
		}
		return nil, err
	}

	out := &Outcome{
		Model: res.Model,
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
