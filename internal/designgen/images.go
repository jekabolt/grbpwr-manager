package designgen

import (
	"context"
	"fmt"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/aiprov"
	"github.com/jekabolt/grbpwr-manager/internal/aiprov/registry"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/orimages"
	"github.com/shopspring/decimal"
)

// imageProvider is the flat / render route over ONE image transport: OpenRouter's image endpoint
// (internal/orimages) today, whichever provider's transport a routed candidate names since B-13.
//
// providerKey is the BILLING transport the ledger books each call to (an entity.AIProvider* key):
// the account that pays for the picture — OpenRouter's for orimages. Empty reads as openrouter.
//
// routeModel is the candidate's route slug (admin → AI providers, image.generate) — "" = the
// transport's own default. It is the slug of a run that FROZE NONE: a frozen params.image.model is
// canonical and wins (see requested). breakers, when set, is the registry the per-call admission is
// asked of (B-13: the router's contract, one physical call at a time); nil = no breaker (the legacy
// NewImageProvider route, tests).
type imageProvider struct {
	t           ImageTransport
	providerKey string
	routeModel  string
	breakers    *registry.Registry
}

// NewImageProvider wires the raster route over the OpenRouter image client alone — no registry, no
// breaker, no route model: the shape every run had before B-13, kept for the tests and for a caller
// with no registry. A nil client is a disabled route, not a panic.
func NewImageProvider(c *orimages.Client) Provider {
	p := imageProvider{providerKey: entity.AIProviderOpenRouter}
	if c != nil {
		// ⚠ ONLY A NON-NIL CLIENT BECOMES THE INTERFACE: a typed nil *orimages.Client inside a non-nil
		// ImageTransport would pass `p.t != nil` and reach a method the nil receiver may not survive.
		p.t = c
	}
	return p
}

// billing is the ledger's provider_key for this route's calls.
func (p imageProvider) billing() string {
	if p.providerKey != "" {
		return p.providerKey
	}
	return entity.AIProviderOpenRouter
}

// Name is the provider key + "_images": what the attempt row stores, and therefore what the chooser
// reads back as «this candidate was tried». OpenRouter's stays `openrouter_images`, the word every row
// written before B-13 already carries.
func (p imageProvider) Name() string { return p.billing() + "_images" }

func (p imageProvider) Enabled() bool { return p.t != nil && p.t.Enabled() }

// MissingCredential is the sentence the DOOR shows when the route is off — see CredentialNamer.
// Both names are given because config/cfg.go binds them in that order: the dedicated one wins, the
// shared account key is the fallback, and a deployment that already translates email needs no new
// secret at all to draw pictures. Another provider's transport (commit F) has no env variable of this
// band's to name; the panel is where its key lives.
func (p imageProvider) MissingCredential() string {
	if p.billing() != entity.AIProviderOpenRouter {
		return "no key for " + p.billing() + " — set it in admin → AI providers"
	}
	return noKeySentence("openrouter", "OPENROUTER_IMAGES_API_KEY / OPENROUTER_API_KEY")
}

// requested is the slug a call of this job goes to: the run's frozen engine, else the route row's
// model, else the transport's own default. The frozen slug wins because it is what the door priced
// and the person picked; the route's model only fills the silence of a run that named none.
func (p imageProvider) requested(jobModel string) string {
	def := ""
	if p.t != nil {
		def = p.t.Model()
	}
	return firstNonEmpty(jobModel, p.routeModel, def)
}

// admit asks the breaker for ONE physical call (registry.Admit — the router's contract, B-18): false
// = the provider's breaker is open, or half-open with its one probe already out. No breaker, no
// question.
func (p imageProvider) admit() (registry.Admission, bool) {
	if p.breakers == nil {
		return registry.Admission{}, true
	}
	return p.breakers.Admit(p.billing(), entity.AICapabilityImage)
}

// endCall closes the admission of a call that WAS made: success clears the breaker; a failure is
// counted only when the registry's one rule says so (a retryable CallError that was not engaged).
func (p imageProvider) endCall(a registry.Admission, err error) {
	if p.breakers == nil {
		return
	}
	if err == nil {
		p.breakers.RecordSuccess(p.billing(), entity.AICapabilityImage, a)
		return
	}
	p.breakers.RecordFailure(p.billing(), entity.AICapabilityImage, a, err)
}

// refusedByBreaker is the failure of a call the breaker would not let out. NOT ENGAGED — nothing
// left the process — and RETRYABLE, so the worker's settle advances the chain past this candidate
// (and a one-candidate route backs off and asks again): the provider is paused, not wrong.
func (p imageProvider) refusedByBreaker() error {
	return &aiprov.CallError{
		Provider:  p.billing(),
		Code:      aiprov.CodeRateLimited,
		Engaged:   false,
		Retryable: true,
		Err:       fmt.Errorf("%s refused the call (its circuit breaker is probing or open)", p.billing()),
	}
}

// Produces is PNG and only PNG: the route asks for it explicitly, because a transparent flat needs
// a format that carries transparency and jpeg silently does not.
//
// ⚠ A Gemini / Seedream row (B-16) takes no `output_format` (NoRouteDefaults), so its answer is
// whatever raster the provider returns. The sink stores JPEG and WebP as well (bucket
// CanStoreMediaType), so nothing is lost; this list stays the GPT route's promise.
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
	// THE ENGINE'S OWN SHAPE (B-16), read off the catalogue by the slug this call goes to. A slug the
	// catalogue does not know (a custom OPENROUTER_MODEL_IMAGE) keeps today's request byte for byte.
	background, format := firstNonEmpty(job.Background, backgroundFor(job.Kind)), "png"
	// A per-run engine names its own slug; the provenance says so even when the call fails.
	requested := p.requested(job.Model)
	if row, ok := catalogueEngine(requested); ok {
		if row.NoRouteDefaults {
			// Neither key is in this slug's catalogue: the kind's `opaque` and the route's `png` are
			// OUR defaults, not the person's words, and are not sent. A stated background was already
			// refused at the door (Engine.Backgrounds); the sink files whatever raster comes back.
			background, format = strings.TrimSpace(job.Background), ""
		}
		// FREE LOCKS BEFORE THE FIRST PAID CALL, over EVERY call: the engine's reference ceiling (the
		// door counts flat / render / pattern references only as an upper bound and leaves them to
		// the client's 16, which is above Gemini's and Seedream's 14) and its `n` range. A refusal
		// halfway through the loop would come after money moved.
		for _, call := range calls {
			if row.MaxRefs > 0 && len(call.refs) > row.MaxRefs {
				return nil, fmt.Errorf("%w: %d reference pictures in one call, and %s takes at most %d",
					orimages.ErrBadRequest, len(call.refs), row.Label, row.MaxRefs)
			}
			if row.MaxN > 0 && call.n > row.MaxN {
				return nil, fmt.Errorf("%w: n=%d in one call, and %s returns at most %d",
					orimages.ErrBadRequest, call.n, row.Label, row.MaxN)
			}
		}
	}
	out := &Outcome{Model: requested, Provider: p.billing()}
	cost := decimal.Zero
	charged := false
	var usage aiprov.TokenUsage

	for i, call := range calls {
		// THE BREAKER IS ASKED PER PHYSICAL CALL, AS THE ROUTER ASKS IT (B-13): admission right
		// before the call, and exactly one end after it. A refusal is a call that never left — no
		// ledger row (nothing was called), and on call 2 of 3 the first picture comes back with the
		// refusal like any other partial.
		adm, admitted := p.admit()
		if !admitted {
			out.Price = decimal.NullDecimal{Decimal: cost, Valid: charged}
			out.Usage = usageOrNil(usage)
			return out, p.refusedByBreaker()
		}
		// ONE LEDGER ROW PER PAID CALL, OPENED BEFORE IT LEAVES (B-07): a per_view flat of three
		// views is three rows, call_no 1..3, under this one attempt. Nothing below reads it back.
		h := job.beginCall(ctx, p.billing(), requested, i+1)
		res, err := p.t.Generate(ctx, orimages.Request{
			// The per-run engine (params.image), else the route row's slug: every field empty on a run
			// that named none under a route row that names none — today's request byte for byte.
			Model:           firstNonEmpty(job.Model, p.routeModel),
			Prompt:          call.prompt,
			N:               call.n,
			Quality:         job.Quality,
			Resolution:      job.Resolution,
			AspectRatio:     job.AspectRatio,
			Background:      background,
			OutputFormat:    format,
			InputReferences: call.refs,
		})
		job.finishCall(ctx, h, imageCallEnd(res, err))
		p.endCall(adm, err)
		if res != nil {
			usage.Prompt += res.Usage.Prompt
			usage.Completion += res.Usage.Completion
		}
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
			out.Usage = usageOrNil(usage)
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
	out.Usage = usageOrNil(usage)
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

// usageOrNil — the pass's summed tokens, or nil when no call reported any.
func usageOrNil(u aiprov.TokenUsage) *aiprov.TokenUsage {
	if u == (aiprov.TokenUsage{}) {
		return nil
	}
	return &u
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
			//
			// ⚠ THE CLOTH RIDES WITH EVERY CALL, AND IT DOES NOT MAKE A CALL OF ITS OWN (J-31).
			// «One paid call per photograph» is still the whole shape of the route — the count is
			// len(References), which is what the door priced and what requested_outputs says. What
			// changes is what each call SHOWS: [this photograph, the cloth to lay on it]. The
			// second picture is not a second frame to compose from, and the prompt says so in as
			// many words (reclothCraft, «image 1 … image 2»); the caption block is numbered off
			// exactly this shape (recolorAttached), so the numbers point at what the call holds.
			//
			// A FRESH SLICE PER CALL, NOT append TO A SHARED ONE. `append([]string{u}, …)` allocates
			// its own backing array; reusing one prefix across the loop would give every call a
			// slice whose head is rewritten by the next iteration.
			calls = append(calls, imageCall{
				prompt: job.Prompt, n: 1,
				refs: append([]string{u}, job.ClothReferences...),
			})
		}
		return calls, nil
	case entity.DesignRunKindPattern:
		// A SWATCH IS BUILT FROM THE STATED COLOUR, AND ITS PICTURE — IF ANY — IS A TEXTURE
		// REFERENCE (STEP 3). Zero pictures is a legal, text-only call, exactly as the flat route
		// already sends one; more than one is refused here for the reason the door refuses it
		// (`one_texture_picture`): two textures blend into a third that neither of them is. The
		// craft paragraph is written for what actually attached (composePrompt), so a texture
		// that did not survive resolution simply makes this a plain-cloth call, not a refusal.
		if job.PatternMode == entity.DesignPatternModeSwatch {
			if len(job.References) > 1 {
				return nil, fmt.Errorf("%w: a swatch takes at most one texture reference, and this run "+
					"resolved %d", orimages.ErrBadRequest, len(job.References))
			}
			return []imageCall{{prompt: job.Prompt, n: 1, refs: job.References}}, nil
		}
		// ONE PICTURE IN, ONE TILE OUT. The door already refuses a pattern run that names anything
		// other than exactly one source; this is the same rule at the money boundary, where the
		// resolved list may be shorter than the frozen one (a media row can disappear between the
		// snapshot and the pass).
		if len(job.References) != 1 {
			return nil, fmt.Errorf("%w: a repeating tile is built from exactly one picture, and this run "+
				"resolved %d", orimages.ErrBadRequest, len(job.References))
		}
		return []imageCall{{prompt: job.Prompt, n: 1, refs: job.References}}, nil
	case entity.DesignRunKindFreeform:
		// ═══ ОДИН ВЫЗОВ, ОДНА КАРТИНКА — СЛОВО ВЛАДЕЛЬЦА, И ЗДЕСЬ ОНО ИСПОЛНЯЕТСЯ ═══
		//
		// Сколько бы картинок человек ни положил и сколько бы областей ни разметил, платный вызов
		// РОВНО ОДИН и `n` у него единица. Дверь посчитала цену по этому же числу
		// (designRequestedOutputs), поэтому всякая другая форма здесь — это либо покупка того, за
		// что не резервировали, либо плитка-плейсхолдер, которую никто не заполнит.
		//
		// ⚠ ЭТО НЕ ПЕРЕКРАС, ХОТЯ ВХОДОВ ТОЖЕ МНОГО. У перекраса N снимков — это N НЕЗАВИСИМЫХ
		// правок, каждая про свой кадр. У плейграунда все картинки — про ОДНУ просьбу: предмет,
		// фурнитура, ткань, обведённая копия и кроп области складываются в один ответ, и разбить
		// их по вызовам значило бы спросить модель N раз про N разных вещей.
		//
		// ПУСТОТА — ТЕРМИНАЛЬНЫЙ ОТКАЗ, как у соседей: дверь уже отвергла прогон без единой
		// картинки, но между снимком и проходом строку медиа могли удалить, и повторять
		// заведомо пустой запрос значит платить за отказ.
		if len(job.References) == 0 {
			// TEXT → IMAGE (tile 11): a `free` run with words and no picture is one legal call with
			// no references — the door let it through only with a non-empty ask. Every other preset
			// works ON a picture, and resolving none is still the terminal refusal above.
			if job.FreeformPreset == entity.DesignFreeformPresetFree && strings.TrimSpace(job.Prompt) != "" {
				return []imageCall{{prompt: job.Prompt, n: 1}}, nil
			}
			return nil, fmt.Errorf("%w: a playground run needs the pictures it works on, and this run "+
				"resolved none", orimages.ErrBadRequest)
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
	//
	// ⚠ И У ПЛЕЙГРАУНДА ТОЖЕ ПУСТО, ПО ТОЙ ЖЕ ПРИЧИНЕ, ЧТО У ПЕРЕКРАСА. Он работает НА КАРТИНКЕ
	// ЧЕЛОВЕКА: фон ответа обязан быть фоном исходника, а всякое значение, посланное отсюда, было
	// бы указанием этот фон сменить — при просьбе «пришей сюда пуговицу». Убрать фон у него просят
	// другим родом (cutout), у которого и провайдер другой.
	return ""
}

// firstNonEmpty returns the first argument that is not blank.
func firstNonEmpty(vs ...string) string {
	for _, v := range vs {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
