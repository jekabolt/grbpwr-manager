package designgen

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/shopspring/decimal"

	"github.com/jekabolt/grbpwr-manager/internal/aiprov"
	"github.com/jekabolt/grbpwr-manager/internal/aiprov/registry"
	"github.com/jekabolt/grbpwr-manager/internal/aiprov/runblob"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
)

// ═══ THE VIDEO ROUTE (B-32): ONE PICTURE → A 5-SECOND CLIP, runblob's Kling image-to-video ═══════
//
// OWNER, 28.09: «в runblob есть и видео и фото — делай и то и то». This is the video half: the
// playground's «Image to Video» tile picks ONE picture of the card and words, and a clip comes back as
// a media of the card (kind=video, purpose video.generate, provider runblob).
//
// IT IS THE SAME TWO-HALVED SHAPE AS THE 3D ROUTES, because it is the same problem: Execute SUBMITS
// (one POST to the slug's family — /v1/kling/generate, /v1/kling/o1-video/generate,
// /v1/kling/o3-video/generate or /v1/seedance/generate (H5, videoFamilyOf) — answered at once with an
// id and, on the Kling families — unlike fal or Meshy — THE PRICE),
// closes the attempt `accepted` and returns; Collect is a FREE status read and, on `completed`, the
// download. A worker that dies during the minutes Kling takes resumes by the id and buys nothing twice.
//
// THE MONEY, IN ONE PARAGRAPH. runblob prices a job AT SUBMIT («price: "0.2900"») and REFUNDS a job it
// fails («Your balance is automatically refunded on any failed task»). So:
//
//   - a 201 is the purchase: the ledger's `accepted` row carries the price as the provider's own
//     number (cost_source provider) from that moment;
//   - `completed` books that price on the attempt (delivered), `failed` books NOTHING (the attempt
//     closes `failed`, the ledger row `failed` with a KNOWN zero, Meshy's refund rule);
//   - `completed` with nothing usable is money that is real and a clip that is not: `unknown`, priced;
//   - a submit that LEFT and was not answered (a post-write break, a 408, a 5xx other than a bare
//     503, a 2xx with no id) may have been queued and charged: errVideoSubmitUnconfirmed, terminal,
//     `unknown` — never resubmitted, never handed to another provider (D-16). fal's rule.
//
// THE PRICE TRAVELS IN THE LOCATOR. The status read does not repeat it, the accepted attempt row books
// no price (the store's rule: the collect books the charge), and a collect after a restart holds only
// the stored request id — so the id is stored WITH its price, `kling#<uuid>#0.2900`, exactly as a fal
// locator carries the slug a build was bought under: what a job cost is a property of the request, not
// of anything that outlives it. Since H5 the first field is the FAMILY the job lives on (kling |
// kling-o1 | kling-o3 | seedance): the status read goes to the family's own path, and a locator
// written before H5 (`kling#…`) is read exactly as it was written.
//
// THE RESERVE is RUNBLOB_VIDEO_CEILING_USD (Config.VideoCeiling, default 1.50): what the door holds
// for one clip. runblob's price is the truth; a price above the reserve is LOGGED at the submit, never
// refused — after the 201 the money has moved (fal's booked-vs-reserved rule).
//
// ⚠ SEEDANCE STATES NO PRICE AT SUBMIT (seedance.json: the 201 is {task_uuid, model, status, output,
// error}; the money is a per-second hold settled after the result): its clip is booked with no number,
// exactly as Kling's «calculating» is, and the owner reconciles it against runblob's cabinet.

// Constants of the route.
const (
	// DefaultVideoModel — Kling's own default and the one the seeded route row ('' model) means.
	DefaultVideoModel = "kling_2.5_turbo"
	// VideoDurationSeconds — the clip length this route sells: five seconds, the cheapest Kling tier.
	VideoDurationSeconds = 5
	// VideoMaxPromptRunes — Kling's prompt ceiling (1–2500 characters); the door caps `ask` by it too.
	VideoMaxPromptRunes = 2500
	// videoLocatorSep separates the family, the id and the price in an accepted locator.
	videoLocatorSep = "#"
	// videoMaxBytes — the most a downloaded clip may weigh: the bucket's own video ceiling
	// (bucket.maxVideoPayloadBytes, 50 MiB). Over it the sink would refuse the file after the money;
	// refusing here says so with the price beside it.
	videoMaxBytes = 50 << 20
	// videoDownloadTimeout bounds the fetch of one clip from the CDN.
	videoDownloadTimeout = 2 * time.Minute
)

// VideoRoute — what the panel's `video.generate` route and this deployment say about a clip: the
// route row's Kling slug ("" = DefaultVideoModel) and the most one clip reserves. ONE value, read
// live by the door (SetDesignVideoRoute) and by the worker at every submit, from the same function.
type VideoRoute struct {
	Model      string
	CeilingUSD decimal.Decimal
}

// ─── the families (H5): the slug decides the path, the body and the locator's tag ───

// Video slugs that are NOT on the main Kling endpoint (runblob-specs/kling.json endpoints[1], [2];
// runblob-specs/seedance.json models).
const (
	VideoModelKlingO1          = "kling_o1"
	VideoModelKlingO3          = "kling_o3"
	VideoModelKlingO3Pro       = "kling_o3_pro"
	VideoModelSeedanceMini     = "seedance-2.0-mini"
	VideoModelSeedanceFace     = "doubao-seedance-2.0-face"
	VideoModelSeedanceFastFace = "doubao-seedance-2.0-fast-face"
	VideoModelSeedance25Face   = "doubao-seedance-2.5-face"
)

// videoFamily — one runblob video family: the locator's tag and the adapter's path.
type videoFamily struct {
	tag  string
	path string
}

var (
	videoFamKling    = videoFamily{tag: "kling", path: runblob.FamilyKling}
	videoFamKlingO1  = videoFamily{tag: "kling-o1", path: runblob.PathKlingO1Video}
	videoFamKlingO3  = videoFamily{tag: "kling-o3", path: runblob.PathKlingO3Video}
	videoFamSeedance = videoFamily{tag: "seedance", path: runblob.PathSeedance}
	// videoFamilies — every family a stored locator may name.
	videoFamilies = []videoFamily{videoFamKling, videoFamKlingO1, videoFamKlingO3, videoFamSeedance}

	// klingMainModel — a slug of the main endpoint: kling_ and a version digit (kling_2.5_turbo,
	// kling_3_pro, …). kling_o* is NOT here: the omni models live on their own endpoints, and an omni
	// slug nobody documents (kling_o5) must be refused, not sent to the main endpoint.
	klingMainModel = regexp.MustCompile(`^kling_[0-9]`)

	// seedanceModels — the four documented Seedance models (a CLOSED list: the media field differs
	// by model — Mini's first_frame_url, the Face models' image_with_roles — so an unknown one would
	// be a body we cannot build).
	seedanceModels = map[string]bool{
		VideoModelSeedanceMini: true, VideoModelSeedanceFace: true,
		VideoModelSeedanceFastFace: true, VideoModelSeedance25Face: true,
	}

	// videoModelsKnown — the refusal sentence's list.
	videoModelsKnown = "kling_<version> (kling_2.5_turbo, kling_3_pro, … — the 14 of the main endpoint), " +
		VideoModelKlingO1 + ", " + VideoModelKlingO3 + ", " + VideoModelKlingO3Pro + ", " +
		VideoModelSeedanceMini + ", " + VideoModelSeedanceFace + ", " + VideoModelSeedanceFastFace + ", " +
		VideoModelSeedance25Face
)

// videoFamilyOf — the family a slug is bought on; false for a slug runblob does not serve as video.
func videoFamilyOf(model string) (videoFamily, bool) {
	switch model = strings.TrimSpace(model); {
	case model == VideoModelKlingO1:
		return videoFamKlingO1, true
	case model == VideoModelKlingO3, model == VideoModelKlingO3Pro:
		return videoFamKlingO3, true
	case seedanceModels[model]:
		return videoFamSeedance, true
	case klingMainModel.MatchString(model):
		return videoFamKling, true
	}
	return videoFamily{}, false
}

// IsVideoModel — whether the video route can buy a clip with this slug (the door asks it before it
// reserves anything, so a slug the worker would refuse never takes the day's money).
func IsVideoModel(model string) bool {
	model = strings.TrimSpace(model)
	fam, ok := videoFamilyOf(model)
	if !ok {
		return false
	}
	// NOT OFFERED (Codex REVIEW-H #5, #7; catalogue_runblob.go lists neither): a Seedance submit
	// carries no price (billing is a per-second hold settled later, behind a cabinet JWT the panel
	// does not hold), so every clip would close as a $0 run; the Kling Motion models need a reference
	// VIDEO the body does not carry. The families stay built (the worker collects a stored run), the
	// door refuses a new one.
	if fam == videoFamSeedance || strings.Contains(model, "_motion") {
		return false
	}
	return true
}

// VideoModelsKnown — the slugs IsVideoModel accepts, as a sentence names them.
func VideoModelsKnown() string { return videoModelsKnown }

// videoTransport — what the route needs of the runblob adapter, and nothing more: the key question
// and the two verbs (the submit that spends, the status read that is free). *runblob.Client is the
// one implementation in this binary; the tests stand a recording fake at this seam, because the
// adapter's host is a constant by design (an editable base url is a place a key can be sent) and its
// own goldens pin the wire.
type videoTransport interface {
	Enabled() bool
	Submit(ctx context.Context, family string, body map[string]any) (*runblob.Submission, error)
	Status(ctx context.Context, family, id string) (*runblob.Generation, error)
}

// videoProvider — see the section doc.
type videoProvider struct {
	c     videoTransport
	route func() VideoRoute
	// fetch downloads a finished clip; nil = the http fetch below. A seam for the tests.
	fetch func(ctx context.Context, rawURL string) ([]byte, error)
}

// NewVideoProvider wires the video route over the runblob adapter. A nil client is a disabled route;
// a nil route function is the transport's own default at the default ceiling.
func NewVideoProvider(c *runblob.Client, route func() VideoRoute) Provider {
	// A nil *Client is a valid, permanently disabled adapter (its Enabled is nil-safe), so the typed
	// nil inside the interface is not a trap here: Enabled() answers false, and nothing else is called.
	return &videoProvider{c: c, route: route}
}

// Name is what lands in design_run_attempt.provider: the ROUTE, i.e. the account the money went to.
func (p *videoProvider) Name() string { return entity.AIProviderRunblob }

func (p *videoProvider) Enabled() bool { return p != nil && p.c != nil && p.c.Enabled() }

// MissingCredential — the door's sentence. runblob has NO env variable (R-05): the key lives only in
// admin → AI providers, and the sentence says exactly that instead of naming a variable nobody can set.
func (p *videoProvider) MissingCredential() string {
	return "no key for runblob — set it in admin → AI providers (runblob has no environment variable)"
}

// Produces names the one artifact: the mp4. The pass refuses up front unless the sink stores it.
func (p *videoProvider) Produces() []string { return []string{ContentTypeMP4} }

// routeOf — the live route, or the defaults.
func (p *videoProvider) routeOf() VideoRoute {
	var r VideoRoute
	if p.route != nil {
		r = p.route()
	}
	r.Model = strings.TrimSpace(r.Model)
	return r
}

// modelFor — the slug this submit buys: the FROZEN one (the door wrote the route row's slug into
// params.video.model when the run was created), else the route row's now, else Kling's own.
func (p *videoProvider) modelFor(job Job) string {
	if m := strings.TrimSpace(job.VideoModel); m != "" {
		return m
	}
	if m := p.routeOf().Model; m != "" {
		return m
	}
	return DefaultVideoModel
}

// Execute SUBMITS and returns immediately with the locator. THE SUBMIT IS THE PAYMENT.
func (p *videoProvider) Execute(ctx context.Context, job Job) (*Outcome, error) {
	if !p.Enabled() {
		return nil, fmt.Errorf("%w: %s", errProviderDisabled, p.MissingCredential())
	}
	// ─── LOCAL REFUSALS, BEFORE THE WIRE AND BEFORE THE LEDGER ROW: nothing is bought here ───
	body, model, err := p.body(job)
	if err != nil {
		return nil, err
	}
	// THE SUBMIT IS THE PAYMENT, SO IT OPENS THE LEDGER ROW (B-07).
	h := job.beginCall(ctx, entity.AIProviderRunblob, model, 1)
	fam, _ := videoFamilyOf(model) // body() refused every slug videoFamilyOf does not know
	sub, serr := p.c.Submit(ctx, fam.path, body)
	if serr != nil {
		if sub != nil && sub.ID != "" {
			// ⚠ ACCEPTED, WITH AN UNREADABLE PRICE (the adapter's partial answer): the job IS running
			// under this id, and dropping it would buy a second clip on the next pass. It is
			// collected like any other; only its price is unknown until the owner reconciles.
			slog.Default().WarnContext(ctx, "video: runblob accepted the clip with a price this "+
				"deployment could not read; the job is collected and booked with no number",
				slog.Int("run_id", job.RunID), slog.String("generation_id", sub.ID), slog.String("err", serr.Error()))
			locator := videoLocator(fam, sub.ID, decimal.NullDecimal{})
			job.finishCall(ctx, h, acceptedEnd(locator))
			return &Outcome{RequestID: locator, Model: model, Pending: true, Provider: entity.AIProviderRunblob}, nil
		}
		job.finishCall(ctx, h, runblobSubmitEnd(serr))
		return nil, videoSubmitError(serr)
	}
	locator := videoLocator(fam, sub.ID, sub.PriceUSD)
	end := acceptedEnd(locator)
	if sub.PriceUSD.Valid {
		// THEIR NUMBER, AT SUBMIT: the provider's own price is on the ledger row from the moment the
		// job is bought, cost_source provider — a restart between here and the collect loses no money.
		end.CostUSD, end.CostSource = sub.PriceUSD, entity.AICostProvider
	}
	job.finishCall(ctx, h, end)
	logVideoCeilingBreach(ctx, job, locator, sub.PriceUSD)
	// No price on the OUTCOME: the store books an accepted attempt's price as the collect's (queue.go
	// «an accepted submit carries no price»), and a refunded `failed` must book nothing at all.
	return &Outcome{RequestID: locator, Model: model, Pending: true, Provider: entity.AIProviderRunblob}, nil
}

// body — the request as the slug's family documents it, the source picture always as a public url:
//
//	kling     (kling.json endpoints[0]) prompt, model, image_url, duration "5" | 5 (Kling 3), aspect_ratio;
//	kling-o1  (endpoints[1]) prompt, images_urls [the picture], duration "5", aspect_ratio — NO model:
//	          the endpoint IS kling_o1;
//	kling-o3  (endpoints[2]) prompt, model, images_url [the picture] (the spec's own spelling, not
//	          O1's), duration "5", aspect_ratio;
//	seedance  (seedance.json) model, prompt, resolution 720p (the documented default, sent so the
//	          per-second rate is the one we meant), duration 5 (an integer), the picture as the FIRST
//	          FRAME — first_frame_url on Mini, image_with_roles [{url, role: first_frame}] on the Face
//	          models — and aspect_ratio: the picture's on Mini and 2.0, `adaptive` on 2.5 (its rule:
//	          frame roles force adaptive).
//
// Single-picture reference mode on O1/O3 (images_url[s]), not their start-end mode: start-end needs
// BOTH frames, and this route has one picture.
//
// ⚠ EVERY REFUSAL HERE IS A CallError THAT IS NOT ENGAGED AND NOT RETRYABLE (provider_bad_request,
// terminal, free): the snapshot is frozen, so the next pass would meet the very same input.
func (p *videoProvider) body(job Job) (map[string]any, string, error) {
	refuse := func(msg string) error {
		return &aiprov.CallError{
			Provider: entity.AIProviderRunblob, Code: aiprov.CodeBadRequest,
			Err: fmt.Errorf("runblob: %s. Nothing was submitted and nothing was charged", msg),
		}
	}
	if len(job.References) == 0 {
		return nil, "", refuse("a video run animates one picture, and this run's picture did not resolve (its media row is gone)")
	}
	source := strings.TrimSpace(job.References[0])
	if u, err := url.Parse(source); err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		// Kling fetches image_url itself (IMAGE_INACCESSIBLE otherwise): a data uri or a bare path
		// would be paid for and fail.
		return nil, "", refuse("the source picture has no public http(s) url the provider could fetch")
	}
	prompt := strings.TrimSpace(job.Prompt)
	if prompt == "" {
		return nil, "", refuse("Kling takes a prompt of 1–2500 characters, and this run has no words")
	}
	if n := len([]rune(prompt)); n > VideoMaxPromptRunes {
		return nil, "", refuse(fmt.Sprintf("the prompt is %d characters; Kling takes at most %d", n, VideoMaxPromptRunes))
	}
	model := p.modelFor(job)
	fam, ok := videoFamilyOf(model)
	if !ok {
		return nil, "", refuse(fmt.Sprintf("%q is not a runblob video model (known: %s)", truncateRunes(model, 60), videoModelsKnown))
	}
	seconds := job.VideoDuration
	if seconds <= 0 {
		seconds = VideoDurationSeconds
	}
	ar := strings.TrimSpace(job.VideoAspectRatio)
	body := map[string]any{"prompt": prompt}
	switch fam {
	case videoFamKlingO1:
		body["images_urls"] = []string{source}
		body["duration"] = strconv.Itoa(seconds)
	case videoFamKlingO3:
		body["model"] = model
		body["images_url"] = []string{source}
		body["duration"] = strconv.Itoa(seconds)
	case videoFamSeedance:
		if n := len([]rune(prompt)); n < seedanceMinPromptRunes && model != VideoModelSeedance25Face {
			return nil, "", refuse(fmt.Sprintf("%s takes a prompt of %d–20000 characters, and this one has %d",
				model, seedanceMinPromptRunes, n))
		}
		body["model"] = model
		body["resolution"] = seedanceResolution
		body["duration"] = seconds
		if model == VideoModelSeedanceMini {
			body["first_frame_url"] = source
		} else {
			body["image_with_roles"] = []map[string]string{{"url": source, "role": "first_frame"}}
		}
		if model == VideoModelSeedance25Face {
			ar = "adaptive"
		}
	default:
		body["model"] = model
		body["image_url"] = source
		// «Kling 3 models: integer 3-15. Other models: string "5" or "10"» — the spec's own
		// two spellings of one number. UNVERIFIED (G-06) whether kling_3 also takes the string.
		body["duration"] = videoDurationWire(model, seconds)
	}
	if ar != "" {
		body["aspect_ratio"] = ar
	}
	return body, model, nil
}

const (
	// seedanceMinPromptRunes — Mini and 2.0: «3–20,000 characters» (2.5 has no minimum).
	seedanceMinPromptRunes = 3
	// seedanceResolution — the documented default, which every Seedance model takes.
	seedanceResolution = "720p"
)

// truncateRunes cuts s to at most n runes for a sentence.
func truncateRunes(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

// videoDurationWire — the duration in the spelling the model family documents.
func videoDurationWire(model string, seconds int) any {
	if strings.HasPrefix(model, "kling_3") {
		return seconds
	}
	return fmt.Sprintf("%d", seconds)
}

// videoSubmitError — the error the WORKER classifies for a failed submit: the adapter's CallError as
// it is when nothing was bought (not engaged: a refusal at the gate, a dial that never connected), and
// errVideoSubmitUnconfirmed AROUND it when the request left and may have been queued — engaged by the
// adapter's own word, a 408, or a 5xx other than a bare 503 (a gateway can lose a queue's answer after
// the enqueue; fal learnt it, G-03). Both errors stay reachable through errors.As / errors.Is.
func videoSubmitError(err error) error {
	ce, ok := aiprov.AsCallError(err)
	if !ok {
		return err
	}
	if ce.Engaged {
		return fmt.Errorf("%w: %w", errVideoSubmitUnconfirmed, err)
	}
	if ce.HTTPStatus == http.StatusRequestTimeout ||
		(ce.HTTPStatus >= 500 && ce.HTTPStatus != http.StatusServiceUnavailable) {
		// The adapter reads a 5xx/408 on the exchange as «not engaged» (it is shared with the image
		// routes); for a SUBMIT it is Meshy's rule that holds: the gateway may have lost the answer
		// after the enqueue. Re-issued engaged and final, so classify (the transport decides the money
		// answers) closes the attempt `unknown` and never buys a twin.
		engaged := *ce
		engaged.Engaged, engaged.Retryable = true, false
		return fmt.Errorf("%w: %w", errVideoSubmitUnconfirmed, &engaged)
	}
	return err
}

// runblobSubmitEnd — a failed runblob submit as the ledger reads it (B-14, the transports' table):
// NOT engaged → `free` (a 408 → `unknown`, timeoutIsNotFree); engaged, or a 5xx that may have queued
// the job (videoSubmitError's rule) → `unknown`, no number; no CallError → `unknown`.
func runblobSubmitEnd(err error) entity.AICallEnd {
	var end entity.AICallEnd
	ce, spoke := aiprov.AsCallError(err)
	switch {
	case spoke && !ce.Engaged && !(ce.HTTPStatus >= 500 && ce.HTTPStatus != http.StatusServiceUnavailable):
		end.Status, end.Engaged = entity.AICallFree, notEngaged()
		return timeoutIsNotFree(withFailure(end, err), err)
	case spoke:
		end.Status, end.Engaged, end.CostSource = entity.AICallUnknown, engaged(), entity.AICostNone
		return withFailure(end, err)
	}
	end.Status, end.CostSource = entity.AICallUnknown, entity.AICostNone
	return withFailure(end, err)
}

// Collect is the FREE half: one status read and, once the clip is there, the bytes.
//
// THE BYTES ARE TAKEN AT ONCE AND THE LINK IS NEVER STORED: runblob's result urls (cdn.runblob.io)
// have an undocumented lifetime, and a stored link is a clip that quietly stops existing (the fal rule).
func (p *videoProvider) Collect(ctx context.Context, job Job, requestID string) (*Outcome, error) {
	if !p.Enabled() {
		return nil, fmt.Errorf("%w: %s", errProviderDisabled, p.MissingCredential())
	}
	fam, id, price, ok := splitVideoLocator(requestID)
	if !ok || id == "" {
		err := fmt.Errorf("%w: the stored locator %q names no video family and generation id", errVideoNoResult, requestID)
		job.priceAcceptedIfSet(ctx, videoCollectEnd(nil, err, decimal.NullDecimal{}))
		return nil, err
	}
	g, err := p.c.Status(ctx, fam.path, id)
	if err != nil {
		// A transient read (retryable by the adapter's word) writes nothing and the row stays
		// `accepted`; a terminal one (the id unknown to runblob, a key rejected on the read) is money
		// nobody can account for — `unknown`, with the submit's price beside it when it was known.
		job.priceAcceptedIfSet(ctx, videoCollectEnd(nil, err, price))
		return nil, err
	}
	switch strings.ToLower(g.Status) {
	case "pending", "processing":
		return nil, fmt.Errorf("%w: generation %s is %s", errVideoNotReady, id, g.Status)
	case "failed":
		err := fmt.Errorf("%w: generation %s: %s", errVideoFailed, id, videoReason(g.Failure()))
		job.priceAcceptedIfSet(ctx, videoCollectEnd(nil, err, price))
		return nil, err
	case "completed":
	default:
		// UNVERIFIED (G-06): the four documented words are pending | processing | completed | failed.
		// A fifth is read as «still running» — looking again is free, and a word we do not know must
		// not close a bought job as lost.
		return nil, fmt.Errorf("%w: generation %s answered status %q", errVideoNotReady, id, g.Status)
	}
	model := strings.TrimSpace(g.Model)
	if model == "" {
		model = p.modelFor(job)
	}
	// «PAID, AND NOTHING CAME OF IT» CARRIES THE PRICE (the 3D routes' rule): a completed job is not
	// refunded, so every failure from here on is booked with the submit's number.
	charged := func(err error) (*Outcome, error) {
		out := &Outcome{RequestID: requestID, Model: model, Price: price, Provider: entity.AIProviderRunblob}
		job.priceAcceptedIfSet(ctx, videoCollectEnd(out, err, price))
		if !price.Valid {
			// ok = false is NOT a charge of zero: an unpriced failure still returns no Outcome.
			return nil, err
		}
		return out, err
	}
	if g.VideoURL == "" {
		return charged(fmt.Errorf("%w: generation %s completed with no video_url", errVideoNoResult, id))
	}
	raw, ferr := p.download(ctx, g.VideoURL)
	if ferr != nil {
		if errors.Is(ferr, errVideoFetchFailed) {
			// The wire broke, the url is durable: nothing is written, the next collect fetches again.
			return nil, ferr
		}
		return charged(ferr)
	}
	out := &Outcome{RequestID: requestID, Model: model, Price: price, Provider: entity.AIProviderRunblob}
	// DELIVERED: the submit's `accepted` row is priced with the number the attempt books (B-07) —
	// runblob's own, cost_source provider.
	job.priceAcceptedIfSet(ctx, videoCollectEnd(out, nil, price))
	out.Artifacts = append(out.Artifacts, Artifact{
		Bytes:       raw,
		ContentType: ContentTypeMP4,
		Kind:        entity.DesignPictureKindVideo,
	})
	return out, nil
}

// videoReason — the provider's failure word for a sentence, or a placeholder.
func videoReason(msg string) string {
	if msg = strings.TrimSpace(msg); msg != "" {
		return msg
	}
	return "no reason given"
}

// videoCollectEnd — what a collect writes onto the submit's `accepted` ledger row (B-07), and whether
// it writes at all. price is the submit's number (from the locator):
//
//   - delivered                  → ok, priced with it (cost_source provider; none when unknown);
//   - failed by runblob          → failed, a KNOWN zero, cost_source provider: the refund is the
//     provider's own statement (spec: «On any failed job the user's balance is auto-refunded»);
//   - still running / transient  → nothing: the row stays `accepted`, the next collect decides;
//   - anything else terminal     → charged_failed with the submit's price when it was known (the money
//     is real, the clip is not), else `unknown`.
func videoCollectEnd(out *Outcome, err error, price decimal.NullDecimal) entity.AICallEnd {
	end := entity.AICallEnd{Engaged: engaged()}
	if out != nil {
		end.ModelActual = out.Model
	}
	switch {
	case err == nil:
		end.Status = entity.AICallOK
	case errors.Is(err, errVideoFailed):
		end.Status = entity.AICallFailed
		end.CostUSD, end.CostSource = decimal.NewNullDecimal(decimal.Zero), entity.AICostProvider
		return withFailure(end, err)
	case classify(err).Retryable:
		return entity.AICallEnd{}
	case price.Valid:
		end.Status = entity.AICallChargedFailed
	default:
		end.Status = entity.AICallUnknown
	}
	if price.Valid {
		end.CostUSD, end.CostSource = price, entity.AICostProvider
	} else {
		end.CostSource = entity.AICostNone
	}
	return withFailure(end, err)
}

// priceAcceptedIfSet prices the submit's row (call 1) unless the end is EMPTY — videoCollectEnd's
// «write nothing» for a transient outcome; recordCollect's `write` flag, as a method.
func (j *Job) priceAcceptedIfSet(ctx context.Context, end entity.AICallEnd) {
	if end.Status == "" {
		return
	}
	j.priceAccepted(ctx, 1, end)
}

// download fetches the finished clip: https only, the bucket's ceiling, the mp4 container checked by
// its own magic (`ftyp` at byte 4, the bucket's sniff) so a page of HTML from an expired link is not
// handed to the sink as a video after the money.
func (p *videoProvider) download(ctx context.Context, rawURL string) ([]byte, error) {
	fetch := p.fetch
	if fetch == nil {
		fetch = httpFetchVideo
	}
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return nil, fmt.Errorf("%w: video_url %q is not an https url", errVideoNoResult, rawURL)
	}
	raw, err := fetch(ctx, u.String())
	if err != nil {
		return nil, err
	}
	if len(raw) < 12 || string(raw[4:8]) != "ftyp" {
		return nil, fmt.Errorf("%w: the downloaded file is not an mp4 container (%d bytes)", errVideoNoResult, len(raw))
	}
	return raw, nil
}

// httpFetchVideo — the real fetch: one GET, bounded in time and in bytes, refusing over the ceiling
// by name (a prefix of a clip is not a clip).
func httpFetchVideo(ctx context.Context, rawURL string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, videoDownloadTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", errVideoNoResult, err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", errVideoFetchFailed, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 500 || resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == http.StatusRequestTimeout {
		return nil, fmt.Errorf("%w: the CDN answered HTTP %d", errVideoFetchFailed, resp.StatusCode)
	}
	if resp.StatusCode != http.StatusOK {
		// A 403/404 on a result url is the link gone (an expired signature, a purged object): the
		// clip cannot be had — terminal, and priced, since the generation completed.
		return nil, fmt.Errorf("%w: the CDN answered HTTP %d for the clip", errVideoNoResult, resp.StatusCode)
	}
	var buf bytes.Buffer
	n, err := io.Copy(&buf, io.LimitReader(resp.Body, videoMaxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("%w: %v", errVideoFetchFailed, err)
	}
	if n > videoMaxBytes {
		return nil, fmt.Errorf("%w: the clip is larger than %d bytes, the store's ceiling", errVideoNoResult, videoMaxBytes)
	}
	return buf.Bytes(), nil
}

// ─── the locator: `<family>#<uuid>#<price>` ───

// videoLocator — the accepted request as the attempt row stores it: the family's tag (kling |
// kling-o1 | kling-o3 | seedance), the generation id and the price it was bought at (absent when
// runblob said «calculating» or nothing — Seedance never says one).
func videoLocator(fam videoFamily, id string, price decimal.NullDecimal) string {
	loc := fam.tag + videoLocatorSep + strings.TrimSpace(id)
	if price.Valid {
		loc += videoLocatorSep + price.Decimal.String()
	}
	return loc
}

// splitVideoLocator — (family, id, price) of a stored locator. A locator written before H5 is
// `kling#<id>#<price>` — the same shape with the main Kling family, read as it was written; a bare
// uuid (no family, no price) is read as a main-Kling id, so a row written by hand still collects. A
// family tag nobody knows is ok=false: its path cannot be guessed, and guessing would read another
// family's generation.
func splitVideoLocator(s string) (videoFamily, string, decimal.NullDecimal, bool) {
	parts := strings.Split(strings.TrimSpace(s), videoLocatorSep)
	var price decimal.NullDecimal
	if len(parts) == 1 {
		return videoFamKling, strings.TrimSpace(parts[0]), price, true
	}
	var fam videoFamily
	for _, f := range videoFamilies {
		if strings.TrimSpace(parts[0]) == f.tag {
			fam = f
		}
	}
	if fam.tag == "" {
		return videoFamily{}, "", price, false
	}
	if len(parts) > 2 {
		if d, err := decimal.NewFromString(parts[2]); err == nil && d.IsPositive() {
			price = decimal.NullDecimal{Decimal: d, Valid: true}
		}
	}
	return fam, strings.TrimSpace(parts[1]), price, true
}

// logVideoCeilingBreach — the submit's price against what the door reserved (RUNBLOB_VIDEO_CEILING_USD
// per clip, on the run as RouteReservedUSD). Said out loud, never refused: the job is bought.
func logVideoCeilingBreach(ctx context.Context, job Job, locator string, price decimal.NullDecimal) {
	if !price.Valid || !job.RouteReservedUSD.Valid || !price.Decimal.GreaterThan(job.RouteReservedUSD.Decimal) {
		return
	}
	slog.Default().ErrorContext(ctx, "video: runblob priced the clip above the run's reservation — raise "+
		"RUNBLOB_VIDEO_CEILING_USD",
		slog.Int("run_id", job.RunID), slog.String("request_id", locator),
		slog.String("price_usd", price.Decimal.String()),
		slog.String("reserved_usd", job.RouteReservedUSD.Decimal.String()))
}

// nearestVideoAspect — the Kling ratio (16:9 | 9:16 | 1:1) nearest to a w×h picture, by the ratio of
// ratios (symmetric: 2:1 is as far from 1:1 as 1:2 is); "" when the size is not known.
func nearestVideoAspect(w, h int) string {
	if w <= 0 || h <= 0 {
		return ""
	}
	r := float64(w) / float64(h)
	best, bestD := "", math.Inf(1)
	for _, c := range []struct {
		name string
		r    float64
	}{{"16:9", 16.0 / 9}, {"9:16", 9.0 / 16}, {"1:1", 1}} {
		if d := math.Abs(math.Log(r / c.r)); d < bestD {
			best, bestD = c.name, d
		}
	}
	return best
}

// VideoRouteFunc — the live `video.generate` route as ONE function for the door and the worker
// (app.go hands it to both, B-32): the head row's model (the owner's runblob video slug — IsVideoModel —
// or "" = Kling's own default) and this deployment's reserve per clip (RUNBLOB_VIDEO_CEILING_USD). Read at every call,
// so a slug moved in the panel reaches the next submit and the next door read together.
func VideoRouteFunc(reg *registry.Registry, ceiling decimal.Decimal) func() VideoRoute {
	return func() VideoRoute {
		r := VideoRoute{CeilingUSD: ceiling}
		if reg == nil {
			return r
		}
		if head, _, ok := reg.RouteHeadAt(entity.AIPurposeVideoGenerate); ok {
			r.Model = strings.TrimSpace(head.Model)
		}
		return r
	}
}
