// Package fal is the client for fal.ai's QUEUE API — the transport behind the DESIGN band's 3D
// route (K-10, owner's own words: «для 3d как референсы должны использоваться
// hitem3d/hi3d/v3.0/multi-view-to-3d и нам нужна интеграция с fal.ai и что бы мы могли туда
// подавать наши фронт бэк и так далее»).
//
// WHY fal AND NOT MESHY DIRECTLY. (Since 2026-09-29 this is the ONLY 3D transport: the direct Meshy
// provider left and fal hosts Meshy's models.) The two model families do not take the same request.
// Meshy's multi-image-to-3d takes an ORDERED LIST and reads image_urls[0] as the front;
// hitem3d takes NAMED SLOTS — front_image_url, back_image_url, left_image_url, right_image_url —
// which is exactly the shape the bench already has, and is what the owner asked for by name. An
// ordered list flattens that naming and loses the one thing this provider is better at.
//
// ⚠ TWO MODEL FAMILIES NOW SHARE THIS TRANSPORT, AND THE REQUEST THIS PACKAGE TAKES STAYS NAMED
// FOR BOTH. fal serves meshy's own multi-image-to-3d as well (`meshy/v7/multi-image-to-3d`, the
// default since the owner asked for it), and that endpoint takes the ORDERED, UNNAMED `image_urls`
// list. The naming is therefore flattened HERE, at the last possible moment and in one place:
// Request3D keeps its four named slots, and the meshy body is written front, back, left, right
// with the front at index 0. Callers do not learn which family they are talking to, so the day the
// slug moves back nothing above this package has to change — and, more to the point, no caller can
// quietly start passing an ordered list of its own and lose the one fact the bench knows for sure.
//
// THE PACKAGE HAS A Submit / Collect / Await SPLIT on purpose: a paid submit, then minutes of
// building, then artifacts behind expiring links — the shape designgen's 3D pass reads.
//
// The client is optional: with no FAL_KEY, Enabled() is false and every verb returns
// ErrNotConfigured, whose sentence names the variable so the refusal a person reads on the screen
// tells them what to set.
package fal

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/shopspring/decimal"

	"github.com/jekabolt/grbpwr-manager/internal/aiprov"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
)

const (
	// defaultBaseURL is fal's queue root. Overridable with FAL_BASE_URL, which exists for tests
	// and for a proxy — not as a knob anybody is expected to set.
	defaultBaseURL = "https://queue.fal.run"

	// DefaultModel3D is the multi-view-to-3d slug the band builds turntables with. It is
	// LOAD-BEARING in the way orimages.DefaultModel is: a slug the provider retires turns every 3D
	// press into a 404 in a fifth of a second, and this repository has already paid for that once —
	// a dead model slug killed both AI features at once and read, on the screen, as a temporary
	// provider fault.
	//
	// THAT IS WHY A 404 ON THE SUBMIT PATH IS ITS OWN SENTINEL HERE (ErrModelUnavailable) and not
	// weather: «there is no such model» and «the service is busy» send a person to two different
	// places, and only one of them is a place where the problem actually is.
	//
	// ⚠ IT MOVED FROM hitem3d TO MESHY v7 ON THE OWNER'S OWN ASK, AND THE TRADE IS SAID OUT LOUD.
	// hitem3d takes the plates BY NAME; meshy/v7 takes an unnamed list and infers the sides itself,
	// which is a WEAKER contract on exactly the axis the complaint was about. It is chosen anyway
	// because the owner asked for the better reconstruction, and the loss is mitigated the only way
	// an ordered list can be: the front is always index 0 (see meshyImageURLs).
	//
	// THE DEFAULT LIVES IN CODE RATHER THAN IN FAL_MODEL_3D, and that is not a preference either:
	// updating the DigitalOcean spec IS a deployment of whatever master happens to be, so a model
	// change made through the environment carries an unrelated release with it.
	//
	// ⚠ MOVING IT ORPHANS EVERY BUILD IN FLIGHT UNLESS SOMETHING LOOKS FOR THEM — see
	// retired3D and locateRequest, which is where that is handled.
	DefaultModel3D = "meshy/v7/multi-image-to-3d"

	// formatGLB is the only export format asked for or accepted. The band shows GLB and only GLB.
	formatGLB = "glb"

	// defaultHTTPTimeout bounds ONE control-plane request (submit, status or result envelope). All
	// three answer with a small JSON object.
	defaultHTTPTimeout = 30 * time.Second

	// defaultPollInterval is how long Await sleeps between status lookups. Lookups are free but not
	// free of rate limits, and a multi-view build takes minutes.
	defaultPollInterval = 5 * time.Second

	// defaultPollTimeout is the ceiling on WAITING for a request (never on fetching its result).
	// Hitting it yields ErrTimedOut, and the request id is in the error: the build may well still
	// finish, and a later Collect fetches it for free.
	defaultPollTimeout = 12 * time.Minute

	// notFoundGrace is how long Await keeps reading a 404 on the status path as «the queue has not
	// caught up yet» rather than as the terminal «this id buys nothing».
	//
	// ⚠ IT EXISTS BECAUSE THE FIRST LOOKUP HAS NO PAUSE IN FRONT OF IT, and the submit IS THE
	// PAYMENT. A read-after-write lag of one second would otherwise throw away a build that was
	// bought a second earlier, and the only road back from a discarded id is a second charge.
	notFoundGrace = 30 * time.Second

	// defaultDownloadTimeout bounds fetching one artifact. Generous on purpose: this is the step
	// that must not be cut short, because a slow CDN is a bad reason to lose a paid model.
	defaultDownloadTimeout = 5 * time.Minute

	// defaultRequestUSD is the fallback price of ONE 3D build in USD, used when FAL_UNIT_USD is
	// unset.
	//
	// IT IS AN ESTIMATE AND IT IS HERE SO THAT AN UNCONFIGURED DEPLOYMENT RECORDS A PLAUSIBLE COST
	// RATHER THAN ZERO, for the ledger's sake:
	// «this run was free» is a worse lie than «this run cost about a dollar».
	//
	// ⚠ ЭТО ЦЕНА ЗАПРОСА, А НЕ ЕДИНИЦЫ, И ИМЕННО ЗДЕСЬ БЫЛ ДЕФЕКТ. Раньше константа звалась
	// `defaultUnitUSD` и стоила тот же доллар, а рядом стоял довод: «маркетплейсные модели на fal
	// берут одну единицу за запрос, значит единица ≈ одна сборка». Довод — ДОПУЩЕНИЕ О ПРОВАЙДЕРЕ,
	// и оно не проверялось ничем. Живой прогон беты (run 17, 2026-09-01) вернул СТО единиц, их
	// умножили на доллар, и в бухгалтерию уехали **100.0000 USD** при оценке 0.60 — ровное число,
	// какого не выставляет ни один API картинок, и оно одно съело дневной потолок.
	//
	// Поэтому умолчание больше НИЧЕГО НЕ УМНОЖАЕТ: не зная тарифа, честно назвать можно только
	// порядок цены сборки, а не цену единицы, смысл которой у каждой модели свой. Как только
	// FAL_UNIT_USD задан — считается настоящая арифметика `единица × единицы`, потому что тогда
	// развёртывание знает, ЧТО у этой модели является единицей.
	//
	// ⚠ 1.20 IS MESHY v7's TEXTURED PRICE FROM fal'S OWN MODEL PAGE, read on 2026-09-02, and it
	// moved with DefaultModel3D. A default left at the retired provider's 0.60 would under-report
	// every build by half — quieter than the hundred-dollar line above, and therefore worse.
	//
	// ⚠ IT IS READ THROUGH EstimatedRequestUSD BY THE DOOR AS WELL AS BY CostUSD, and it is
	// unexported so that it can only be read that way. See EstimatedRequestUSD for what happened
	// when the door kept its own copy.
	defaultRequestUSD = 1.20

	// defaultDetailedRequestUSD is the same fallback for a DETAILED build (Request3D.Quality =
	// QualityDetailed, which sends `geometry_resolution: "2k"`).
	//
	// ⚠ 1.40 IS fal's OWN «ULTRA MODE» PRICE for meshy v7 multi-image-to-3d, read on 2026-09-27 at
	// https://fal.ai/models/meshy/v7/multi-image-to-3d/llms.txt («A textured model generated from
	// multiple input images costs $1.20, or $1.40 with ultra mode enabled»). The page does not say
	// in so many words that ultra mode IS `geometry_resolution: "2k"`; it is the only resolution dial
	// the endpoint's schema has, and Meshy's own pricing names the 2k geometry surcharge «ultra».
	// The inference is priced at the HIGH end on purpose: booking a detailed build at 1.20 would
	// understate real spend, which is the failure this ledger exists to prevent.
	//
	// THERE IS NO UNTEXTURED FIGURE, AND NONE IS INVENTED. fal publishes only the textured price;
	// an untextured build is therefore booked at the textured one — the safe end of the mistake.
	defaultDetailedRequestUSD = 1.40

	// maxAPIResponseBytes caps a control-plane JSON body. Queue envelopes are a few kilobytes.
	maxAPIResponseBytes = 1 << 20

	// maxModelBytes and maxThumbnailBytes cap the artifacts. Both REFUSE at the limit instead of
	// truncating: a GLB cut at the boundary is a file that opens in nothing and looks like a
	// provider defect for as long as it takes somebody to compare byte counts.
	maxModelBytes     = 64 << 20
	maxThumbnailBytes = 8 << 20

	// maxErrorBodyBytes is how much of a failed response is quoted back in the error message.
	maxErrorBodyBytes = 4 << 10

	// billableUnitsHeader is fal's own report of what a request cost, returned on the RESULT fetch.
	// It is the only number in this whole exchange that comes from the provider rather than from
	// our configuration, which is why it is read and why its absence is recorded rather than
	// papered over — see Result.UnitsAssumed.
	billableUnitsHeader = "x-fal-billable-units"
)

// Status is the queue lifecycle, spelled exactly as fal spells it.
type Status string

const (
	StatusInQueue    Status = "IN_QUEUE"
	StatusInProgress Status = "IN_PROGRESS"
	StatusCompleted  Status = "COMPLETED"
)

// ErrNotConfigured is returned when the client is used with no FAL_KEY.
//
// ⚠ THE SENTENCE NAMES THE VARIABLE, AND THAT IS THE WHOLE POINT OF THE WORDING. This error is
// what a person sees on the screen when they press GENERATE, and «not configured» without a name
// sends them looking through a dashboard for something they cannot identify. The owner types the
// key and must be able to tell, from the button alone, whether that was the missing piece.
var ErrNotConfigured = errors.New("fal: FAL_KEY is not set")

// ErrModelUnavailable is returned when the SUBMIT path answers 404: the configured slug is not one
// fal serves — retired, renamed, or mistyped.
//
// IT IS ITS OWN SENTINEL BECAUSE A RETIRED SLUG IS NOT WEATHER. An unrecognised provider fault is
// classified retryable, so without this the whole attempt cap would be spent knocking on an address
// that does not exist, and the history row would read `provider_unavailable` — sending a person to
// a status page for a model that is simply gone.
var ErrModelUnavailable = errors.New("fal: the configured model is not served by the provider")

// ErrRequestNotFound is returned when the STATUS or RESULT path answers 404 for a request id we
// hold: the id in our attempt row buys nothing, so the only way forward is a new (paid) submit —
// a decision for the worker, not for this client.
var ErrRequestNotFound = errors.New("fal: the provider does not know this request")

// ErrNotReady is returned while the request is still IN_QUEUE or IN_PROGRESS. It is the signal a
// polling caller loops on, and — for the worker — the signal to come back later rather than to
// submit (and pay for) anything again.
var ErrNotReady = errors.New("fal: the request has not finished yet")

// ErrTaskFailed is returned when fal ends the request itself. Terminal: nothing about it improves
// on a retry.
var ErrTaskFailed = errors.New("fal: the provider failed the request")

// ErrTimedOut is returned by Await when the poll ceiling passes with the request still running. It
// is NOT «the request failed» — the id is in the error and a later Collect can still fetch it.
var ErrTimedOut = errors.New("fal: the request did not finish within the poll ceiling")

// ErrNoModel is returned when a COMPLETED request carries no model url. We ask for exactly one
// format, so its absence is a broken answer rather than a format to fall back on.
var ErrNoModel = errors.New("fal: the finished request carries no model file")

// ErrUnauthorized is returned on 401/403: the key is missing, wrong, or not permitted. Like a
// retired slug it is a CONFIGURATION fault wearing the clothes of a transient one.
var ErrUnauthorized = errors.New("fal: the API key was rejected")

// ErrOutOfCredit is returned on 402: the key is good, the request is good, and the account has no
// balance. Its own sentinel because it is its own instruction — nobody in this process can fix it,
// and waiting does not help.
var ErrOutOfCredit = errors.New("fal: the fal.ai account has no balance left")

// ErrRateLimited is returned on 429, so a caller can back off. Submits are never retried here.
var ErrRateLimited = errors.New("fal: rate limited by the provider")

// ErrBadRequest is the provider's own 4xx (and 422) for a request it will not accept.
//
// ⚠ WITHOUT IT A 4xx IS WEATHER. The classifier's default leans retryable because a reset
// connection really is weather; that default would otherwise swallow every «you sent something
// wrong» answer and burn the whole attempt cap re-sending it.
var ErrBadRequest = errors.New("fal: the provider refused the request")

// ErrNoFrontView is returned when a request carries no front image. Checked LOCALLY, before the
// request leaves, because it is a fact we can be certain about here and because the provider's own
// answer to it is a 422 that a retry reproduces exactly.
//
// A BUILD WITHOUT A FRONT IS NOT A CHEAPER BUILD, IT IS A WRONG ONE. hitem3d reads front_image_url
// as the face of the object; handing it a back plate produces a garment turned inside out, and
// the run closes `done` with money spent and nothing in the history to tell it from an honest one.
var ErrNoFrontView = errors.New("fal: a multi-view build needs at least the front view")

// The per-run 3D options (DesignThreedParams.texture / pbr / quality, PLAYGROUND phase 2), spelled
// exactly as the frozen params spell them. The EMPTY STRING is a legal value of every one of them
// and means «not stated», i.e. today's constant: textured, no PBR, standard geometry. That is what
// keeps every run frozen before the fields — and every bench-plate run — byte-identical on the wire.
const (
	OptionOn        = "on"
	OptionOff       = "off"
	QualityStandard = "standard"
	QualityDetailed = "detailed"
)

// ErrBadOption is returned, LOCALLY and before any money, for a 3D option this transport cannot
// send: an unknown word, or PBR on an untextured build (the provider documents enable_pbr as
// «Requires should_texture to be true»). The door refuses both first; this is the second lock for a
// frozen snapshot that reached the worker some other way. It wraps ErrBadRequest so the worker's
// classifier reads it as the non-retryable «this request is wrong» it is.
var ErrBadOption = fmt.Errorf("fal: a 3D option this route cannot send: %w", ErrBadRequest)

// ErrBadImageURL is returned for a reference the provider could not fetch itself.
var ErrBadImageURL = errors.New("fal: image references must be public http(s) urls or data: uris")

// ErrUnexpectedResponse is returned when fal answers 2xx with something this client cannot read.
var ErrUnexpectedResponse = errors.New("fal: unreadable response from the provider")

// ErrTooLarge is returned when an artifact or an envelope exceeds its cap. Refusal, not truncation.
var ErrTooLarge = errors.New("fal: artifact is larger than the allowed maximum")

// ErrSubmitUnconfirmed — A SUBMIT WHOSE OUTCOME NOBODY KNOWS: the whole request left this process
// (WroteRequest without an error) and no usable answer came back — a timeout, a reset connection, a
// 5xx other than a bare 503 (a 502 and a 504 included, and any 5xx naming a request id), a 408 (B-13/A3:
// a server that gave up may have taken the whole body first), a 2xx that could not be read or named no
// request id. fal MAY have queued the job and charged for it.
//
// ⚠ IT IS NEVER RESUBMITTED AUTOMATICALLY (G-03, Codex 1). fal's queue documents no idempotency key
// for a submit (https://fal.ai/docs/documentation/model-apis/inference/queue lists Authorization,
// X-Fal-Request-Timeout, X-Fal-Runner-Hint, X-Fal-Queue-Priority, X-Fal-Store-IO, X-Fal-No-Retry,
// X-Fal-Object-Lifecycle-Preference — nothing that deduplicates two submits; the `Idempotency-Key`
// fal documents belongs to the platform's queue-flush endpoint, not to model submits). A retry of an
// ambiguous submit is therefore a possible SECOND purchase of the same job against ONE reservation.
// designgen classifies this terminal (`submit_unconfirmed`, attempt state `unknown`): the run fails
// closed and the history says the charge must be reconciled with fal's dashboard.
//
// A submit whose request was never written in full (DNS, dial, TLS, a body write cut half-way) is
// not this: fal holds no complete request it could enqueue, and it keeps its ordinary retryable
// classification. Nor is a 503 without a request id — the one explicit refusal; see submitServerError.
var ErrSubmitUnconfirmed = errors.New("fal: the submit may have reached the provider and its outcome is unknown")

// submitLost — the 2xx submit that named no request id: accepted (so possibly paid) and unresumable.
// ENGAGED, like every 2xx that did not become a usable answer.
//
// status is the 2xx the answer came with (submitResponse.httpStatus, stamped by callJSON), and it is
// carried because the contract says HTTPStatus is the response's status, 0 ONLY when no response
// arrived (B-13/A4, Codex B-14 review P3 #4): a 0 here made «fal accepted it and named nothing» read
// in the ledger's http_status exactly like «the connection died before any answer».
func submitLost(status int) error {
	return fail(aiprov.CodeProviderError, status, true, false,
		fmt.Errorf("%w: %w: submit returned no request id", ErrSubmitUnconfirmed, ErrUnexpectedResponse))
}

// ChargedError marks a failure the provider HAS ALREADY BILLED, and carries the charge.
//
// THE SHAPE WAS SHARED with the direct Meshy and Recraft transports (removed 2026-09-29), and
// designgen's 3D pass reads that spelling.
//
// THE UNIT IS BILLABLE UNITS, NOT DOLLARS: the rate is
// configuration (FAL_UNIT_USD) and lives on the Client, and a package-level wrap has no client
// to ask.
type ChargedError struct {
	// Err is the failure itself; errors.Is / errors.As reach it through Unwrap, so every sentinel
	// above keeps classifying exactly as it did before the charge was attached.
	Err error
	// Units is what the provider reported billing. Always > 0 — an unbilled failure must never be
	// dressed as a billed one, so chargedWith refuses to wrap without a number.
	Units float64
	// RequestID names the job the money went to, so a ledger line can be traced to the provider.
	RequestID string
	// Model is the slug that was actually being polled when the failure happened — the same
	// provenance Result.Model carries, and needed for the same reason: a failed-but-billed call on
	// a RECOVERED build must be priced as the model it was bought at, not as today's.
	Model string
	// Assumed — the provider sent NO billing header and Units is billableUnits' one assumed unit
	// (G-03, Codex 3). A pricing side that can book a conservative ceiling instead reads this; one
	// that cannot keeps the old reading (one unit), which is what it always did.
	Assumed bool
}

func (e *ChargedError) Error() string {
	return fmt.Sprintf("%v [billed %v units]", e.Err, e.Units)
}

func (e *ChargedError) Unwrap() error { return e.Err }

// chargedWith attaches a charge to an error, and ONLY when the provider named one. Zero units means
// «the provider did not say», which is not the same as «free».
func chargedWith(err error, units float64, requestID, model string) error {
	if err == nil || units <= 0 {
		return err
	}
	return &ChargedError{Err: err, Units: units, RequestID: requestID, Model: model}
}

// chargedAssumed — chargedWith, saying whether the units were read or assumed.
func chargedAssumed(err error, units float64, assumed bool, requestID, model string) error {
	out := chargedWith(err, units, requestID, model)
	if ce, ok := out.(*ChargedError); ok {
		ce.Assumed = assumed
	}
	return out
}

// ChargedModel is the slug a failed-but-billed call was polling, or «» when the error carries no
// charge at all. A caller pricing that charge must ask this rather than the client's CONFIGURED
// model — see Result.Model for the same argument on the success path.
func ChargedModel(err error) string {
	var ce *ChargedError
	if errors.As(err, &ce) {
		return ce.Model
	}
	return ""
}

// Charge reports what a failed call billed, when the provider said. ok = false means NOBODY COULD
// SAY — never «it was free»; a NULL price and a zero price are different claims about one run.
func Charge(err error) (units float64, ok bool) {
	var ce *ChargedError
	if errors.As(err, &ce) {
		return ce.Units, true
	}
	return 0, false
}

// Config is the client configuration. Bound in config/cfg.go — EVERY field has its own explicit
// viper.BindEnv line and a test, because viper.AutomaticEnv is off in this repo and an unbound
// variable reads as empty without a word of complaint.
type Config struct {
	APIKey          string        `mapstructure:"api_key"`          // FAL_KEY; empty = disabled
	BaseURL         string        `mapstructure:"base_url"`         // FAL_BASE_URL; empty = defaultBaseURL
	Model3D         string        `mapstructure:"model_3d"`         // FAL_MODEL_3D; empty = DefaultModel3D
	ModelCutout     string        `mapstructure:"model_cutout"`     // FAL_MODEL_CUTOUT; empty = DefaultModelCutout
	HTTPTimeout     time.Duration `mapstructure:"http_timeout"`     // FAL_HTTP_TIMEOUT
	PollInterval    time.Duration `mapstructure:"poll_interval"`    // FAL_POLL_INTERVAL
	PollTimeout     time.Duration `mapstructure:"poll_timeout"`     // FAL_POLL_TIMEOUT
	DownloadTimeout time.Duration `mapstructure:"download_timeout"` // FAL_DOWNLOAD_TIMEOUT
	UnitUSD         float64       `mapstructure:"unit_usd"`         // FAL_UNIT_USD; <=0 = defaultUnitUSD
	// UnitUSDCutout is the tariff of the BACKGROUND-REMOVAL route, and it is a second variable
	// rather than a reuse of UnitUSD because the two routes' units differ by two orders of
	// magnitude — see CostCutoutUSD. <=0 = defaultCutoutUSD per request.
	UnitUSDCutout float64 `mapstructure:"unit_usd_cutout"` // FAL_UNIT_USD_CUTOUT
	// UnitsCeiling3D is the most billable units ONE 3D build may report (FAL_UNITS_CEILING_3D). It
	// matters only when UnitUSD is set: then a build books `UnitUSD × units`, and the door can
	// reserve no less than that only if somebody states how many units a build may take. With a
	// tariff and no ceiling the reservation has nothing to stand on, so the 3D door refuses in
	// words (G-02, Codex 4) rather than reserving a number below the booking. <=0 = not stated.
	UnitsCeiling3D float64 `mapstructure:"units_ceiling_3d"` // FAL_UNITS_CEILING_3D
	// PLAYGROUND phase 3 — the generic JSON routes (generic.go). Each has its own slug and its own
	// tariff for the reason the cut-out has (units differ per model), and its own units ceiling for
	// the reason 3D has (a tariff without a ceiling leaves the reserve nothing to stand on — the door
	// then refuses the kind: route_reserve_unbounded). Empty slug = the code default; <=0 = unset.
	ModelOutpaint        string  `mapstructure:"model_outpaint"`         // FAL_MODEL_OUTPAINT
	ModelFill            string  `mapstructure:"model_fill"`             // FAL_MODEL_FILL
	UnitUSDOutpaint      float64 `mapstructure:"unit_usd_outpaint"`      // FAL_UNIT_USD_OUTPAINT
	UnitsCeilingOutpaint float64 `mapstructure:"units_ceiling_outpaint"` // FAL_UNITS_CEILING_OUTPAINT
	UnitUSDFill          float64 `mapstructure:"unit_usd_fill"`          // FAL_UNIT_USD_FILL
	UnitsCeilingFill     float64 `mapstructure:"units_ceiling_fill"`     // FAL_UNITS_CEILING_FILL
	// ModelImage is the image.generate transport's own default slug (images.go, H3): what a route
	// row naming fal with no model draws. Empty = DefaultModelImage. No tariff of its own: the ledger
	// prices it by the catalogue (pricing, fal rows) × x-fal-billable-units, or books it unpriced.
	ModelImage string `mapstructure:"model_image"` // FAL_MODEL_IMAGE
	// KeyFunc, when set, is asked for the key on EVERY request and by Enabled(): it is the AI
	// providers registry's hook (internal/aiprov/registry), so a key saved in the admin panel — or
	// a provider switched off there — takes effect on the next request without a redeploy. "" means
	// disabled. nil = APIKey above, exactly as before. Never serialised, never printed.
	KeyFunc func() string `mapstructure:"-"`
}

// String renders the config with the API key redacted, so an accidental %v / %+v / %s of it — in a
// log line, an error, or a test print — cannot leak the key. fmt routes all three through Stringer.
func (c Config) String() string {
	key := ""
	if strings.TrimSpace(c.APIKey) != "" {
		// Empty stays empty: whether the provider is configured at all is diagnostic, and hiding
		// that would turn a redaction into a second mystery.
		key = "***REDACTED***"
	}
	return fmt.Sprintf("fal.Config{APIKey:%s BaseURL:%s Model3D:%s ModelCutout:%s HTTPTimeout:%s "+
		"PollInterval:%s PollTimeout:%s DownloadTimeout:%s UnitUSD:%v UnitUSDCutout:%v UnitsCeiling3D:%v "+
		"ModelOutpaint:%s ModelFill:%s UnitUSDOutpaint:%v UnitsCeilingOutpaint:%v UnitUSDFill:%v UnitsCeilingFill:%v "+
		"ModelImage:%s}",
		key, c.BaseURL, c.Model3D, c.ModelCutout, c.HTTPTimeout, c.PollInterval, c.PollTimeout,
		c.DownloadTimeout, c.UnitUSD, c.UnitUSDCutout, c.UnitsCeiling3D,
		c.ModelOutpaint, c.ModelFill, c.UnitUSDOutpaint, c.UnitsCeilingOutpaint, c.UnitUSDFill, c.UnitsCeilingFill,
		c.ModelImage)
}

// Client is a configured fal queue client. A nil *Client is valid and permanently disabled, so
// callers need not nil-check before asking Enabled().
type Client struct {
	cfg  Config
	http *http.Client
	log  *slog.Logger
}

// New builds a client and applies defaults. It does not validate the key: an unset key simply
// leaves the client disabled.
func New(cfg Config) *Client {
	cfg.APIKey = strings.TrimSpace(cfg.APIKey)
	cfg.BaseURL = strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	if cfg.BaseURL == "" {
		cfg.BaseURL = defaultBaseURL
	}
	cfg.Model3D = strings.Trim(strings.TrimSpace(cfg.Model3D), "/")
	if cfg.Model3D == "" {
		cfg.Model3D = DefaultModel3D
	}
	if cfg.HTTPTimeout <= 0 {
		cfg.HTTPTimeout = defaultHTTPTimeout
	}
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = defaultPollInterval
	}
	if cfg.PollTimeout <= 0 {
		cfg.PollTimeout = defaultPollTimeout
	}
	if cfg.DownloadTimeout <= 0 {
		cfg.DownloadTimeout = defaultDownloadTimeout
	}
	// НЕ ПОДСТАВЛЯЕТСЯ. Ноль здесь — не «забыли», а «тариф неизвестен», и `CostUSD` отвечает на это
	// оценкой ЗА ЗАПРОС. Подстановка доллара за единицу и была тем, что дало сто долларов.
	if cfg.UnitUSD < 0 {
		cfg.UnitUSD = 0
	}
	if cfg.UnitsCeiling3D < 0 {
		cfg.UnitsCeiling3D = 0
	}
	// Same rule for the generic routes: a negative number is «unset», never a negative price.
	for _, f := range []*float64{&cfg.UnitUSDOutpaint, &cfg.UnitsCeilingOutpaint, &cfg.UnitUSDFill, &cfg.UnitsCeilingFill} {
		if *f < 0 {
			*f = 0
		}
	}
	return &Client{
		// The shared http.Client carries NO Timeout of its own: every request below gets its
		// deadline from its own context, and the two budgets differ by an order of magnitude — a
		// status lookup may not take half a minute, a download may take five.
		cfg:  cfg,
		http: &http.Client{},
		log:  slog.Default(),
	}
}

// Enabled reports whether an API key is configured. Nil-safe.
func (c *Client) Enabled() bool { return c != nil && c.apiKey() != "" }

// apiKey is the key a request is built with: Config.KeyFunc when wired (read per call, so a
// rotation reaches the next submit), else Config.APIKey (trimmed in New).
func (c *Client) apiKey() string {
	if c.cfg.KeyFunc != nil {
		return strings.TrimSpace(c.cfg.KeyFunc())
	}
	return c.cfg.APIKey
}

// Model returns the effective 3D slug (provenance for the attempt row). Nil-safe.
func (c *Client) Model() string {
	if c == nil {
		return ""
	}
	return c.cfg.Model3D
}

// PollInterval and PollTimeout expose the effective waiting shape, so a worker can size its own
// lease against the same numbers rather than a guess.
func (c *Client) PollInterval() time.Duration {
	if c == nil {
		return defaultPollInterval
	}
	return c.cfg.PollInterval
}

func (c *Client) PollTimeout() time.Duration {
	if c == nil {
		return defaultPollTimeout
	}
	return c.cfg.PollTimeout
}

// EstimatedRequestUSD is what ONE build off this route is expected to cost, before the provider has
// said anything. It is the number CostUSD falls back on when no tariff is configured — and it is
// EXPORTED BECAUSE THE DOOR RESERVES AGAINST IT.
//
// ⚠ TWO LITERALS THAT HAPPEN TO MATCH ARE NOT ONE NUMBER, AND THESE TWO HAD ALREADY COME APART.
// StartDesignRun writes designPriceEstimate[threed] into design_run.price_estimate and adds it to
// design_budget_day.reserved. That table carried its own literal — $0.60, the retired provider's
// price — while this package charged $1.20 for the same build. Five turntables in flight showed
// $3.00 of committed money against $6.00 of real spend. The estimate a route publishes and the
// number the door reserves are now one expression, so moving one moves the other.
//
// ⚠ WHAT IT IS NOT: A GATE. The daily admission ceiling was removed as a concept in migration 0358
// («у нас в принципе не должно быть потолка»), so nothing is refused on the strength of this
// number. Getting it wrong therefore misstates the books and the panel — it does not let spending
// past a limit, because there is no limit. That is a smaller harm and it is the accurate one.
//
// IT IS A FUNCTION RATHER THAN A CONSTANT so callers get a decimal.Decimal rather than a float they
// would each have to convert — three conversions of one number is how a number becomes three.
func EstimatedRequestUSD() decimal.Decimal { return EstimatedRequestUSDFor("") }

// EstimatedRequestUSDFor is the same number FOR A NAMED MODEL, which matters exactly once: when a
// build is recovered from a slug this deployment has since moved off.
//
// ⚠ AN UNKNOWN OR EMPTY SLUG ANSWERS WITH THE CURRENT DEFAULT'S PRICE, WHICH IS THE SAFE END OF
// THE MISTAKE. The two ways to be wrong are not symmetrical: pricing an old build at today's
// (higher) number overstates one row, while pricing today's build at an old (lower) number
// understates real spend in the ledger — the failure this whole accounting exists to prevent. A
// slug nobody wrote down therefore gets the current estimate, not the cheapest one.
func EstimatedRequestUSDFor(model string) decimal.Decimal {
	return EstimatedRequestUSDForQuality(model, "")
}

// EstimatedRequestUSDForQuality is EstimatedRequestUSDFor AT THE BUILD'S OWN TIER: a detailed build
// (quality = QualityDetailed) off the current family is fal's «ultra mode» price, every other value
// — empty, standard, anything unknown — the standard one. A retired slug keeps its own single price:
// it was never offered a tier, so a tier cannot move its money.
//
// ⚠ THE DOOR RESERVES AGAINST THIS AND THE COLLECT BOOKS AGAINST THIS, and that is why it is one
// function: a detailed run reserved at 1.40 and booked at 1.20 (or the reverse) is the two-copies
// defect EstimatedRequestUSD documents, reborn one dial later.
func EstimatedRequestUSDForQuality(model, quality string) decimal.Decimal {
	model = strings.Trim(strings.TrimSpace(model), "/")
	for _, r := range retired3D {
		if strings.EqualFold(model, r.Model) {
			return decimal.NewFromFloat(r.RequestUSD)
		}
	}
	if strings.TrimSpace(quality) == QualityDetailed {
		return decimal.NewFromFloat(defaultDetailedRequestUSD)
	}
	return decimal.NewFromFloat(defaultRequestUSD)
}

// RequestCeilingUSDForQuality — THE MOST ONE 3D BUILD OF THIS TIER MAY BOOK ON THIS CLIENT, i.e. the
// number the door must reserve so that the collect (CostUSDForQuality) never books more (G-02,
// Codex 4). ok = false means there is no such number.
//
//   - no tariff (FAL_UNIT_USD unset): the collect books EstimatedRequestUSDForQuality(model, tier)
//     whatever the units — the same function, so the reserve equals the booking exactly;
//   - a tariff AND a stated units ceiling (FAL_UNITS_CEILING_3D): tariff × ceiling — the booking is
//     tariff × reported units, bounded by the ceiling the operator stated;
//   - a tariff and no ceiling: ok = false. The booking is tariff × whatever the provider reports
//     (run 17 reported a hundred), and no reservation can be said to cover it.
//
// Nil-safe: a nil client answers the unconfigured estimate for the default model.
func (c *Client) RequestCeilingUSDForQuality(quality string) (decimal.Decimal, bool) {
	if c == nil || c.cfg.UnitUSD <= 0 {
		return EstimatedRequestUSDForQuality(c.Model(), quality), true
	}
	if c.cfg.UnitsCeiling3D <= 0 {
		return decimal.Zero, false
	}
	return decimal.NewFromFloat(c.cfg.UnitUSD).Mul(decimal.NewFromFloat(c.cfg.UnitsCeiling3D)), true
}

// UnitsCeiling3D — the stated units ceiling of one 3D build under a tariff (ok = false: no tariff,
// or no ceiling stated). The collect compares a build's reported units with it and says so out loud
// when the provider billed more than the reservation was sized for.
func (c *Client) UnitsCeiling3D() (float64, bool) {
	if c == nil || c.cfg.UnitUSD <= 0 || c.cfg.UnitsCeiling3D <= 0 {
		return 0, false
	}
	return c.cfg.UnitsCeiling3D, true
}

// AcceptsBuildOptions reports whether the CONFIGURED 3D model reads the per-run build options
// (Request3D.Texture / PBR / Quality). Only the meshy family does; the retired hitem3d body sends
// fixed constants and drops them (Submit logs it). The band advertises no options and the door
// refuses a non-default one on a route that answers false (G-02, Codex 3). Nil-safe.
func (c *Client) AcceptsBuildOptions() bool {
	return c != nil && isMeshyFamily(c.Model())
}

// CostUSD converts billable units into money at the configured rate (FAL_UNIT_USD). It is the only
// place that knows the conversion, so the price written into an attempt row and the price shown on
// a button cannot drift apart.
func (c *Client) CostUSD(units float64) decimal.Decimal { return c.CostUSDFor("", units) }

// CostUSDFor prices a charge against THE MODEL THAT ACTUALLY PRODUCED IT (Result.Model /
// ChargedModel), rather than against the one this client is configured with today.
//
// ⚠ THE TARIFF IS NOT PER-MODEL AND IS NOT TREATED AS IF IT WERE. FAL_UNIT_USD is one number an
// operator typed for one deployment; there is no second one to reach for, and inventing a
// per-model rate out of a single configured value would be a guess wearing arithmetic. Only the
// UNCONFIGURED fallback varies by model, because only there does this package supply the number
// itself.
func (c *Client) CostUSDFor(model string, units float64) decimal.Decimal {
	return c.CostUSDForQuality(model, units, "")
}

// CostUSDForQuality is CostUSDFor for a build of a stated tier — see EstimatedRequestUSDForQuality.
//
// ⚠ THE TIER MOVES ONLY THE UNCONFIGURED FALLBACK. With FAL_UNIT_USD set the charge is
// `tariff × units`, and the units are the provider's own report of what the request cost — a
// detailed build that bills more units is already priced higher by that arithmetic, and a second,
// local surcharge on top would count the tier twice.
func (c *Client) CostUSDForQuality(model string, units float64, quality string) decimal.Decimal {
	if c == nil || units <= 0 {
		return decimal.Zero
	}
	// ТАРИФ НЕ ЗАДАН — ЗНАЧИТ УМНОЖАТЬ НЕ НА ЧТО. Число единиц провайдер называет честно, но что
	// именно он ими меряет — секунды, мегапиксели, запросы — знает только его прайс. Умножение на
	// выдуманный тариф даёт не оценку, а уверенное враньё, тем более убедительное, чем больше
	// единиц вернул провайдер. Без тарифа отвечаем ОДНОЙ оценкой за сборку.
	if c.cfg.UnitUSD <= 0 {
		return EstimatedRequestUSDForQuality(model, quality)
	}
	return decimal.NewFromFloat(c.cfg.UnitUSD).Mul(decimal.NewFromFloat(units))
}

// Request3D is one multi-view-to-3d job. The four views are NAMED rather than ordered, which is the
// whole reason this provider was asked for: the bench already knows which plate is the front.
//
// ⚠ THE NAMES SURVIVE EVEN WHERE THE MODEL CANNOT READ THEM. On the meshy family Submit flattens
// these four fields into the ordered `image_urls` list (front first) — the caller still states what
// it knows, and the one place that has to give that knowledge up is the one line that writes the
// body. A Request3D shaped like a list would push the loss all the way back to the render bench.
type Request3D struct {
	// FrontURL is REQUIRED — see ErrNoFrontView.
	FrontURL string
	// BackURL, LeftURL, RightURL are optional supporting views. Empty omits the key entirely.
	BackURL  string
	LeftURL  string
	RightURL string
	// FaceCount optionally overrides the provider's mesh density. Zero means the provider's own
	// default, which is the right answer for a garment shown in a browser.
	FaceCount int
	// Resolution optionally names the provider's quality tier ("2048quality" | "2048master").
	// Empty leaves the provider's default in force; it is a PRICE DIAL, so it is not set silently.
	Resolution string
	// Model overrides the configured slug for this one call. Empty = the configured slug.
	Model string
	// TexturePrompt is a short hint about the SURFACE — colour and cloth — for the texturing stage.
	//
	// ⚠ IT REACHES THE MESHY FAMILY ONLY, AND ON hitem3d IT IS SILENTLY DROPPED — not by oversight
	// but because hitem3d's multi-view-to-3d payload HAS NO TEXT FIELD AT ALL: there is nowhere for
	// the words to go. Refusing the request over it would be worse (a run killed for a hint), and
	// pretending it travelled would be worse still — which is why AcceptsTexturePrompt exists, so
	// the caller that WRITES DOWN what the provider was told can ask instead of assuming.
	TexturePrompt string
	// Texture / PBR / Quality are the run's own 3D options (PLAYGROUND phase 2): '' | on | off,
	// '' | on | off, '' | standard | detailed. EMPTY IS TODAY'S CONSTANT for every one of them —
	// textured, no PBR, the provider's standard geometry — so a request that states none of them is
	// byte-identical to the body this transport sent before the fields existed.
	//
	// ⚠ THEY REACH THE MESHY FAMILY ONLY. hitem3d's body keeps its constants and the request is
	// logged, exactly like TexturePrompt: the slug is retired and selectable only by FAL_MODEL_3D.
	//
	// ⚠ fal's meshy/v7 schema has NO `texture_resolution` and NO `ai_model` (read 2026-09-27 from
	// https://fal.ai/api/openapi/queue/openapi.json?endpoint_id=meshy/v7/multi-image-to-3d), so on
	// this route «detailed» is `geometry_resolution: "2k"` and nothing else.
	Texture string
	PBR     string
	Quality string
}

// Sink is where the bytes of a finished request go. Model is required; Thumbnail is optional and,
// if given, receives the provider's preview image — the tile the band shows for a kind='threed'
// picture, since a GLB is not something a list view can render.
//
// Both are io.Writer rather than []byte returns on purpose: this backend runs on a 0.5 GB instance,
// and a model belongs in the bucket, not in the heap.
//
// ON ERROR A SINK MAY HOLD A PARTIAL WRITE. A transfer that dies halfway has already handed over
// what it received, and this package cannot un-write somebody else's writer.
type Sink struct {
	Model     io.Writer
	Thumbnail io.Writer
}

// Result describes ONE delivered model. It carries no url of any kind, and that omission is
// deliberate: fal's artifact links are temporary, so the only honest thing to hand back is the
// bytes (already written to the Sink) and what they cost.
type Result struct {
	// RequestID is the provider's id for the job — durable, free to look up again, and the one
	// thing worth storing.
	RequestID string
	// Model is the slug this result was ACTUALLY fetched from, which is not always the configured
	// one: a build submitted before a model move is found under the slug it was bought at.
	//
	// ⚠ IT TRAVELS BECAUSE THE PRICE FOLLOWS IT. Without it a caller can only ask the client what
	// model it is configured with NOW, and a recovered hitem3d build — reserved at $0.60 — would be
	// booked at meshy's $1.20, rewriting the money of a run frozen before the deploy. See
	// EstimatedRequestUSDFor.
	Model string
	// Format is always formatGLB, stated rather than assumed so a caller writing a media row does
	// not hardcode the extension in a second place.
	Format string
	// ModelBytes / ThumbnailBytes are what was actually written to the Sink.
	ModelBytes     int64
	ThumbnailBytes int64
	// ModelSHA256 is the hex digest of the model bytes, computed while streaming.
	ModelSHA256 string
	// BillableUnits is what fal's own x-fal-billable-units header reported for this request.
	BillableUnits float64
	// UnitsAssumed says the header was ABSENT and BillableUnits is this package's assumption of
	// one unit per request rather than the provider's number.
	//
	// ⚠ THE FLAG SITS BESIDE THE NUMBER, WHERE THE DECISION IS MADE, AND NOT IN A LIST SOMEWHERE
	// ELSE. A caller that writes the price has to be able to say, at the moment it writes it,
	// whether the provider named it — otherwise an assumption becomes a measurement one refactor
	// later, silently, and in the direction that misreports spend.
	UnitsAssumed bool
}

// Submit creates a queue request and returns its provider id. It does not wait and it does not
// retry: a second submit is a second charge.
func (c *Client) Submit(ctx context.Context, req Request3D) (string, error) {
	if !c.Enabled() {
		return "", ErrNotConfigured
	}
	model := strings.Trim(strings.TrimSpace(req.Model), "/")
	if model == "" {
		model = c.cfg.Model3D
	}
	front := strings.TrimSpace(req.FrontURL)
	if front == "" {
		return "", ErrNoFrontView
	}
	// THE FOUR SLOTS ARE VALIDATED ONCE, IN THE ONE ORDER BOTH FAMILIES CARE ABOUT. The order is
	// meaning for the meshy body (index 0 is the front) and is merely tidy for hitem3d's named
	// fields; deriving it here, before the branch, is what keeps the two bodies from disagreeing
	// about which url is the face of the garment.
	views := []struct {
		name string
		url  string
	}{
		{"front", front},
		{"back", strings.TrimSpace(req.BackURL)},
		{"left", strings.TrimSpace(req.LeftURL)},
		{"right", strings.TrimSpace(req.RightURL)},
	}
	for _, v := range views {
		if v.url == "" {
			continue
		}
		if err := validateImageRef(v.url); err != nil {
			// NAMED, NOT NUMBERED. On the meshy body the ordinal of a view is not even stable —
			// an unoccupied slot closes up — so a refusal reading «view 2» would point at nothing
			// a person can find on the bench.
			return "", fmt.Errorf("%s view: %w", v.name, err)
		}
	}

	opts, err := resolveOptions(req)
	if err != nil {
		return "", err
	}

	var body any
	if isMeshyFamily(model) {
		// ⚠ THE NAMES ARE LOST HERE AND NOWHERE ELSE, AND THE FRONT IS INDEX 0 BY CONSTRUCTION.
		// meshy's multi-image-to-3d takes «images of the same object from different angles» with no
		// way to say which angle each one is; its own documentation promises only that the FIRST is
		// read as the primary frontal reference. So the one guarantee this route can still offer is
		// that the front leads the list — never that a hole in the middle keeps the rest in place,
		// because there are no places to keep.
		urls := make([]string, 0, len(views))
		for _, v := range views {
			if v.url != "" {
				urls = append(urls, v.url)
			}
		}
		mb := meshySubmitBody{
			ImageURLs: urls,
			// A flat drawing becomes a garment only with its colour and print on it; an untextured
			// mesh answers a different question than the one the designer asked — which is why
			// «textured» is what an unstated option means. Texture = off is the person asking
			// that different question on purpose.
			ShouldTexture: opts.texture,
			// PBR maps quadruple the download for lighting nuance a product tile does not show, so
			// they are off unless the run asks. The field is STATED rather than omitted either way.
			EnablePBR: opts.pbr,
			// Safety checking is the provider's default and is left on: a refusal we can read is
			// worth more than a surprise on somebody else's terms.
			EnableSafetyChecker: true,
			// Omitted when empty: an empty texture_prompt is not «no hint», it is a hint that says
			// nothing, and the provider's own default behaviour is the better answer to that.
			TexturePrompt: strings.TrimSpace(req.TexturePrompt),
			// symmetry_mode and target_polycount are DELIBERATELY ABSENT: the provider's defaults
			// (auto / 30000) are right for a garment shown in a browser, and a value stated here
			// would freeze today's guess into every future build.
		}
		if !opts.texture {
			// The schema says texture_prompt «Requires should_texture to be true». An untextured
			// build has no texturing stage for the words to steer, so they do not travel — and the
			// designgen route reports the same empty string as what was sent (SentPrompt).
			mb.TexturePrompt = ""
		}
		if opts.detailed {
			// fal's «ultra mode» ($1.40, see defaultDetailedRequestUSD). Omitted otherwise, so a
			// standard build keeps the provider's own default and today's exact body.
			mb.GeometryResolution = geometryResolution2K
		}
		body = mb
	} else {
		// The hitem3d body, unchanged: NAMED slots, and the text field it has nowhere to put.
		//
		// ⚠ AND THE PER-RUN OPTIONS ARE NOT MAPPED ONTO IT. hitem3d is retired (selectable only by
		// FAL_MODEL_3D), its tiers are priced differently, and nobody has measured its
		// `resolution` values against «detailed». The build goes out at today's constants and the
		// gap is said out loud rather than papered over.
		if opts.stated {
			c.log.InfoContext(ctx, "3D: the configured model takes no per-run texture/PBR/quality "+
				"options, so the run's options were not sent",
				slog.String("model", model), slog.String("texture", req.Texture),
				slog.String("pbr", req.PBR), slog.String("quality", req.Quality))
		}
		h := submitBody{
			ExportFormat:        formatGLB,
			EnableTexture:       true,
			EnablePBR:           false,
			EnableSafetyChecker: true,
			FaceCount:           req.FaceCount,
			Resolution:          strings.TrimSpace(req.Resolution),
		}
		h.FrontImageURL, h.BackImageURL = views[0].url, views[1].url
		h.LeftImageURL, h.RightImageURL = views[2].url, views[3].url
		body = h
	}

	var out submitResponse
	if err := c.callJSON(ctx, http.MethodPost, "/"+model, body, &out, nil); err != nil {
		return "", err
	}
	id := strings.TrimSpace(out.RequestID)
	if id == "" {
		return "", submitLost(out.httpStatus)
	}
	// ⚠ THE DERIVED POLLING PATH IS CHECKED AGAINST THE PROVIDER'S OWN, ONCE, HERE. fal documents
	// that a model id with a sub-path (`hitem3d/hi3d/v3.0/multi-view-to-3d`) is submitted whole but
	// polled at its BASE (`hitem3d/hi3d`), and that rule is the sort of thing that is true until it
	// is not. The submit answer carries status_url, so the derivation can be compared with the
	// truth for free, at the one moment both are in hand — and a mismatch is said out loud rather
	// than discovered as an unresumable paid build.
	//
	// ⚠ IT MATTERS MORE SINCE THE DEFAULT MOVED TO `meshy/v7/multi-image-to-3d`: that the polling
	// namespace of THAT slug is `meshy/v7` is a derivation, not something fal's docs state, and the
	// first live submit is where it gets checked.
	c.checkQueuePath(ctx, model, id, out.StatusURL)
	return id, nil
}

// isMeshyFamily says whether a slug is one of meshy's own models on fal, which take the ORDERED
// `image_urls` list and a `texture_prompt`, rather than hitem3d's named slots.
//
// ⚠ IT IS A PREFIX ON THE SLUG BECAUSE THE SLUG IS THE ONLY THING THE PROVIDER GIVES US. fal
// publishes the same family under two spellings — `meshy/v7/…` and `fal-ai/meshy/v6/…` — and both
// are accepted here so a deployment pinned to v6 through FAL_MODEL_3D does not silently get the
// hitem3d body, which meshy would answer with a 422 that reads like our own bug.
//
// A SLUG THIS FUNCTION DOES NOT RECOGNISE TAKES THE NAMED BODY, which is the safe default: a named
// payload sent to a list-shaped endpoint is refused loudly by the provider's validator, while an
// ordered list sent to a named-slot endpoint is a build with NO front view at all.
func isMeshyFamily(model string) bool {
	m := strings.ToLower(strings.Trim(strings.TrimSpace(model), "/"))
	return strings.HasPrefix(m, "meshy/") || strings.HasPrefix(m, "fal-ai/meshy/")
}

// AcceptsTexturePrompt reports whether the CONFIGURED model has anywhere to put Request3D's
// TexturePrompt. Nil-safe.
//
// ⚠ IT EXISTS FOR THE HISTORY ROW, NOT FOR THE REQUEST. The submit needs no such question — it
// simply writes the body its model takes. What needs it is the caller that WRITES DOWN what the
// provider was told: a run panel showing a paragraph the provider never received is a lie about
// money that has already been spent, and hitem3d's payload has no text field at all.
func (c *Client) AcceptsTexturePrompt() bool {
	return c != nil && isMeshyFamily(c.Model())
}

// queuePath is the path the STATUS and RESULT endpoints hang off: the model id's first two
// segments — its namespace and name — with any sub-path dropped.
//
// A model id with fewer than two segments is returned as-is; it is not this function's business to
// invent a namespace, and the request will fail with a readable provider answer instead of a
// silently mangled URL.
func queuePath(model string) string {
	parts := strings.Split(strings.Trim(model, "/"), "/")
	if len(parts) <= 2 {
		return strings.Join(parts, "/")
	}
	return parts[0] + "/" + parts[1]
}

// QueueNamespace is the queue namespace a slug's requests are polled under (queuePath): what a
// resume needs to find a request when only part of its locator can be stored. "" for "".
func QueueNamespace(model string) string {
	model = strings.Trim(strings.TrimSpace(model), "/")
	if model == "" {
		return ""
	}
	return queuePath(model)
}

// retiredModel3D is every 3D slug this band has DEFAULTED to before the current one.
//
// ⚠ IT IS A MONEY LIST, NOT A HISTORY NOTE. A submit is the payment; it closes its attempt
// `accepted` with a request id and the build runs for minutes afterwards. The polling path is
// derived from a MODEL SLUG (queuePath), so a release that moves the default DURING those minutes
// sends the next poll to `meshy/v7/requests/<id>/status` for an id that was bought at
// `hitem3d/hi3d`. The provider answers 404, which this package reads — correctly, for an id it has
// no other reason to doubt — as the terminal ErrRequestNotFound. Two things are lost at once: the
// build, and its price, because the 404 lands on the STATUS call and x-fal-billable-units rides
// only on the RESULT one, so the ledger records the whole thing as free.
//
// A SLUG IS ADDED HERE WHEN THE DEFAULT MOVES OFF IT, and is not removed while any build could
// still be in flight under it — which, given PollTimeout and the retry window, is a matter of
// hours, not of releases.
// retired3D is every 3D model this band has DEFAULTED to before the current one, with what one
// build off it was worth.
//
// ⚠ IT IS A MONEY LIST, NOT A HISTORY NOTE, AND IT CARRIES A PRICE FOR THE SAME REASON IT CARRIES A
// SLUG. A submit is the payment; it closes its attempt `accepted` with a request id and the build
// runs for minutes afterwards. The polling path is derived from a MODEL SLUG (queuePath), so a
// release that moves the default DURING those minutes sends the next poll to
// `meshy/v7/requests/<id>/status` for an id that was bought at `hitem3d/hi3d`. The provider answers
// 404, which this package reads — correctly, for an id it has no other reason to doubt — as the
// terminal ErrRequestNotFound. Two things are lost at once: the build, and its price, because the
// 404 lands on the STATUS call and x-fal-billable-units rides only on the RESULT one, so the ledger
// records the whole thing as free.
//
// AND A RECOVERED BUILD MUST BE PRICED AS WHAT IT WAS BOUGHT AS. Finding it and then booking it at
// TODAY's model's price rewrites the money of a run frozen before the deploy: an hitem3d turntable
// that was estimated at $0.60 would settle at meshy's $1.20 for no reason a person could reconstruct
// from the row. So the estimate travels with the slug — see EstimatedRequestUSDFor.
//
// A MODEL IS ADDED HERE WHEN THE DEFAULT MOVES OFF IT, and is not removed while any build could
// still be in flight under it — which, given PollTimeout and the retry window, is a matter of
// hours, not of releases.
var retired3D = []struct {
	Model      string
	RequestUSD float64
}{
	// hitem3d's named-slot multi-view-to-3d: the default until the owner asked for meshy v7's
	// reconstruction. Both 3D runs on beta were submitted under it, at ~30 Meshy credits.
	{Model: "hitem3d/hi3d/v3.0/multi-view-to-3d", RequestUSD: 0.60},
}

// locateOutcome is what a search for a request id concluded, and its three values are three
// DIFFERENT claims that must not collapse into two.
type locateOutcome int

const (
	// locateDenied — every candidate was asked and every one of them said 404. The id really does
	// buy nothing, and the caller may end the run on it.
	locateDenied locateOutcome = iota
	// locateFound — a candidate knows the id, and its model is the one to poll and to price with.
	locateFound
	// ⚠ locateUnknown — AT LEAST ONE CANDIDATE COULD NOT BE ASKED (429, 5xx, a dead socket), so
	// the search is UNFINISHED, not exhausted. Reported as a denial it would discard a paid build
	// on the strength of a rate limit: ErrRequestNotFound classifies non-retryable
	// (CodeEmptyResponse), the run closes, and the only way back is a second submit — a second
	// charge — for a model that was already built and is sitting in the provider's queue.
	locateUnknown
)

// locateRequest looks for a request id in every OTHER queue namespace this deployment could have
// submitted it to.
//
// ⚠ WHY A SEARCH AND NOT A RECORDED SLUG. Nothing writes the model down. design_run_attempt carries
// the ROUTE («fal») and the provider's request id and nothing else — see entity.DesignRunAttempt —
// so on a RESUME the only thing in hand is an id. The choice was therefore between losing a paid
// build and looking in the places we could have put it, and a status lookup is free.
//
// WHAT IT COVERS AND WHAT IT DOES NOT. It covers the moves that actually happen: a default moved in
// code (in either direction, because both the current default and the retired ones are tried), and
// a deployment that switched between the default and FAL_MODEL_3D. It does NOT cover one override
// replaced by another — nobody recorded the first — and it says so here rather than pretending.
func (c *Client) locateRequest(ctx context.Context, current, requestID string) (string, locateOutcome, error) {
	candidates := []string{DefaultModel3D, c.cfg.Model3D}
	for _, r := range retired3D {
		candidates = append(candidates, r.Model)
	}
	return c.searchNamespaces(ctx, current, candidates, requestID)
}

// searchNamespaces asks each candidate slug's queue namespace (skipping current's and repeats)
// whether it knows requestID — locateRequest's search over any candidate list. Since G-03 r2 new
// requests carry their slug in the locator; the search remains the recovery for the ids stored bare
// before that (every generic route and the 3D route), over the namespaces the route is known to have
// used (RouteLegacyModels, CutoutLegacyModels, retired3D).
func (c *Client) searchNamespaces(ctx context.Context, current string, candidates []string, requestID string) (string, locateOutcome, error) {
	tried := map[string]bool{queuePath(strings.Trim(strings.TrimSpace(current), "/")): true}
	// THE FIRST TRANSIENT FAULT IS KEPT, NOT THE LAST, because it is the one closest to the model
	// the caller was already polling — and because a caller that reports «could not be asked» has
	// to be able to say WHY, in the provider's own words.
	var unreachable error
	for _, m := range candidates {
		m = strings.Trim(strings.TrimSpace(m), "/")
		q := queuePath(m)
		if q == "" || tried[q] {
			continue
		}
		tried[q] = true
		var st statusResponse
		path := "/" + q + "/requests/" + url.PathEscape(requestID) + "/status"
		err := c.callJSON(ctx, http.MethodGet, path, nil, &st, nil)
		switch {
		case err == nil:
			// LOUD, AND AT ERROR LEVEL, because it means a paid build was one release away from
			// being thrown away and recorded as free. The operator's action is to leave the retired
			// slug in retired3D until nothing can still be in flight under it.
			c.log.ErrorContext(ctx, "fal: a request id was not found under the configured model and was "+
				"located under another one; a model move left this paid build behind",
				slog.String("request_id", requestID), slog.String("configured", current),
				slog.String("found_under", m))
			return m, locateFound, nil
		case errors.Is(err, ErrRequestNotFound):
			// A REAL DENIAL: this namespace was asked and does not know the id.
			continue
		default:
			// ⚠ NOT A DENIAL. A 429 or a 500 says nothing whatever about where the request lives,
			// and treating silence as «no» is how a rate limit becomes a discarded paid build.
			if unreachable == nil {
				unreachable = err
			}
			c.log.WarnContext(ctx, "fal: a candidate namespace could not be asked about a request id; "+
				"the search is unfinished, not exhausted",
				slog.String("request_id", requestID), slog.String("candidate", m),
				slog.String("err", err.Error()))
		}
	}
	if unreachable != nil {
		return "", locateUnknown, unreachable
	}
	return "", locateDenied, nil
}

// notFoundYetUnsure turns an unfinished search into an error a worker can act on: RETRYABLE, and
// carrying the provider's own transient sentinel so the classifier reads it as weather rather than
// as «this id buys nothing».
func notFoundYetUnsure(requestID, current string, cause error) error {
	return fmt.Errorf("fal: request %s is not under %s and the other namespaces could not be asked, "+
		"so it is not yet known to be lost: %w", requestID, current, cause)
}

// checkQueuePath compares the polling path this client derived with the one the provider itself
// handed back. It never fails the submit — the model is already bought by the time this runs, and
// refusing here would throw away a paid build over a log line's worth of doubt.
func (c *Client) checkQueuePath(ctx context.Context, model, requestID, statusURL string) {
	statusURL = strings.TrimSpace(statusURL)
	if statusURL == "" {
		return
	}
	want := c.cfg.BaseURL + "/" + queuePath(model) + "/requests/" + url.PathEscape(requestID) + "/status"
	if strings.HasPrefix(statusURL, want) {
		return
	}
	c.log.ErrorContext(ctx, "fal: the derived polling path disagrees with the provider's own status_url; "+
		"a resumed collect will look in the wrong place",
		slog.String("model", model), slog.String("derived", want), slog.String("provider", statusURL))
}

// Collect performs ONE status lookup and, when the request has completed, downloads the artifacts
// into dst BEFORE returning. It is the whole answer to the expiring-link trap: there is no moment
// between «we learned the url» and «we have the bytes» in which a caller could store the url.
//
// A REQUEST THE CONFIGURED MODEL DOES NOT KNOW IS LOOKED FOR ELSEWHERE BEFORE IT IS GIVEN UP — see
// locateRequest. The id was bought under whatever slug was configured AT SUBMIT TIME, which is not
// necessarily the one configured now.
func (c *Client) Collect(ctx context.Context, requestID string, dst Sink) (*Result, error) {
	res, err := c.collect(ctx, ctx, c.cfg.Model3D, requestID, dst)
	if !errors.Is(err, ErrRequestNotFound) {
		return res, err
	}
	switch alt, out, cause := c.locateRequest(ctx, c.cfg.Model3D, requestID); out {
	case locateFound:
		return c.collect(ctx, ctx, alt, requestID, dst)
	case locateUnknown:
		// THE SEARCH DID NOT FINISH, so the 404 has not earned the right to be terminal.
		return nil, notFoundYetUnsure(requestID, c.cfg.Model3D, cause)
	}
	return res, err
}

// collect splits the two budgets: lookupCtx bounds the status and result requests (and is therefore
// what a poll ceiling constrains), fetchCtx bounds the download. They are the same context for a
// direct Collect and deliberately different inside Await.
func (c *Client) collect(lookupCtx, fetchCtx context.Context, model, requestID string, dst Sink) (*Result, error) {
	if !c.Enabled() {
		return nil, ErrNotConfigured
	}
	if strings.TrimSpace(requestID) == "" {
		return nil, fmt.Errorf("%w: empty request id", ErrUnexpectedResponse)
	}
	if dst.Model == nil {
		return nil, errors.New("fal: Sink.Model is required — there is nowhere to put the model")
	}
	base := "/" + queuePath(model) + "/requests/" + url.PathEscape(requestID)

	var st statusResponse
	if err := c.callJSON(lookupCtx, http.MethodGet, base+"/status", nil, &st, nil); err != nil {
		return nil, err
	}
	switch Status(strings.ToUpper(strings.TrimSpace(st.Status))) {
	case "":
		return nil, fmt.Errorf("%w: request %s came back with no status", ErrUnexpectedResponse, requestID)
	case StatusInQueue, StatusInProgress:
		// NOT CHARGED, AND THE OMISSION IS THE POINT: a request still being built has settled
		// nothing, and Await loops on this error rather than ending on it.
		return nil, fmt.Errorf("%w: request %s is %s (queue position %d)",
			ErrNotReady, requestID, st.Status, st.QueuePosition)
	case StatusCompleted:
		// fall through
	default:
		return nil, fmt.Errorf("%w: request %s has unknown status %q", ErrUnexpectedResponse, requestID, st.Status)
	}

	// ─── THE RESULT FETCH IS WHERE THE MONEY BECOMES KNOWN. x-fal-billable-units rides on THIS
	// response and on no other, so everything from here on carries the charge.
	var out resultBody
	var hdr http.Header
	if err := c.callJSON(lookupCtx, http.MethodGet, base, nil, &out, &hdr); err != nil {
		// A COMPLETED request whose result the provider refuses to serve is the provider ending
		// the job itself: terminal, and possibly billed. The status code carries the classification
		// and the charge cannot be read from a body we did not get.
		return nil, err
	}
	units, assumed := billableUnits(hdr)
	charged := func(err error) error { return chargedWith(err, units, requestID, model) }

	modelURL := out.modelURL()
	if modelURL == "" {
		// THE MOST EXPENSIVE LINE IN THE PACKAGE: the request COMPLETED, the model was built and
		// the units are spent — there is simply no file url to fetch it with.
		return nil, charged(fmt.Errorf("%w: request %s", ErrNoModel, requestID))
	}

	res := &Result{
		RequestID:     requestID,
		Model:         model,
		Format:        formatGLB,
		BillableUnits: units,
		UnitsAssumed:  assumed,
	}

	// The paid artifact first, and immediately. Everything above this line is a lookup; everything
	// below is the reason the lookup happened.
	n, sum, err := c.fetch(fetchCtx, modelURL, dst.Model, maxModelBytes)
	if err != nil {
		// A model too large for maxModelBytes, or a transfer that died: built and billed either
		// way. The bytes are lost; the money is not, and must not be.
		return nil, charged(fmt.Errorf("fal: downloading the model of request %s: %w", requestID, err))
	}
	res.ModelBytes, res.ModelSHA256 = n, sum

	// The thumbnail is a courtesy: it makes a tile, it is not what was paid for. Losing it must not
	// lose the run, so its failure is logged and the model still comes back.
	if thumb := strings.TrimSpace(out.Thumbnail.URL); thumb != "" && dst.Thumbnail != nil {
		tn, _, terr := c.fetch(fetchCtx, thumb, dst.Thumbnail, maxThumbnailBytes)
		if terr != nil {
			c.log.WarnContext(fetchCtx, "fal: thumbnail of a delivered model could not be fetched",
				slog.String("request_id", requestID), slog.String("err", terr.Error()))
		} else {
			res.ThumbnailBytes = tn
		}
	}
	return res, nil
}

// Await polls a submitted request until it finishes and returns its Result with the bytes already
// in dst. It is cancellable through ctx and bounded by PollTimeout.
//
// THE CEILING BOUNDS THE WAIT, NEVER THE FETCH. The lookups run under a derived context that
// expires at the ceiling; the download runs under the caller's context with a budget of its own. A
// single ceiling over both would cut the download of a request that finished in the last second of
// the wait — the units spent, the model built, and nothing to show but a link that expires.
func (c *Client) Await(ctx context.Context, requestID string, dst Sink) (*Result, error) {
	return c.AwaitAt(ctx, "", requestID, dst)
}

// AwaitAt is Await polled FIRST at the slug the request was SUBMITTED to (G-03 r2, Codex 4: the 3D
// route now stores "<slug>#<id>" like the generic routes, so a custom FAL_MODEL_3D replaced by
// another custom one no longer strands a paid build). An empty model is today's FAL_MODEL_3D — the
// legacy bare-id path. A 404 that outlives the grace still searches the known namespaces either way.
func (c *Client) AwaitAt(ctx context.Context, model, requestID string, dst Sink) (*Result, error) {
	if !c.Enabled() {
		return nil, ErrNotConfigured
	}
	model = strings.Trim(strings.TrimSpace(model), "/")
	if model == "" {
		model = c.cfg.Model3D
	}
	ceiling := c.cfg.PollTimeout
	waitCtx, cancel := context.WithTimeout(ctx, ceiling)
	defer cancel()

	timer := time.NewTimer(c.cfg.PollInterval)
	defer timer.Stop()

	// A 404 IN THE FIRST SECONDS IS A LAG, NOT AN ANSWER — see notFoundGrace. The grace never eats
	// more than half the ceiling, so an id that really is unknown still gets its terminal verdict
	// inside the wait rather than surfacing as a timeout, which points a worker the other way.
	grace := notFoundGrace
	if half := ceiling / 2; grace > half {
		grace = half
	}
	started := time.Now()

	// THE MODEL IS A LOCAL, NOT A READ OF THE CONFIGURATION EACH TIME AROUND, because a 404 that
	// outlives the grace is the one symptom of a model that moved under a build already paid for —
	// and the answer to it is to poll where the build actually is. Searched at most ONCE per wait:
	// a second search could only offer a namespace already tried, and would spin.
	searched := false

	for {
		res, err := c.collect(waitCtx, ctx, model, requestID, dst)
		if err == nil {
			return res, nil
		}
		if errors.Is(err, ErrRequestNotFound) {
			switch {
			case time.Since(started) < grace:
				err = fmt.Errorf("%w: request %s is not visible to the provider yet", ErrNotReady, requestID)
			case !searched:
				// PAST THE GRACE THE 404 IS ABOUT TO BECOME TERMINAL, and that is the one moment
				// worth spending a few free lookups on: past here the build is written off and,
				// because the 404 landed on the status call, written off as FREE.
				alt, out, cause := c.locateRequest(waitCtx, model, requestID)
				switch out {
				case locateFound:
					model = alt
					continue
				case locateUnknown:
					// ⚠ UNFINISHED, NOT EXHAUSTED — so the wait ENDS, retryably, rather than
					// polling a namespace already known to 404 until the ceiling. The verdict
					// carries the candidate's own transient sentinel, so the run backs off (30 s ×
					// 2ⁿ) and the NEXT pass resumes for free and searches again with the rate limit
					// expired. `searched` deliberately stays false: it exists to stop a search that
					// CONCLUDED, and this one did not.
					err = notFoundYetUnsure(requestID, model, cause)
				case locateDenied:
					searched = true
				}
				if waitCtx.Err() != nil {
					// THE SEARCH RAN OUT OF TIME RATHER THAN OUT OF PLACES, and those are different
					// verdicts about money. «This id buys nothing» is terminal and writes the build
					// off; «the wait ran out» keeps the id, and a later pass collects it for free.
					// An unfinished search may not be reported as an exhausted one.
					return nil, waitErr(ctx, requestID, ceiling)
				}
			}
		}
		if !errors.Is(err, ErrNotReady) {
			// A LOOKUP killed by the ceiling must read as a ceiling, not as a transport hiccup —
			// the request is very probably still alive and the id is still worth something. A
			// DOWNLOAD that failed on its own terms must NOT be relabelled that way, even though
			// the ceiling has by then usually passed: «the wait ran out, look again later» and «the
			// artifact would not come down» point a worker in opposite directions.
			if waitCtx.Err() != nil && (errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled)) {
				return nil, waitErr(ctx, requestID, ceiling)
			}
			return nil, err
		}
		select {
		case <-waitCtx.Done():
			return nil, waitErr(ctx, requestID, ceiling)
		case <-timer.C:
			timer.Reset(c.cfg.PollInterval)
		}
	}
}

// waitErr tells the two ways of running out of time apart. The caller's own cancellation is the
// caller's business; the ceiling is ours, and it is not a failure of the request.
func waitErr(parent context.Context, requestID string, ceiling time.Duration) error {
	if err := parent.Err(); err != nil {
		return fmt.Errorf("fal: waiting for request %s: %w", requestID, err)
	}
	return fmt.Errorf("%w: request %s, waited %s", ErrTimedOut, requestID, ceiling)
}

// billableUnits reads fal's own charge off the result response, and says whether it had to guess.
//
// ⚠ THE ASSUMPTION IS MADE HERE, NEXT TO THE READ, AND IT IS FLAGGED. fal bills marketplace models
// one unit per request unless the model reports otherwise, so a missing header means «one build»
// far more often than it means «free» — and recording NULL would leave a paid 3D build costing
// nothing in the ledger, which is the failure this whole accounting exists to prevent. The flag is
// what keeps the guess from being read as a measurement later.
func billableUnits(h http.Header) (float64, bool) {
	if h != nil {
		if raw := strings.TrimSpace(h.Get(billableUnitsHeader)); raw != "" {
			if v, err := strconv.ParseFloat(raw, 64); err == nil && v > 0 {
				return v, false
			}
		}
	}
	return 1, true
}

// --- wire types ---

// submitBody is the hitem3d multi-view-to-3d payload. Field names are the provider's.
type submitBody struct {
	FrontImageURL       string `json:"front_image_url,omitempty"`
	BackImageURL        string `json:"back_image_url,omitempty"`
	LeftImageURL        string `json:"left_image_url,omitempty"`
	RightImageURL       string `json:"right_image_url,omitempty"`
	ExportFormat        string `json:"export_format"`
	EnableTexture       bool   `json:"enable_texture"`
	EnablePBR           bool   `json:"enable_pbr"`
	EnableSafetyChecker bool   `json:"enable_safety_checker"`
	FaceCount           int    `json:"face_count,omitempty"`
	Resolution          string `json:"resolution,omitempty"`
}

// meshySubmitBody is meshy's multi-image-to-3d payload on fal. Field names are the provider's.
//
// ⚠ `image_urls` HAS NO NAMES IN IT, and that is the whole difference between the two families —
// see the package doc. The list is written front, back, left, right, occupied entries only.
type meshySubmitBody struct {
	ImageURLs           []string `json:"image_urls"`
	ShouldTexture       bool     `json:"should_texture"`
	EnablePBR           bool     `json:"enable_pbr"`
	EnableSafetyChecker bool     `json:"enable_safety_checker"`
	TexturePrompt       string   `json:"texture_prompt,omitempty"`
	// GeometryResolution — `standard | 2k` on fal's schema; only ever sent as 2k (a detailed
	// build) and OMITTED otherwise, which keeps a standard build's body byte-identical to the one
	// this transport sent before the option existed.
	GeometryResolution string `json:"geometry_resolution,omitempty"`
}

// geometryResolution2K is the one geometry_resolution value this transport sends. fal's schema:
// «Geometry resolution. Multi-image generation does not support 4k.» enum standard | 2k.
const geometryResolution2K = "2k"

// falOptions is a Request3D's three options, read once and validated once.
type falOptions struct {
	texture, pbr, detailed bool
	// stated — at least one option was given at all (for the hitem3d log line).
	stated bool
}

// resolveOptions reads the three option words. The empty string is today's constant for each; an unknown word,
// or PBR on an untextured build, is ErrBadOption — refused here, locally, before the submit that is
// the payment.
func resolveOptions(req Request3D) (falOptions, error) {
	o := falOptions{texture: true}
	switch t := strings.TrimSpace(req.Texture); t {
	case "", OptionOn:
	case OptionOff:
		o.texture = false
	default:
		return falOptions{}, fmt.Errorf("%w: texture %q is not on | off", ErrBadOption, t)
	}
	switch v := strings.TrimSpace(req.PBR); v {
	case "", OptionOff:
	case OptionOn:
		o.pbr = true
	default:
		return falOptions{}, fmt.Errorf("%w: pbr %q is not on | off", ErrBadOption, v)
	}
	switch q := strings.TrimSpace(req.Quality); q {
	case "", QualityStandard:
	case QualityDetailed:
		o.detailed = true
	default:
		return falOptions{}, fmt.Errorf("%w: quality %q is not standard | detailed", ErrBadOption, q)
	}
	if o.pbr && !o.texture {
		return falOptions{}, fmt.Errorf("%w: realistic materials (pbr) need a textured build — the "+
			"provider documents enable_pbr as «Requires should_texture to be true»", ErrBadOption)
	}
	o.stated = strings.TrimSpace(req.Texture) != "" || strings.TrimSpace(req.PBR) != "" ||
		strings.TrimSpace(req.Quality) != ""
	return o, nil
}

type submitResponse struct {
	RequestID   string `json:"request_id"`
	StatusURL   string `json:"status_url"`
	ResponseURL string `json:"response_url"`
	CancelURL   string `json:"cancel_url"`
	// httpStatus — the 2xx this answer arrived with, stamped by callJSON (statusStamped); what
	// submitLost books when the answer names no request id (B-13/A4).
	httpStatus int
}

func (s *submitResponse) stampStatus(status int) { s.httpStatus = status }

// statusStamped is an answer that keeps the 2xx it arrived with. callJSON stamps it after a clean
// decode, so a caller that finds the answer unusable (no id) can still report the response's real
// status instead of a 0 that means «no response at all».
type statusStamped interface{ stampStatus(status int) }

type statusResponse struct {
	Status        string `json:"status"`
	QueuePosition int    `json:"queue_position"`
}

// falFile is fal's file envelope. The url is read into it and NEVER leaves the package.
type falFile struct {
	URL         string `json:"url"`
	ContentType string `json:"content_type"`
	FileName    string `json:"file_name"`
	FileSize    int64  `json:"file_size"`
}

// resultBody reads BOTH families' answers, because the two spell the same file differently:
// hitem3d returns `model_mesh`, meshy returns `model_glb`. The thumbnail happens to coincide.
//
// ⚠ READING ONLY ONE OF THEM IS THE MOST EXPENSIVE POSSIBLE MISTAKE HERE. A model that was built
// and billed, whose url this client cannot see, ends as ErrNoModel with the charge attached — the
// money gone and nothing delivered — and it would look exactly like a provider defect.
type resultBody struct {
	ModelMesh falFile `json:"model_mesh"`
	ModelGLB  falFile `json:"model_glb"`
	Thumbnail falFile `json:"thumbnail"`
}

// modelURL is the one non-empty model link of the answer, whichever field carried it. Neither key
// is preferred over the other: a response carries one of them, and a response carrying both would
// have to be naming the same file twice.
func (r resultBody) modelURL() string {
	if u := strings.TrimSpace(r.ModelMesh.URL); u != "" {
		return u
	}
	return strings.TrimSpace(r.ModelGLB.URL)
}

// callJSON performs one control-plane request against the queue API and decodes its JSON answer.
// When hdr is non-nil it receives the response headers, which is how the billing header is read.
//
// EVERY ERROR IT RETURNS IS AN *aiprov.CallError (B-14) wrapping today's error, sentinel and sentence
// untouched: Provider fal, HTTPStatus the answer's status (0 when none arrived), and Engaged — the
// money fact designgen's classifier and ledger read instead of the sentinels.
//
// ⚠ ENGAGED IS ONLY EVER TRUE ON A POST, BECAUSE EVERY POST ON THIS API IS A SUBMIT, i.e. A PAYMENT
// (G-03, Codex 1), and a GET is a lookup of a job paid for at ITS submit. A status poll or a result
// fetch that fails — timed out, cut, garbled — moved no money whatever happened to it, and it stays
// RETRYABLE: looking again is free, and a poll read as «maybe paid» would close a bought job as lost.
// On a submit, whether the WHOLE request left the process is the one fact that separates «nothing
// complete reached fal» (dial, DNS, TLS, a body write cut half-way — retryable) from «fal may have
// queued and charged it» (ErrSubmitUnconfirmed — never resubmitted). The Go transport itself never
// replays a written POST that carries no Idempotency-Key header (net/http Request.isReplayable), so no
// second copy leaves from below.
//
// THE FACT IS aiprov.ObserveWrite's, the one observer every transport of the stack shares. This
// function used to carry its own copy of the same trace (B-14 removed it); a transport that rolls its
// own is a transport whose engaged flag drifts from the others'. What the copy had learnt, the shared
// one does too:
//
//   - ⚠ WroteRequest, NOT WroteHeaders (G-03 r2, Codex 2). WroteHeaders fires before a single byte of
//     the JSON body is written: a reset while the body is still going out — a definite non-submission,
//     fal holds an incomplete JSON it cannot enqueue — used to read as «maybe charged» and terminalize
//     the run. WroteRequest fires once the body is written and carries the write's own error; only
//     info.Err == nil marks the request as sent.
//   - GetConn resets the flag: it fires once per transport attempt (a redirect, or the transport's own
//     retry of a nothing-written request on a reused connection), so the flag describes the LAST
//     attempt, not the union of all of them.
//
// WHAT IS STILL CONSERVATIVE, SAID OUT LOUD: net/http calls WroteRequest before the final flush of
// its 4 KiB buffer (writeLoop flushes after Request.write returns), so a request whose last buffered
// bytes fail to flush is read as sent. That errs toward «unconfirmed» — a lost run, never a second
// purchase — and is the same reading internal/openrouter measured and accepted.
func (c *Client) callJSON(ctx context.Context, method, path string, in, out any, hdr *http.Header) error {
	submit := method == http.MethodPost
	var body io.Reader
	if in != nil {
		raw, err := json.Marshal(in)
		if err != nil {
			return fail(aiprov.CodeBadRequest, 0, false, false, fmt.Errorf("fal: encoding request: %w", err))
		}
		body = bytes.NewReader(raw)
	}

	reqCtx, cancel := context.WithTimeout(ctx, c.cfg.HTTPTimeout)
	defer cancel()
	reqCtx, wroteRequest := aiprov.ObserveWrite(reqCtx)

	req, err := http.NewRequestWithContext(reqCtx, method, c.cfg.BaseURL+path, body)
	if err != nil {
		return fail(aiprov.CodeBadRequest, 0, false, false, fmt.Errorf("fal: building request: %w", err))
	}
	// fal's own scheme: `Authorization: Key <FAL_KEY>`, not Bearer.
	req.Header.Set("Authorization", "Key "+c.apiKey())
	req.Header.Set("Accept", "application/json")
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.http.Do(req)
	if err != nil {
		err = fmt.Errorf("fal: %s %s: %w", method, path, err)
		code := aiprov.Interruption(reqCtx, err)
		switch {
		case !submit:
			return fail(code, 0, false, true, err)
		case wroteRequest():
			return fail(code, 0, true, false, fmt.Errorf("%w: %w", ErrSubmitUnconfirmed, err))
		default:
			return fail(code, 0, false, code != aiprov.CodeCanceled, err)
		}
	}
	defer resp.Body.Close()

	status := resp.StatusCode
	if status < 200 || status > 299 {
		raw, _ := readCapped(resp.Body, maxErrorBodyBytes)
		// ⚠ THE SENTINEL AND THE CODE DISAGREE ON A 408, AND THAT IS LEFT SO ON PURPOSE (B-14).
		// statusErrorFrom folds 408 (with 422 and every other unnamed 4xx) into ErrBadRequest, which is
		// the word inside; the matrix calls a 408 weather (provider_error, retryable) — true of a
		// status poll, which stays so. On a SUBMIT the CallError overrides both (B-13/A3, below).
		err := statusErrorFrom(status, raw, method, path)
		code, retryable := aiprov.ClassifyStatus(status)
		if submit && (status >= 500 || status == http.StatusRequestTimeout) {
			// submitServerError stays the fal-side truth about a 5xx on a submit: a bare 503 is the
			// queue refusing work (not engaged, retryable, as the matrix says); every other 5xx, and
			// any 5xx naming a request id, may have been enqueued and billed — ENGAGED, because fal's
			// gateway can lose the queue's answer after the enqueue.
			//
			// ⚠ AND A 408 WITH THEM (B-13/A3, Codex B-14 review P1 #1): a server or a gateway giving up
			// on a request whose whole body it may already have taken. The job may be queued and billed
			// behind it, and a retry would buy a second one beside a first whose id is gone — so it is
			// ErrSubmitUnconfirmed like a 502, the row `unknown`, the run closed for reconciliation.
			if err = submitServerError(status, raw, err); errors.Is(err, ErrSubmitUnconfirmed) {
				return fail(code, status, true, false, err)
			}
		}
		return fail(code, status, false, retryable, err)
	}
	if hdr != nil {
		*hdr = resp.Header
	}

	// ─── FROM HERE ON A 2xx. On a submit the queue accepted SOMETHING, so whatever breaks now breaks
	// after the payment: engaged, never resubmitted. On a lookup it is a free read that can be redone.
	broken := func(code string, err error) error {
		if submit {
			return fail(code, status, true, false, fmt.Errorf("%w: %w", ErrSubmitUnconfirmed, err))
		}
		return fail(code, status, false, true, err)
	}
	raw, err := readCapped(resp.Body, maxAPIResponseBytes)
	if err != nil {
		code := aiprov.CodeTooLarge
		if !errors.Is(err, ErrTooLarge) {
			code = aiprov.Interruption(reqCtx, err)
		}
		return broken(code, fmt.Errorf("fal: reading %s %s: %w", method, path, err))
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return broken(aiprov.CodeProviderError, fmt.Errorf("%w: %s %s: %v", ErrUnexpectedResponse, method, path, err))
	}
	if s, ok := out.(statusStamped); ok {
		s.stampStatus(status)
	}
	return nil
}

// fail wraps today's error in the CallError every design transport returns (B-14). Provider is the
// billing key; the classification (Code, Retryable) is aiprov.ClassifyStatus / aiprov.Interruption's.
func fail(code string, status int, engaged, retryable bool, err error) *aiprov.CallError {
	return &aiprov.CallError{
		Provider:   entity.AIProviderFal,
		Code:       code,
		HTTPStatus: status,
		Engaged:    engaged,
		Retryable:  retryable,
		Err:        err,
	}
}

// statusErrorFrom turns a non-2xx answer into the sentinel that says what to DO about it.
//
// ⚠ THE TWO 404s ARE DIFFERENT FAULTS AND MUST NOT SHARE A SENTENCE. A 404 on the SUBMIT path means
// the model slug is gone — a setting to fix, and the exact failure that once took down both AI
// features here while reading as a temporary outage. A 404 on the STATUS/RESULT path means the
// request id is worthless — a run to abandon. They are told apart by the METHOD AND PATH, never by
// the provider's English sentence, so a reworded message cannot silently reclassify either.
//
// It takes the body already read: callJSON reads it once, because a 5xx answering a submit is also
// searched for a request id (submitServerError).
func statusErrorFrom(code int, raw []byte, method, path string) error {
	detail := providerMessage(raw)

	switch code {
	case http.StatusUnauthorized, http.StatusForbidden:
		return fmt.Errorf("%w (HTTP %d): %s", ErrUnauthorized, code, detail)
	case http.StatusPaymentRequired:
		return fmt.Errorf("%w (HTTP %d): %s", ErrOutOfCredit, code, detail)
	case http.StatusTooManyRequests:
		return fmt.Errorf("%w (HTTP %d): %s", ErrRateLimited, code, detail)
	case http.StatusNotFound:
		if strings.Contains(path, "/requests/") {
			return fmt.Errorf("%w (HTTP 404): %s", ErrRequestNotFound, detail)
		}
		return fmt.Errorf("%w (HTTP 404): %s — model %q", ErrModelUnavailable, detail, strings.TrimPrefix(path, "/"))
	case http.StatusGone, http.StatusConflict:
		// The provider ended the job itself. Terminal: nothing about it improves on a retry.
		return fmt.Errorf("%w (HTTP %d): %s", ErrTaskFailed, code, detail)
	}
	// EVERY OTHER 4xx IS «WE SENT SOMETHING WRONG», AND THAT IS NOT WEATHER — 422 above all, which
	// is what fal answers to a payload its validator rejects. 5xx keeps the generic form: a server
	// failing today may well answer tomorrow.
	if code >= 400 && code < 500 {
		return fmt.Errorf("%w: %s %s: HTTP %d: %s", ErrBadRequest, method, path, code, detail)
	}
	return fmt.Errorf("fal: %s %s: HTTP %d: %s", method, path, code, detail)
}

// submitRetryableStatus — the ONE 5xx answer to a SUBMIT that says the queue did not take the
// request: 503 Service Unavailable, the queue refusing work outright. It is an explicit refusal —
// the service is saying «not now», not «something went wrong on the way».
//
// ⚠ 502 AND 504 ARE NOT HERE, AND THAT IS DELIBERATE (G-03 r3, Codex BLOCKER 1). Both are a
// GATEWAY's word about the hop behind it, and neither says in which direction the hop failed: a 504
// is the gateway giving up waiting for the queue's ANSWER, a 502 is it receiving a broken one — and
// the queue may have enqueued (and billed) the job before that answer was lost on its way back.
// fal's own queue client retries 502/503/504 on a submit (fal-js libs/client/src/retry.ts), but a
// client's retry policy is not a guarantee that acceptance was impossible, and fal documents no
// submit idempotency key to make a duplicate harmless. So only the bare 503 is repeated; an
// ambiguous gateway status is ErrSubmitUnconfirmed — it can lose a run, never buy the job twice.
func submitRetryableStatus(code int) bool {
	return code == http.StatusServiceUnavailable
}

// submitServerError classifies a 5xx — or a 408 (B-13/A3), which it reads as unconfirmed like every
// status that is not the bare 503 — that answered a SUBMIT (G-03 r2, Codex 2; r3, Codex BLOCKER 1).
//
//   - a body naming a request_id: the queue DID accept the job, whatever the status says — it is
//     paid, and the id rides the error (ErrSubmitUnconfirmed, terminal) so last_error carries what
//     a person reconciles by;
//   - 503 without an id: an explicit «service unavailable» refusal (see submitRetryableStatus) — the
//     ordinary retryable error;
//   - every OTHER 5xx (500, 501, 502, 504, 505…): ErrSubmitUnconfirmed. A 500 is the queue's own
//     handler failing, a 502/504 is a gateway losing the queue's answer — and nothing documents
//     whether either happened before or after the enqueue; the conservative reading is the one that
//     can only lose a run, never buy the job twice.
func submitServerError(code int, raw []byte, err error) error {
	var named struct {
		RequestID string `json:"request_id"`
	}
	if json.Unmarshal(raw, &named) == nil {
		if id := strings.TrimSpace(named.RequestID); id != "" {
			return fmt.Errorf("%w: the answer named request %s: %w", ErrSubmitUnconfirmed, id, err)
		}
	}
	if submitRetryableStatus(code) {
		return err
	}
	return fmt.Errorf("%w: %w", ErrSubmitUnconfirmed, err)
}

// providerMessage extracts the provider's own sentence from an error body, falling back to the raw
// body. DISPLAY ONLY — never used to classify a fault, so that a reworded provider message cannot
// silently change how an error is handled.
func providerMessage(raw []byte) string {
	// fal answers `{"detail": "..."}` for most faults and `{"detail": [{"msg": "..."}]}` for
	// validation ones. Both are read; neither decides anything.
	var asString struct {
		Detail  string `json:"detail"`
		Message string `json:"message"`
		Error   string `json:"error"`
	}
	if err := json.Unmarshal(raw, &asString); err == nil {
		for _, m := range []string{asString.Detail, asString.Message, asString.Error} {
			if m = strings.TrimSpace(m); m != "" {
				return m
			}
		}
	}
	var asList struct {
		Detail []struct {
			Msg  string `json:"msg"`
			Type string `json:"type"`
		} `json:"detail"`
	}
	if err := json.Unmarshal(raw, &asList); err == nil && len(asList.Detail) > 0 {
		parts := make([]string, 0, len(asList.Detail))
		for _, d := range asList.Detail {
			if m := strings.TrimSpace(d.Msg); m != "" {
				parts = append(parts, m)
			}
		}
		if len(parts) > 0 {
			return strings.Join(parts, "; ")
		}
	}
	if s := strings.TrimSpace(string(raw)); s != "" {
		return s
	}
	return "no body"
}

// fetch downloads one artifact into dst and returns the byte count and the sha256 of what was
// written.
//
// IT SENDS NO AUTHORIZATION HEADER. The url comes out of the provider's JSON and points at whatever
// host the provider names; attaching our API key to a request at an address we did not choose would
// hand the key to that host. fal's artifact links are pre-signed and need no key.
func (c *Client) fetch(ctx context.Context, rawURL string, dst io.Writer, limit int64) (int64, string, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return 0, "", fmt.Errorf("fal: unparsable artifact url: %w", err)
	}
	if u.Scheme != "https" && u.Scheme != "http" {
		return 0, "", fmt.Errorf("fal: refusing artifact url with scheme %q", u.Scheme)
	}

	fetchCtx, cancel := context.WithTimeout(ctx, c.cfg.DownloadTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(fetchCtx, http.MethodGet, u.String(), nil)
	if err != nil {
		return 0, "", fmt.Errorf("fal: building artifact request: %w", err)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, "", fmt.Errorf("fal: fetching artifact: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := readCapped(resp.Body, maxErrorBodyBytes)
		return 0, "", fmt.Errorf("fal: fetching artifact: HTTP %d: %s", resp.StatusCode, providerMessage(raw))
	}

	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(dst, h), newCapReader(resp.Body, limit))
	if err != nil {
		return n, "", err
	}
	return n, hex.EncodeToString(h.Sum(nil)), nil
}

// readCapped reads at most limit bytes and REFUSES if there are more, instead of silently handing
// back a prefix. A JSON body cut at the boundary fails to parse and blames the provider.
func readCapped(r io.Reader, limit int64) ([]byte, error) {
	raw, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(raw)) > limit {
		return nil, fmt.Errorf("%w: over %d bytes", ErrTooLarge, limit)
	}
	return raw, nil
}

// capReader is the streaming half of the same rule: it fails once more than limit bytes have passed
// through it. io.LimitReader would report a clean EOF at the boundary, and a truncated GLB that
// arrived «successfully» is indistinguishable from a corrupt one.
type capReader struct {
	r     io.Reader
	left  int64
	limit int64
	err   error
}

func newCapReader(r io.Reader, limit int64) *capReader {
	return &capReader{r: r, left: limit, limit: limit}
}

func (c *capReader) Read(p []byte) (int, error) {
	if c.err != nil {
		return 0, c.err
	}
	n, err := c.r.Read(p)
	c.left -= int64(n)
	if c.left < 0 {
		c.err = fmt.Errorf("%w: over %d bytes", ErrTooLarge, c.limit)
		return 0, c.err
	}
	return n, err
}

// validateImageRef insists the provider will be able to fetch the reference itself.
func validateImageRef(raw string) error {
	if raw == "" {
		return fmt.Errorf("%w: empty reference", ErrBadImageURL)
	}
	if strings.HasPrefix(raw, "data:") {
		return nil
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrBadImageURL, err)
	}
	if (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return fmt.Errorf("%w: got %q", ErrBadImageURL, raw)
	}
	return nil
}
