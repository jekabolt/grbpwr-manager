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

// MissingCredential is the sentence the DOOR shows when the route is off — see CredentialNamer.
// Both names are given because config/cfg.go binds them in that order: the dedicated one wins, the
// shared account key is the fallback, and a deployment that already translates email needs no new
// secret at all to draw pictures.
func (p imageProvider) MissingCredential() string {
	return "OPENROUTER_IMAGES_API_KEY (or OPENROUTER_API_KEY) is not set"
}

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
	calls, err := imageCalls(job)
	if err != nil {
		// A LOCAL REFUSAL, BEFORE ANY MONEY MOVES. It exists for the two routes whose input is a
		// particular picture rather than a pile of context: a recolour with nothing to recolour and
		// a pattern built from no swatch are requests we can judge here, for free, and re-sending
		// either one unchanged cannot end differently.
		return nil, err
	}
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
			InputReferences: call.refs,
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
	// ─── DOES THE TILE ACTUALLY TILE. The measurement, the reasoning behind it and the reason it
	// may not fail the run are all in seam.go. Here it does one thing: it returns the artifacts
	// TOGETHER WITH the complaint, which is the shape settle() already implements for a partial
	// success — the picture is filed and the attempt row carries `pattern_not_seamless`.
	if job.Kind == entity.DesignRunKindPattern {
		if v := seamCheck(out.Artifacts[0].Bytes); !v.Seamless() {
			// THE FIGURES TRAVEL WITH THE COMPLAINT. «It does not tile» with no numbers beside it
			// is an accusation nobody can check against the picture they are looking at.
			return out, fmt.Errorf("%w: %d×%d tile — wrap seam %.1f across / %.1f down against a "+
				"tolerance of %.1f, edge bias %.1f against a tolerance of %.1f; the picture was kept",
				errPatternNotSeamless, v.Width, v.Height, v.Horizontal, v.Vertical, v.WrapLimit(),
				v.EdgeBias, v.BiasLimit())
		}
	}
	return out, nil
}

// imageCall is one paid request: a prompt, how many variants of it, the pictures THIS call shows the
// model, and the view it stands for.
//
// ⚠ THE REFERENCES BELONG TO THE CALL, NOT TO THE JOB, AND THAT MOVE IS THE WHOLE RECOLOUR ROUTE.
// Every call used to receive the run's entire reference list, which is right for a flat and for a
// render — they compose from all of it — and wrong for a recolour, whose instruction is «give me
// back THIS photograph with one thing changed». Handed a second picture, the model composes: the
// answer is a similar frame, not the same one, and nothing in the history distinguishes that from a
// correct result. Four on-model photographs are therefore four calls of one picture each, not four
// calls of four.
type imageCall struct {
	prompt string
	n      int
	refs   []string
	view   string
}

// imageCalls turns a job into the calls it costs.
//
// It returns an error for the routes whose input is a PARTICULAR PICTURE: a recolour with nothing to
// recolour, a pattern with no swatch. That refusal happens before the loop that spends money, and it
// is terminal by nature — re-sending the identical empty request cannot end differently.
func imageCalls(job Job) ([]imageCall, error) {
	switch job.Kind {
	case entity.DesignRunKindRecolor:
		// ONE PAID CALL PER PHOTOGRAPH, EACH SHOWING ONLY ITS OWN. The owner asked for «фото
		// реальное на модели с разных сторон», i.e. several frames of one garment; each of them is
		// a separate edit of a separate picture, and there is no cheaper honest shape — a single
		// call with four references returns ONE image, which would answer none of the four asks.
		if len(job.References) == 0 {
			return nil, fmt.Errorf("%w: a recolour needs the photograph it recolours — add the on-model "+
				"pictures to this run", orimages.ErrBadRequest)
		}
		calls := make([]imageCall, 0, len(job.References))
		for _, u := range job.References {
			// THE VIEW IS LEFT EMPTY DELIBERATELY. A recoloured photograph is not addressed to a
			// side of the bench: the person uploaded frames of their own choosing, the run never
			// claimed which side each one shows, and a ghost view invented here would put the
			// picture into a slot nobody pointed at.
			calls = append(calls, imageCall{prompt: job.Prompt, n: 1, refs: []string{u}})
		}
		return calls, nil
	case entity.DesignRunKindPattern:
		// ONE PICTURE IN, ONE TILE OUT. The door already refuses a pattern run that names anything
		// other than exactly one source; this is the same rule at the money boundary, where the
		// resolved list may be shorter than the frozen one (a media row can disappear between the
		// snapshot and the pass).
		if len(job.References) != 1 {
			return nil, fmt.Errorf("%w: a repeating tile is built from exactly one picture, and this run "+
				"resolved %d", orimages.ErrBadRequest, len(job.References))
		}
		return []imageCall{{prompt: job.Prompt, n: 1, refs: job.References}}, nil
	}

	if job.Layout == layoutPerView && len(job.Views) > 0 {
		// ⚠ ПОДПИСЬ ВЫЗОВА И ВИД КАДРА — РАЗНЫЕ ВЕЩИ, И РАСХОДЯТСЯ ОНИ НАМЕРЕННО. В промпт уходит
		// РАЗЛИЧАЮЩАЯ подпись («detail — collar»), иначе два вызова на две детали были бы одним и
		// тем же платным запросом дважды; а в `view` кладётся ЧИСТЫЙ КЛЮЧ ВИДА, потому что это
		// GhostView — метка, которую стор сверяет со своим словарём видов, и имя детали в ней
		// сделало бы её нечитаемой.
		labels := viewCallLabels(job.Views, job.DetailNames)
		calls := make([]imageCall, 0, len(job.Views))
		for i, v := range job.Views {
			calls = append(calls, imageCall{
				prompt: viewPrompt(job.Prompt, labels[i]), n: 1, refs: job.References, view: v,
			})
		}
		return calls, nil
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
	return []imageCall{{prompt: job.Prompt, n: 1, refs: job.References}}, nil
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
	switch kind {
	case entity.DesignRunKindFlat:
		return "opaque"
	case entity.DesignRunKindPattern:
		// A TILE IS CLOTH, AND CLOTH HAS NO HOLES. A transparent region inside a repeating tile is a
		// hole that repeats: it shows through at the same spot in every cell of the grid, which is
		// the one artefact a person will not be able to explain and will not be able to remove. The
		// value is STATED rather than left to `auto` for the reason the flat route states it — «auto»
		// is the model deciding a thing we have an opinion about.
		return "opaque"
	}
	// A render, a recolour and a turntable are pictures of a garment in a scene, and keep the
	// provider's own default. On the recolour route that matters twice over: the background of the
	// answer must be the background of the SOURCE PHOTOGRAPH, and any value we sent here would be an
	// instruction to change it.
	return ""
}
