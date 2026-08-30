package designgen

import (
	"context"
	"fmt"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/orimages"
	"github.com/shopspring/decimal"
)

// imageProvider is the flat / render route: OpenRouter's image endpoint (internal/orimages).
type imageProvider struct{ c *orimages.Client }

// NewImageProvider wires the raster route. A nil client is a disabled route, not a panic.
func NewImageProvider(c *orimages.Client) Provider { return imageProvider{c: c} }

func (p imageProvider) Name() string { return "openrouter_images" }

func (p imageProvider) Enabled() bool { return p.c != nil && p.c.Enabled() }

// Produces is PNG and only PNG: the route asks for it explicitly, because a transparent flat needs
// a format that carries transparency and jpeg silently does not.
func (p imageProvider) Produces() []string { return []string{ContentTypePNG} }

// Execute runs one pass over a raster job.
//
// ONE PAID CALL PER VIEW ON THE per_view ROUTE, AND THAT IS NOT A CHOICE THIS CODE MAKES — the
// provider's `n` returns n VARIANTS OF ONE PROMPT, so three views are three prompts and three
// charges. The cheap route (`one`) asks for a single composite and the human splits it afterwards
// with SplitDesignPicture, which is why the layout is frozen into the run's params instead of
// being a preference.
//
// A PARTIAL RESULT IS RETURNED, NOT DISCARDED. If the second of three calls fails, the first
// picture and its price come back together with the error, and the caller files what arrived. The
// alternative — failing the pass — would repeat the calls that already succeeded and pay for them
// a second time.
func (p imageProvider) Execute(ctx context.Context, job Job) (*Outcome, error) {
	if !p.Enabled() {
		return nil, fmt.Errorf("%w: the image route holds no API key", errProviderDisabled)
	}

	// ⚠ THE TAIL IS NOT CUT. It used to be — with a warn line nobody reads — and that made the run
	// LIE ABOUT ITSELF: the frozen snapshot said twenty-four pictures went to the model, the model
	// saw sixteen, and the run closed `done`. Nothing anywhere recorded which eight were dropped,
	// so the provenance of the picture that came back was unrecoverable forever after. A log line
	// is not a record: it is not on the run, the author never sees it, and it is gone in a week.
	//
	// THE REFUSAL IS THE PROVIDER'S OWN, AND DELIBERATELY SO. orimages checks the count LOCALLY,
	// before the round trip, so this costs nothing — no paid call is made — and the ceiling lives
	// in exactly one place, next to the client that knows it. A copy of the number here is a
	// second number, and two numbers disagree the moment one of them is edited.
	refs := job.References

	calls := imageCalls(job)
	out := &Outcome{Model: p.c.Model()}
	cost := decimal.Zero
	charged := false

	for _, call := range calls {
		res, err := p.c.Generate(ctx, orimages.Request{
			Prompt:          call.prompt,
			N:               call.n,
			Quality:         job.Quality,
			Background:      backgroundFor(job.Kind),
			OutputFormat:    "png",
			InputReferences: refs,
		})
		// THE PRICE IS TAKEN FIRST, BEFORE THE ERROR IS EVEN LOOKED AT. Both the empty-data case
		// and the undecodable-image case return a *Result carrying Usage TOGETHER WITH the error:
		// the call was billed, and a ledger that records only successes under-reports spend in
		// exactly the case where the spend was wasted.
		if res != nil && res.Usage.Cost > 0 {
			cost = cost.Add(decimal.NewFromFloat(res.Usage.Cost))
			charged = true
		}
		if res != nil && res.Model != "" {
			out.Model = res.Model
		}
		if err != nil {
			out.Price = decimal.NullDecimal{Decimal: cost, Valid: charged}
			return out, err
		}
		for _, img := range res.Images {
			out.Artifacts = append(out.Artifacts, Artifact{
				Bytes:       img.Bytes,
				ContentType: img.MediaType,
				GhostView:   call.view,
			})
		}
	}
	out.Price = decimal.NullDecimal{Decimal: cost, Valid: charged}
	if len(out.Artifacts) == 0 {
		// Reachable only through a call that reported success with an empty image list, which the
		// client already refuses — kept because "success with nothing to file" must never close a
		// run as done.
		return out, fmt.Errorf("%w: the image route produced no pictures", orimages.ErrNoImages)
	}
	return out, nil
}

// imageCall is one paid request: a prompt, how many variants of it, and the view it stands for.
type imageCall struct {
	prompt string
	n      int
	view   string
}

// imageCalls turns a job into the calls it costs.
func imageCalls(job Job) []imageCall {
	if job.Layout == layoutPerView && len(job.Views) > 0 {
		calls := make([]imageCall, 0, len(job.Views))
		for _, v := range job.Views {
			calls = append(calls, imageCall{prompt: viewPrompt(job.Prompt, v), n: 1, view: v})
		}
		return calls
	}
	// `one` (and anything unspecified, which the store also reads as one sheet): ONE prompt and
	// ONE picture.
	//
	// ⚠ n IS NOT requested_outputs, AND READING IT THAT WAY IS AN OVERCHARGE. `layout=one` with
	// three views is a single composite sheet carrying all three — that is precisely what the
	// store's composite_views rule says — so a run whose requested_outputs happens to equal the
	// view count would, on that reading, buy three whole composites instead of one. There is no
	// «variants» field in the frozen params today; when one arrives it belongs here, named, rather
	// than inferred from a count that means something else.
	//
	// A composite carries several views and therefore has no single one; leaving the view empty
	// lets the store's own rule (no ghost guess for a composite) stand.
	return []imageCall{{prompt: job.Prompt, n: 1}}
}

// backgroundFor names the background the model must produce.
//
// IT USED TO ASK FOR TRANSPARENCY, AND THAT IS NOW A 400 ON EVERY FLAT RUN. The default model is
// gpt-image-2 (the owner's choice), and its catalogue lists background as `auto | opaque` only —
// `transparent` is not a value it knows. The old code sent it anyway, and the old test pinned it,
// so this package was green while the flat path was dead on arrival.
//
// WHAT THE OLD ARGUMENT WAS RIGHT ABOUT, AND WHAT IT COSTS US. «A flat with a white rectangle
// behind it cannot be laid over the technical sheet» is true, and we are giving that up on
// purpose, not by accident:
//
//   - the owner's own prompt orders the opposite in words — «black vector line art on a plain
//     white background», «white seamless background». Asking the API for transparency while the
//     prompt asks for white is one order contradicting itself;
//   - the raster is not the end of the road. It goes to a vector model next, and a vector carries
//     no background at all — so the sheet is composed from something that never had a rectangle;
//   - a technical sheet is printed on white paper, where a white plate is invisible anyway.
//
// So: `opaque` is STATED rather than omitted. Omitting would leave `auto`, and «auto» is the model
// deciding a thing we have an opinion about — the same silence that hid the old bug.
//
// A render is a picture of a garment in a scene and keeps the provider's own default.
func backgroundFor(kind string) string {
	if kind == entity.DesignRunKindFlat {
		return "opaque"
	}
	return ""
}
