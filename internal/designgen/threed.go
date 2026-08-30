package designgen

import (
	"bytes"
	"context"
	"fmt"

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
	if len(refs) > meshy.MaxImages {
		refs = refs[:meshy.MaxImages]
	}
	id, err := p.c.Submit(ctx, meshy.Request{ImageURLs: refs, TexturePrompt: job.Prompt})
	if err != nil {
		return nil, err
	}
	// No price yet, and NULL is the schema's word for that. Meshy reports consumed_credits on the
	// finished task, so the charge is recorded by the collect — writing a zero here would say the
	// model was free.
	return &Outcome{RequestID: id, Pending: true}, nil
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
