// Package fal is the client for fal.ai's QUEUE API — the transport behind the DESIGN band's 3D
// route (K-10, owner's own words: «для 3d как референсы должны использоваться
// hitem3d/hi3d/v3.0/multi-view-to-3d и нам нужна интеграция с fal.ai и что бы мы могли туда
// подавать наши фронт бэк и так далее»).
//
// WHY A SECOND 3D TRANSPORT AND NOT A SECOND MESHY METHOD. The two providers do not take the same
// request. Meshy's multi-image-to-3d takes an ORDERED LIST and reads image_urls[0] as the front;
// hitem3d takes NAMED SLOTS — front_image_url, back_image_url, left_image_url, right_image_url —
// which is exactly the shape the bench already has, and is what the owner asked for by name. An
// ordered list flattens that naming and loses the one thing this provider is better at.
//
// THE PACKAGE IS SHAPED LIKE internal/meshy ON PURPOSE, down to the sentinel names and the
// Submit / Collect / Await split. It is the same problem — a paid submit, then minutes of building,
// then artifacts behind expiring links — and designgen's 3D pass already knows that shape. Two
// spellings of one mechanism would be two things to remember at the one call site that reads them.
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
)

const (
	// defaultBaseURL is fal's queue root. Overridable with FAL_BASE_URL, which exists for tests
	// and for a proxy — not as a knob anybody is expected to set.
	defaultBaseURL = "https://queue.fal.run"

	// DefaultModel3D is the multi-view-to-3d slug the owner named. It is LOAD-BEARING in the way
	// orimages.DefaultModel is: a slug the provider retires turns every 3D press into a 404 in a
	// fifth of a second, and this repository has already paid for that once — a dead model slug
	// killed both AI features at once and read, on the screen, as a temporary provider fault.
	//
	// THAT IS WHY A 404 ON THE SUBMIT PATH IS ITS OWN SENTINEL HERE (ErrModelUnavailable) and not
	// weather: «there is no such model» and «the service is busy» send a person to two different
	// places, and only one of them is a place where the problem actually is.
	DefaultModel3D = "hitem3d/hi3d/v3.0/multi-view-to-3d"

	// formatGLB is the only export format asked for or accepted. The band shows GLB and only GLB,
	// exactly as on the Meshy route.
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
	// RATHER THAN ZERO — the same argument meshy.defaultCreditUSD makes, and for the same ledger:
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
	defaultRequestUSD = 0.60

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

// ErrBadImageURL is returned for a reference the provider could not fetch itself.
var ErrBadImageURL = errors.New("fal: image references must be public http(s) urls or data: uris")

// ErrUnexpectedResponse is returned when fal answers 2xx with something this client cannot read.
var ErrUnexpectedResponse = errors.New("fal: unreadable response from the provider")

// ErrTooLarge is returned when an artifact or an envelope exceeds its cap. Refusal, not truncation.
var ErrTooLarge = errors.New("fal: artifact is larger than the allowed maximum")

// ChargedError marks a failure the provider HAS ALREADY BILLED, and carries the charge.
//
// THE SHAPE IS meshy.ChargedError / recraft.ChargedError, DELIBERATELY. designgen's 3D pass already
// reads that spelling; a third mechanism for one fact would be a third thing to remember at the one
// call site that reads them.
//
// THE UNIT IS BILLABLE UNITS, NOT DOLLARS, for the same reason Meshy's is credits: the rate is
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
}

func (e *ChargedError) Error() string {
	return fmt.Sprintf("%v [billed %v units]", e.Err, e.Units)
}

func (e *ChargedError) Unwrap() error { return e.Err }

// chargedWith attaches a charge to an error, and ONLY when the provider named one. Zero units means
// «the provider did not say», which is not the same as «free».
func chargedWith(err error, units float64, requestID string) error {
	if err == nil || units <= 0 {
		return err
	}
	return &ChargedError{Err: err, Units: units, RequestID: requestID}
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
	HTTPTimeout     time.Duration `mapstructure:"http_timeout"`     // FAL_HTTP_TIMEOUT
	PollInterval    time.Duration `mapstructure:"poll_interval"`    // FAL_POLL_INTERVAL
	PollTimeout     time.Duration `mapstructure:"poll_timeout"`     // FAL_POLL_TIMEOUT
	DownloadTimeout time.Duration `mapstructure:"download_timeout"` // FAL_DOWNLOAD_TIMEOUT
	UnitUSD         float64       `mapstructure:"unit_usd"`         // FAL_UNIT_USD; <=0 = defaultUnitUSD
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
	return fmt.Sprintf("fal.Config{APIKey:%s BaseURL:%s Model3D:%s HTTPTimeout:%s PollInterval:%s "+
		"PollTimeout:%s DownloadTimeout:%s UnitUSD:%v}",
		key, c.BaseURL, c.Model3D, c.HTTPTimeout, c.PollInterval, c.PollTimeout, c.DownloadTimeout, c.UnitUSD)
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
func (c *Client) Enabled() bool { return c != nil && c.cfg.APIKey != "" }

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

// CostUSD converts billable units into money at the configured rate (FAL_UNIT_USD). It is the only
// place that knows the conversion, so the price written into an attempt row and the price shown on
// a button cannot drift apart.
func (c *Client) CostUSD(units float64) decimal.Decimal {
	if c == nil || units <= 0 {
		return decimal.Zero
	}
	// ТАРИФ НЕ ЗАДАН — ЗНАЧИТ УМНОЖАТЬ НЕ НА ЧТО. Число единиц провайдер называет честно, но что
	// именно он ими меряет — секунды, мегапиксели, запросы — знает только его прайс. Умножение на
	// выдуманный тариф даёт не оценку, а уверенное враньё, тем более убедительное, чем больше
	// единиц вернул провайдер. Без тарифа отвечаем ОДНОЙ оценкой за сборку.
	if c.cfg.UnitUSD <= 0 {
		return decimal.NewFromFloat(defaultRequestUSD)
	}
	return decimal.NewFromFloat(c.cfg.UnitUSD).Mul(decimal.NewFromFloat(units))
}

// Request3D is one multi-view-to-3d job. The four views are NAMED rather than ordered, which is the
// whole reason this provider was asked for: the bench already knows which plate is the front.
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
	body := submitBody{
		ExportFormat: formatGLB,
		// A flat drawing becomes a garment only with its colour and print on it; an untextured
		// mesh answers a different question than the one the designer asked.
		EnableTexture: true,
		// PBR maps quadruple the download for lighting nuance a product tile does not show. The
		// provider's own default here is TRUE, so this field is STATED rather than omitted.
		EnablePBR: false,
		// Safety checking is the provider's default and is left on: a refusal we can read is worth
		// more than a surprise on somebody else's terms.
		EnableSafetyChecker: true,
		FaceCount:           req.FaceCount,
		Resolution:          strings.TrimSpace(req.Resolution),
	}
	for i, pair := range []struct {
		dst *string
		src string
	}{
		{&body.FrontImageURL, front},
		{&body.BackImageURL, strings.TrimSpace(req.BackURL)},
		{&body.LeftImageURL, strings.TrimSpace(req.LeftURL)},
		{&body.RightImageURL, strings.TrimSpace(req.RightURL)},
	} {
		if pair.src == "" {
			continue
		}
		if err := validateImageRef(pair.src); err != nil {
			return "", fmt.Errorf("view %d: %w", i, err)
		}
		*pair.dst = pair.src
	}

	var out submitResponse
	if err := c.callJSON(ctx, http.MethodPost, "/"+model, body, &out, nil); err != nil {
		return "", err
	}
	id := strings.TrimSpace(out.RequestID)
	if id == "" {
		return "", fmt.Errorf("%w: submit returned no request id", ErrUnexpectedResponse)
	}
	// ⚠ THE DERIVED POLLING PATH IS CHECKED AGAINST THE PROVIDER'S OWN, ONCE, HERE. fal documents
	// that a model id with a sub-path (`hitem3d/hi3d/v3.0/multi-view-to-3d`) is submitted whole but
	// polled at its BASE (`hitem3d/hi3d`), and that rule is the sort of thing that is true until it
	// is not. The submit answer carries status_url, so the derivation can be compared with the
	// truth for free, at the one moment both are in hand — and a mismatch is said out loud rather
	// than discovered as an unresumable paid build.
	c.checkQueuePath(ctx, model, id, out.StatusURL)
	return id, nil
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
func (c *Client) Collect(ctx context.Context, requestID string, dst Sink) (*Result, error) {
	return c.collect(ctx, ctx, c.cfg.Model3D, requestID, dst)
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
	charged := func(err error) error { return chargedWith(err, units, requestID) }

	modelURL := strings.TrimSpace(out.ModelMesh.URL)
	if modelURL == "" {
		// THE MOST EXPENSIVE LINE IN THE PACKAGE: the request COMPLETED, the model was built and
		// the units are spent — there is simply no file url to fetch it with.
		return nil, charged(fmt.Errorf("%w: request %s", ErrNoModel, requestID))
	}

	res := &Result{
		RequestID:     requestID,
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
	if !c.Enabled() {
		return nil, ErrNotConfigured
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

	for {
		res, err := c.collect(waitCtx, ctx, c.cfg.Model3D, requestID, dst)
		if err == nil {
			return res, nil
		}
		if errors.Is(err, ErrRequestNotFound) && time.Since(started) < grace {
			err = fmt.Errorf("%w: request %s is not visible to the provider yet", ErrNotReady, requestID)
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

type submitResponse struct {
	RequestID   string `json:"request_id"`
	StatusURL   string `json:"status_url"`
	ResponseURL string `json:"response_url"`
	CancelURL   string `json:"cancel_url"`
}

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

type resultBody struct {
	ModelMesh falFile `json:"model_mesh"`
	Thumbnail falFile `json:"thumbnail"`
}

// callJSON performs one control-plane request against the queue API and decodes its JSON answer.
// When hdr is non-nil it receives the response headers, which is how the billing header is read.
func (c *Client) callJSON(ctx context.Context, method, path string, in, out any, hdr *http.Header) error {
	var body io.Reader
	if in != nil {
		raw, err := json.Marshal(in)
		if err != nil {
			return fmt.Errorf("fal: encoding request: %w", err)
		}
		body = bytes.NewReader(raw)
	}

	reqCtx, cancel := context.WithTimeout(ctx, c.cfg.HTTPTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, method, c.cfg.BaseURL+path, body)
	if err != nil {
		return fmt.Errorf("fal: building request: %w", err)
	}
	// fal's own scheme: `Authorization: Key <FAL_KEY>`, not Bearer.
	req.Header.Set("Authorization", "Key "+c.cfg.APIKey)
	req.Header.Set("Accept", "application/json")
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("fal: %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return c.statusError(resp, method, path)
	}
	if hdr != nil {
		*hdr = resp.Header
	}

	raw, err := readCapped(resp.Body, maxAPIResponseBytes)
	if err != nil {
		return fmt.Errorf("fal: reading %s %s: %w", method, path, err)
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("%w: %s %s: %v", ErrUnexpectedResponse, method, path, err)
	}
	return nil
}

// statusError turns a non-2xx answer into the sentinel that says what to DO about it.
//
// ⚠ THE TWO 404s ARE DIFFERENT FAULTS AND MUST NOT SHARE A SENTENCE. A 404 on the SUBMIT path means
// the model slug is gone — a setting to fix, and the exact failure that once took down both AI
// features here while reading as a temporary outage. A 404 on the STATUS/RESULT path means the
// request id is worthless — a run to abandon. They are told apart by the METHOD AND PATH, never by
// the provider's English sentence, so a reworded message cannot silently reclassify either.
func (c *Client) statusError(resp *http.Response, method, path string) error {
	raw, _ := readCapped(resp.Body, maxErrorBodyBytes)
	detail := providerMessage(raw)

	switch resp.StatusCode {
	case http.StatusUnauthorized, http.StatusForbidden:
		return fmt.Errorf("%w (HTTP %d): %s", ErrUnauthorized, resp.StatusCode, detail)
	case http.StatusPaymentRequired:
		return fmt.Errorf("%w (HTTP %d): %s", ErrOutOfCredit, resp.StatusCode, detail)
	case http.StatusTooManyRequests:
		return fmt.Errorf("%w (HTTP %d): %s", ErrRateLimited, resp.StatusCode, detail)
	case http.StatusNotFound:
		if strings.Contains(path, "/requests/") {
			return fmt.Errorf("%w (HTTP 404): %s", ErrRequestNotFound, detail)
		}
		return fmt.Errorf("%w (HTTP 404): %s — model %q", ErrModelUnavailable, detail, strings.TrimPrefix(path, "/"))
	case http.StatusGone, http.StatusConflict:
		// The provider ended the job itself. Terminal: nothing about it improves on a retry.
		return fmt.Errorf("%w (HTTP %d): %s", ErrTaskFailed, resp.StatusCode, detail)
	}
	// EVERY OTHER 4xx IS «WE SENT SOMETHING WRONG», AND THAT IS NOT WEATHER — 422 above all, which
	// is what fal answers to a payload its validator rejects. 5xx keeps the generic form: a server
	// failing today may well answer tomorrow.
	if resp.StatusCode >= 400 && resp.StatusCode < 500 {
		return fmt.Errorf("%w: %s %s: HTTP %d: %s", ErrBadRequest, method, path, resp.StatusCode, detail)
	}
	return fmt.Errorf("fal: %s %s: HTTP %d: %s", method, path, resp.StatusCode, detail)
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
