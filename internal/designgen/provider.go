package designgen

import (
	"context"
	"fmt"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/aiprov"
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
	// ContentTypeMP4 / ContentTypeWEBM — the video route's output (B-32) and the bucket's second
	// video container; both take the sink's VIDEO door (bucket.UploadContentVideo).
	ContentTypeMP4  = "video/mp4"
	ContentTypeWEBM = "video/webm"
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
	// Kind is the run kind: flat | render | threed | … | video. draft_idea never reaches here.
	Kind string
	// Prompt is the composed instruction: the ask, the garment description, the fit, the roles and
	// notes of the references. Composed from the SNAPSHOT rather than from today's card, because
	// an async run must send what it was launched with, not what the card says when it happens to
	// be picked up.
	Prompt string
	// References are public URLs the provider fetches itself. Order is meaning for the 3D route,
	// where the first is the front view.
	References []string
	// ClothReferences are the CLOTH TEXTURES that travel WITH every photograph of a recolour — the
	// pictures of `params.colour.fabrics`, in statedCloths order. Empty on every other kind, and
	// empty on a recolour that names no cloth (a plain colour change, which is the route as it
	// existed before J-31).
	//
	// ⚠ THEY ARE A SEPARATE LIST BECAUSE `References` IS ALSO THE COUNT OF PAID CALLS. A recolour
	// makes one call per photograph (imageCalls) and the run row's requested_outputs was computed
	// from the same number at the door; folding the cloth into References would have made the
	// worker buy one more picture than the run was priced for, silently. The cloth is not another
	// photograph to recolour — it is the SECOND IMAGE OF EVERY CALL.
	//
	// ⚠ AND THAT IS WHY THE CAPTIONS OF A RECOLOUR ARE NOT NUMBERED OFF `References`. Each call
	// shows the model [one photograph, the cloths]; a caption block numbered «image 1..N» over N
	// photographs described a call that never happens. See buildJob's recolorAttached.
	ClothReferences []string
	// ReferenceViews names the SIDE each reference shows, POSITIONALLY: the i-th entry belongs to
	// References[i], and is empty where the run has no view for that picture (a moodboard-style
	// reference, a fabric swatch, an uploaded photograph nobody labelled).
	//
	// ⚠ IT TRAVELS BECAUSE A POSITION IS NOT A NAME, AND THE ROUTE HAS TO STATE WHAT IT KNOWS. The
	// meshy family on fal, which is what FAL_MODEL_3D defaults to today, takes an ORDERED LIST and
	// infers the front from position zero. fal's hitem3d, the slug
	// the owner named first, takes NAMED SLOTS (front_image_url, back_image_url, …) and is one
	// variable away.
	//
	// ⚠ THE DEFAULT BEING A LIST-SHAPED MODEL IS THE REASON THIS FIELD MATTERS MORE, NOT LESS. The
	// name is what lets falViews REFUSE a run whose front plate did not survive media resolution
	// instead of sending the back as the face of the garment; the flattening to an ordered list
	// then happens in one line of the transport, front first, rather than by a positional rule
	// spread across the band. See falViews.
	//
	// A ROUTE THAT NEEDS THE NAMES MUST REFUSE AN UNNAMED PICTURE RATHER THAN GUESS ONE. An empty
	// entry is legal here and means «this run does not claim to know»; inventing `front` for it
	// would buy a model of a garment turned the wrong way round, and the history could not tell
	// that run from an honest one.
	ReferenceViews []string
	// Views and Layout come from the frozen params: `one` asks for a single composite sheet,
	// `per_view` for one picture per view — which is one paid call per view, not one call with n.
	Views  []string
	Layout string
	// DetailNames names the requested details POSITIONALLY: the i-th entry belongs to the i-th
	// `detail` in Views. An entry is empty when the frozen snapshot could not name that slot.
	//
	// IT IS NOT DERIVABLE FROM Views, WHICH IS THE WHOLE REASON IT TRAVELS. A view key says
	// `detail` and nothing else, so two details are two indistinguishable calls — on the per_view
	// route, two paid calls with a byte-identical prompt.
	DetailNames []string
	// SurfaceSteer is the ONLY text a 3D route sends: a short hint about the SURFACE of the
	// garment — its stated colour, the words describing the cloth, and how the turntable is
	// presented. It is composed in buildJob from the frozen snapshot, exactly like Prompt.
	//
	// ⚠ IT IS A SECOND, NARROWER COMPOSITION AND NOT A CUT OF Prompt, AND THAT IS THE FIX. What
	// used to travel to `texture_prompt` was the run's whole prompt truncated to the provider's
	// ceiling — which meant the texturing stage was handed the ASK («build the turntable of this
	// top»), the GARMENT NOTE («crossed straps on the back»), the FIT and the numbered reference
	// captions. A texturing stage has no spatial understanding to place «on the back» with: it
	// stamps what it is told wherever it is texturing, which is the single most plausible textual
	// route to the owner's complaint of a back that came out at the front. Silhouette travels as
	// PICTURES; only the surface travels as words.
	//
	// EMPTY IS LEGAL AND MEANS «this run states nothing about its surface» — the provider's own
	// defaults are the honest answer to that, and an empty hint is not the same as no hint.
	//
	// ⚠ IT IS COMPOSED WITHIN A CEILING, NOT MERELY SHORT BY NATURE. Nothing in the band bounds
	// `colour.words`, `fabrics[].words` or the number of cloths, and both providers refuse a hint
	// past 600 runes TERMINALLY — see surfaceSteer, which is where the bound lives and where the
	// argument for bounding rather than refusing is made.
	SurfaceSteer string
	// Window is the GENERATION WINDOW of this job, or nil for every job that sends whole frames.
	//
	// ⚠ ЕГО ЧИТАЕТ НЕ ПРОВАЙДЕР, А ВОРКЕР, И ИМЕННО ПОЭТОМУ ОНО ЕДЕТ В Job. Маршрут получил кроп
	// области и вернёт кроп; кадром это станет ПОСЛЕ вызова, когда ответ вклеят обратно по этим
	// координатам (window.go). Координаты обязаны быть теми же, по которым вырезали, — значит они
	// замораживаются один раз при сборке задания и путешествуют вместе с ним, а не пересчитываются
	// по params во второй раз.
	Window *GenerationWindow
	// PatternMode is params.pattern.mode of a pattern run: "" or "image" (a tile from ONE source
	// photograph) or "swatch" (a swatch built from the stated colour, with zero or one TEXTURE
	// reference). It travels because imageCalls — the money boundary — counts the pictures by it;
	// empty on every other kind.
	PatternMode string
	// Outputs is design_run.requested_outputs: how many pictures the history row expects.
	Outputs int
	// Quality is the price dial for the image route.
	Quality string
	// Model / AspectRatio / Resolution / Background — the per-run engine of an image route
	// (params.image, phase 2). Empty = the route's own configuration, i.e. every run frozen
	// before the field. Resolution is the dial of an engine that prices by size, not by quality.
	Model       string
	AspectRatio string
	Resolution  string
	Background  string
	// FreeformPreset is params.freeform.preset of a playground run; empty on every other kind.
	FreeformPreset string
	// ThreedTexture / ThreedPBR / ThreedQuality — params.threed.texture / pbr / quality of a 3D
	// run (PLAYGROUND phase 2, B-09): '' | on | off, '' | on | off, '' | standard | detailed.
	// Empty = today's constants, i.e. every bench-plate run and every run frozen before the fields.
	// The 3D routes read them through threedJobOptions; the fal collect prices a detailed build by
	// them.
	ThreedTexture string
	ThreedPBR     string
	ThreedQuality string
	// ThreedSurfaceHint — params.threed.surface_hint as the run froze it (B-24): with the three above,
	// the FOUR values the 3D chooser hands ThreedUnread for each candidate, and the same four the
	// worker's pre-submit check reads off the params (threedUnreadAtSubmit) — one reading, two askers.
	// It is the person's raw words; the text a route SENDS is SurfaceSteer.
	ThreedSurfaceHint string
	// ThreedReservedUSD — what the door reserved for ONE build of this 3D run (price_estimate over
	// requested_outputs); invalid when the row carries no estimate. The fal collect compares the
	// booked charge with it before saying the reservation was short (G-02 r2, Codex 5).
	ThreedReservedUSD decimal.NullDecimal
	// RouteReservedUSD — the same figure for an extend / inpaint / video run (one output): what the
	// door reserved. The fal collect compares the booked charge with it (G-03, Codex 5 + 10); the
	// video submit compares runblob's price with it (RUNBLOB_VIDEO_CEILING_USD, B-32).
	RouteReservedUSD decimal.NullDecimal

	// VideoModel / VideoDuration / VideoAspectRatio — a video run's frozen params.video (B-32): the
	// Kling slug the door froze ("" = the route's own), the clip length in seconds (0 = the route's
	// own, 5), and the aspect ratio NEAREST to the source picture's (16:9 | 9:16 | 1:1; "" when the
	// media row states no size — the provider's default then). Empty on every other kind.
	VideoModel       string
	VideoDuration    int
	VideoAspectRatio string

	// Extend is an extend run's plan (kind=extend, PLAYGROUND phase 3), frozen at build time BEFORE
	// the money: the source size after the 3 MP cap, the canvas, where the source sits in it, the
	// expansion per side. The route sends it, the composite reads it back — one plan, read twice.
	// nil on every other kind.
	Extend *ExtendPlan

	// Inpaint is a mask retouch's plan (kind=inpaint, phase 3), frozen before the money; InpaintMask
	// is the mask crop as a PNG data URI, the same size as References[0] (the picture crop). The mask
	// travels HERE and never in References: it is not a picture of the run, it is where to paint.
	Inpaint     *InpaintPlan
	InpaintMask string

	// Recorder books this job's PHYSICAL provider calls into the AI ledger (B-07): one row per
	// transport invocation, opened before it (see ledger.go). Set by the worker after StartAttempt,
	// scoped to that attempt — for a collect, to the SUBMIT's attempt, whose row the collect prices.
	// nil records nothing: every route behaves exactly as it did before the ledger existed.
	Recorder CallRecorder
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
	// Flags — pixel labels read off the picture (flat route, 0397: FlagGrey). A label, never a refusal.
	Flags []string
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
	// Pending marks a provider that ACCEPTED the job and will deliver later (the fal 3D queue, the runblob video). The worker
	// closes the attempt as `accepted` with RequestID, then collects — and a collect is free, so a
	// worker that dies between the two costs nothing to resume.
	Pending bool
	// Provider is the BILLING transport of this pass (an entity.AIProvider* key: the account that
	// pays — a model routed through OpenRouter is "openrouter"), and Usage the tokens its calls reported,
	// summed; nil when none did. Both are PROVENANCE for the ledger's side of the pass, set by the
	// route: recordAttempt does not read them, and the attempt row stays the money truth of the run.
	Provider string
	Usage    *aiprov.TokenUsage
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

// CredentialNamer is a route that can say WHICH SETTING would turn it on.
//
// ⚠ IT EXISTS BECAUSE THE REFUSAL A PERSON READS IS THE PRE-FLIGHT'S, NEVER THE PROVIDER'S OWN.
// Every Execute in this package opens with a sentence that names its variable ("FAL_KEY is not
// set"), and not one of them is ever reached with the key missing: preflight refuses first, at the
// door, and its message could only say the route's NAME. So the owner who types a key into the
// dashboard pressed GENERATE and was told "the provider for this run kind is not configured: fal" —
// which does not tell them whether the thing they just typed was the thing that was missing.
//
// OPTIONAL, so a route that has nothing to say is still a legal Provider; missingCredential falls
// back to a generic sentence rather than to silence.
type CredentialNamer interface {
	// MissingCredential names the unset setting, as a sentence a person can act on:
	// "FAL_KEY is not set".
	MissingCredential() string
}

// missingCredential asks a route to name the setting it lacks.
func missingCredential(p Provider) string {
	if n, ok := p.(CredentialNamer); ok {
		if s := n.MissingCredential(); s != "" {
			return s
		}
	}
	return "no API key is configured for this route"
}

// PromptCarrier is a route whose OUTGOING TEXT is not the job's composed prompt.
//
// ⚠ IT EXISTS SO THE HISTORY ROW CAN STOP GUESSING. design_run.prompt is written by the worker
// before the paid call and is read by a person as «what this run told the model». On the image
// routes that is exactly what Job.Prompt is. On the 3D routes it never was: the only text a
// turntable provider accepts is a short surface hint (`texture_prompt`), and one of the two model
// families accepts NO text at all — so the column was showing an operator a composed paragraph
// that the provider had not been sent, beside a price that was real. A run panel that attributes
// words to a spend that never carried them is worse than an empty column, because it looks like
// evidence.
//
// OPTIONAL, and its absence means «this route sends Job.Prompt», which is the truth for the image
// routes and the reason they implement nothing.
//
// ⚠ THE ANSWER IS THE ROUTE'S, NEVER THE KIND'S. A `switch kind` in the dispatcher would be a
// second opinion about a fact only the route holds — which model family is configured, and whether
// that family has a text field — and the two would drift the first time a slug moved.
type PromptCarrier interface {
	// SentPrompt returns the text this route will hand the provider for this job. Empty means «no
	// words travel», which is a claim in its own right and not a missing value.
	SentPrompt(job Job) string
}

// recordedPrompt is what design_run.prompt must say about a job: what the route is going to send.
func recordedPrompt(prov Provider, job Job) string {
	if pc, ok := prov.(PromptCarrier); ok {
		return pc.SentPrompt(job)
	}
	return job.Prompt
}

// Collector is the second half of an asynchronous route. The 3D and video routes implement it:
// fal answers a 3D submit with a request id and builds the model for minutes afterwards.
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
	// Image serves flat, render, recolor, pattern and freeform. Since B-13 app.go wires the ROUTED
	// provider here (NewRoutedImageProvider): a Chooser, whose concrete candidate is what each pass
	// pays and records (dispatch.go).
	Image Provider
	// Threed serves the threed kind.
	Threed Provider
	// Cutout serves the cutout kind — background removal, a SEGMENTER rather than a generator.
	//
	// It is a route of its own and not a fifth passenger on Image for the one reason that decides
	// every entry in this table: a different paid endpoint with a different unit of money and a
	// different name in the history row. Nothing about the prompt would tell them apart, because
	// this route has no prompt at all.
	Cutout Provider
	// Outpaint serves kind=extend (tile 9, fal's outpaint route) and Fill serves kind=inpaint (tile
	// 10's mask route, fal's fill route) — PLAYGROUND phase 3. Same fal client and FAL_KEY as the
	// cut-out; different slugs, bodies and tariffs, so different routes.
	Outpaint Provider
	Fill     Provider
	// Video serves kind=video (B-32): runblob's Kling image-to-video, a Provider + Collector like the
	// 3D routes (the submit pays, the collect is free). One provider today, so no Chooser: the
	// panel's `video.generate` route names the model, not the transport.
	Video Provider

	// Also — providers the worker CONSTRUCTED but does not route any kind to directly: since B-24 the
	// boot-time 3D provider (fal at the env slug), because the Threed slot is the panel's route (named
	// "threed", never an attempt row's name). It is never chosen for a fresh run; it exists so a job it
	// ACCEPTED — under any route row, before any route edit — is collected by it,
	// for free: the collect goes to the provider the accepted attempt names (byName), never to
	// whoever the route puts first today.
	Also []Provider
}

// byName finds the provider the worker knows under an attempt row's name: the six slots, then Also.
// It is how a paid, accepted job reaches the provider that accepted it (REVIEW-A4 #2).
func (p Providers) byName(name string) (Provider, bool) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, false
	}
	for _, prov := range append([]Provider{p.Image, p.Threed, p.Cutout, p.Outpaint, p.Fill, p.Video}, p.Also...) {
		if prov != nil && prov.Name() == name {
			return prov, true
		}
	}
	return nil, false
}

// forKind returns the route for a run kind, or an error naming the kind.
func (p Providers) forKind(kind string) (Provider, error) {
	switch kind {
	// ⚠ ЧЕТЫРЕ РОДА НА ОДНОМ МАРШРУТЕ, И ЭТО НЕ ЛЕНЬ. Флэт, рендер, перекрас и паттерн — ОДИН
	// платный эндпоинт (POST /api/v1/images) с ОДНИМ ключом и одним провайдером в строке истории.
	// Различаются они не транспортом, а ПРОМПТОМ и тем, КАКИЕ КАРТИНКИ уходят в КАКОЙ вызов, — и
	// обе эти вещи уже живут в этом пакете (composePrompt, imageCalls). Второй Provider с тем же
	// клиентом внутри дал бы второе имя провайдера в истории для одних и тех же денег.
	//
	// ПЛЕЙГРАУНД ЕДЕТ ТУДА ЖЕ, И ПО ТОЙ ЖЕ ПРИЧИНЕ: `freeform` — это тот же POST /api/v1/images с
	// тем же ключом, отличающийся ровно промптом и набором ссылок. Пятый род в этой строке не
	// добавляет системе ни одного нового платного маршрута.
	case entity.DesignRunKindFlat, entity.DesignRunKindRender,
		entity.DesignRunKindRecolor, entity.DesignRunKindPattern,
		entity.DesignRunKindFreeform:
		if p.Image == nil {
			return nil, fmt.Errorf("%w: no image route is wired", errRouteMissing)
		}
		return p.Image, nil
	case entity.DesignRunKindCutout:
		if p.Cutout == nil {
			return nil, fmt.Errorf("%w: no cutout route is wired", errRouteMissing)
		}
		return p.Cutout, nil
	case entity.DesignRunKindExtend:
		if p.Outpaint == nil {
			return nil, fmt.Errorf("%w: no outpaint route is wired", errRouteMissing)
		}
		return p.Outpaint, nil
	case entity.DesignRunKindInpaint:
		if p.Fill == nil {
			return nil, fmt.Errorf("%w: no fill route is wired", errRouteMissing)
		}
		return p.Fill, nil
	case entity.DesignRunKindThreed:
		if p.Threed == nil {
			return nil, fmt.Errorf("%w: no 3D route is wired", errRouteMissing)
		}
		return p.Threed, nil
	case entity.DesignRunKindVideo:
		if p.Video == nil {
			return nil, fmt.Errorf("%w: no video route is wired", errRouteMissing)
		}
		return p.Video, nil
	case entity.DesignRunKindDraftIdea:
		// Reachable only if the claim predicate ever stops excluding it. Refusing loudly is the
		// cheap half of that mistake; paying twice is the expensive half.
		return nil, fmt.Errorf("%w: draft_idea is executed by the handler, not by the worker", errRouteMissing)
	default:
		return nil, fmt.Errorf("%w: unknown run kind %q", errRouteMissing, kind)
	}
}

// ─────────────────────────── THE PRE-FLIGHT, IN ONE PLACE ───────────────────────────

// KindRefusal is a pre-flight verdict in the shape a CALLER OUTSIDE THIS PACKAGE needs: the
// sentence, plus the machine reason that is the very same code the history row would have carried
// had the run been allowed to fail its way to it.
//
// It wraps the sentinel rather than replacing it, so classify() — and everything else that asks
// errors.Is — keeps working on the worker's own path. RefusalReason is a METHOD rather than an
// exported field read by an importer, so the API layer can pick the reason up through a one-method
// interface and not depend on this package at all.
type KindRefusal struct {
	// Kind is the run kind that was asked for.
	Kind string
	// Reason is the machine word: output_not_storable, kind_not_available…
	Reason string
	err    error
}

func (r *KindRefusal) Error() string         { return r.err.Error() }
func (r *KindRefusal) Unwrap() error         { return r.err }
func (r *KindRefusal) RefusalReason() string { return r.Reason }

// preflight is EVERY refusal that happens before an attempt row exists — that is, before any money
// can move: no route wired, no credentials, nowhere to put what the route produces. It returns the
// provider as well, because the pass needs it immediately afterwards and looking it up twice is how
// two lookups come to disagree.
//
// ⚠ THIS IS THE SINGLE EXPRESSION BEHIND BOTH GUARDS — the worker's, and the door's (see
// PreflightKind). It is computed from the ROUTE'S OWN Produces() crossed with the SINK'S OWN
// Accepts(), never from a list of kind names: a hand-written list of "kinds that do not work" would
// keep refusing after the sink learned the type, and would keep accepting after a route started
// returning a new one. Because this is a computation, the refusal disappears BY ITSELF the day the
// sink can store the output — with no edit here and none at the door.
func (p Providers) preflight(sink MediaSink, kind string) (Provider, error) {
	refuse := func(err error) error {
		return &KindRefusal{Kind: kind, Reason: classify(err).Code, err: err}
	}
	prov, err := p.forKind(kind)
	if err != nil {
		return nil, refuse(err)
	}
	if err := routeReady(sink, prov); err != nil {
		return nil, refuse(err)
	}
	return prov, nil
}

// routeReady is the provider half of the pre-flight: credentials, and somewhere to put what it
// returns. preflight asks it of the kind's route; the resume of an accepted job asks it of the STORED
// provider (dispatch.go resumeRoute) — one expression, whichever provider the pass is about to use.
func routeReady(sink MediaSink, prov Provider) error {
	if !prov.Enabled() {
		// THE SENTENCE NAMES THE SETTING, because this is the sentence that reaches the screen —
		// see CredentialNamer. "the provider for this run kind is not configured: fal" and
		// "…: fal — FAL_KEY is not set" are the same refusal; only the second one tells the person
		// who just typed a key whether they typed the right one.
		return fmt.Errorf("%w: %s — %s", errProviderDisabled, prov.Name(), missingCredential(prov))
	}
	if sink == nil {
		// A worker cannot reach this (New refuses a nil bucket); a caller assembling the gate by
		// hand can. "I cannot tell whether the output is storable" must not read as "it is".
		return fmt.Errorf("%w: %s has no sink to store its output", errSinkUnsupported, prov.Name())
	}
	for _, ct := range prov.Produces() {
		if !sink.Accepts(ct) {
			// ⚠ THE GUARD THAT SAVES REAL MONEY. A route whose output the sink cannot store would
			// otherwise be paid for on every pass and refused by the upload every single time —
			// five times per run, for as long as the mismatch lives.
			return fmt.Errorf("%w: %s returns %s", errSinkUnsupported, prov.Name(), ct)
		}
	}
	return nil
}

// PreflightKind is the DOOR'S copy of the question the pass asks first: would a run of this kind be
// refused for free, before any money moved?
//
// It exists so the handler can refuse BEFORE it reserves the day's budget instead of after. The
// refusal it hands back is not a second opinion — it is the same call, on the same providers and
// the same sink, that the worker will make a tick later; the two cannot drift because there is only
// one of them. A nil error means the pass would get as far as paying.
func (w *Worker) PreflightKind(kind string) error {
	_, err := w.providers.preflight(w.sink, kind)
	return err
}
