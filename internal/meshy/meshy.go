package meshy

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
	"strings"
	"time"

	"github.com/shopspring/decimal"
)

const (
	// defaultBaseURL is the Meshy API root. Overridable with MESHY_BASE_URL, which exists for
	// tests and for a future regional host — not as a knob anybody is expected to set.
	defaultBaseURL = "https://api.meshy.ai"

	// multiImagePath is the multi-image-to-3d resource: POST creates a task, GET /{id} reads it.
	// Verified against https://docs.meshy.ai/en/api/multi-image-to-3d on 2026-08-30.
	multiImagePath = "/openapi/v1/multi-image-to-3d"

	// formatGLB is the only target format asked for or accepted. See the doc comment.
	formatGLB = "glb"

	// MaxImages is the provider's ceiling on reference views for one task, and MinImages its floor.
	// THE FIRST URL IS THE FRONT VIEW: Meshy treats image_urls[0] as the primary/frontal reference
	// and reads the rest as supporting angles, so the order of this slice is meaning, not style.
	MinImages = 1
	MaxImages = 4

	// MaxTexturePrompt is the provider's ceiling on texture_prompt, in characters. Meshy answers
	// 400 above it.
	//
	// ⚠ IT IS CHECKED LOCALLY, BY THE SAME ARGUMENT AS MaxImages: this is something we can be
	// certain about without asking, and the 400 it saves is not free. A 400 read as weather —
	// which is exactly what an unrecognised provider error is — is retried to the attempt cap,
	// five rounds of sending the same too-long text, and the history row then says "failed after 5
	// attempts · provider_unavailable" instead of naming a sentence that is 40 characters too long.
	MaxTexturePrompt = 600

	// defaultHTTPTimeout bounds ONE control-plane request (submit or lookup). Both answer with a
	// small JSON object; seconds are plenty, and a provider that cannot answer a status question in
	// half a minute is not answering.
	defaultHTTPTimeout = 30 * time.Second

	// defaultPollInterval is how long Await sleeps between status lookups. Lookups are free, but
	// they are not free of rate limits, and a multi-image task takes minutes: five seconds is
	// roughly a hundred lookups over the whole ceiling, which no rate limit objects to.
	defaultPollInterval = 5 * time.Second

	// defaultPollTimeout is the ceiling on WAITING for a task (not on fetching its result). Meshy
	// quotes minutes for multi-image-to-3d; twelve leaves room for a queue without letting a worker
	// slot sit on a task that will never land. Hitting it yields ErrTimedOut, and the task id is in
	// the error: the model may still finish, and a later Collect can still fetch it — for three days.
	defaultPollTimeout = 12 * time.Minute

	// notFoundGrace is how long Await keeps reading a 404 as "the provider has not caught up yet"
	// instead of as the terminal "this id buys nothing".
	//
	// ⚠ IT EXISTS BECAUSE THE FIRST LOOKUP HAS NO PAUSE IN FRONT OF IT. Generate submits and polls
	// in the same breath, and the submit is THE PAYMENT. If a create call can return an id that the
	// retrieve endpoint does not serve for a moment — an ordinary shape for a read path behind a
	// write — then the strict reading throws away a model that was paid for one second earlier, and
	// the only route back to a discarded id is a second charge. Thirty seconds is several poll
	// intervals of patience against a fault whose whole cost is a few free lookups, and it does not
	// weaken the terminal verdict: an id nobody knows is still terminal thirty seconds later.
	notFoundGrace = 30 * time.Second

	// defaultDownloadTimeout bounds fetching one artifact. It is generous on purpose: this is the
	// step that must not be cut short (see doc.go), and a slow CDN is a bad reason to lose a paid
	// model.
	defaultDownloadTimeout = 5 * time.Minute

	// defaultCreditUSD is the fallback price of one Meshy credit in USD, used when MESHY_CREDIT_USD
	// is unset. It is an ESTIMATE from the published plans (~30 credits ≈ $0.60 for one model) and
	// it is here so that an unconfigured deployment records a plausible cost rather than zero —
	// "this run was free" is a worse lie than "this run cost about sixty cents". Set the variable to
	// the real rate of the active plan.
	defaultCreditUSD = 0.02

	// maxAPIResponseBytes caps a control-plane JSON body. Task objects are a few kilobytes; a
	// megabyte is already a sign that something other than Meshy is answering.
	maxAPIResponseBytes = 1 << 20

	// maxModelBytes and maxThumbnailBytes cap the artifacts. Both REFUSE at the limit instead of
	// truncating: a GLB cut at the boundary is a file that opens in nothing and looks like a
	// provider defect for as long as it takes somebody to compare byte counts.
	maxModelBytes     = 64 << 20
	maxThumbnailBytes = 8 << 20

	// maxErrorBodyBytes is how much of a failed response is quoted back in the error message.
	maxErrorBodyBytes = 4 << 10
)

// Status is the lifecycle of a Meshy task, spelled exactly as the provider spells it.
type Status string

const (
	StatusPending    Status = "PENDING"
	StatusInProgress Status = "IN_PROGRESS"
	StatusSucceeded  Status = "SUCCEEDED"
	StatusFailed     Status = "FAILED"
	StatusCanceled   Status = "CANCELED"
)

// Terminal reports whether the task will never change again.
func (s Status) Terminal() bool {
	switch s {
	case StatusSucceeded, StatusFailed, StatusCanceled:
		return true
	default:
		return false
	}
}

// ErrNotConfigured is returned when the client is used with no MESHY_API_KEY. Callers should turn
// it into a closed button ("3D generation is not configured"), not into a queued run: a run
// submitted to a provider that cannot be called waits forever.
var ErrNotConfigured = errors.New("meshy: MESHY_API_KEY is not set")

// ErrImageCount is returned when a request carries no images or more than the provider accepts.
// It is checked BEFORE the request leaves, because a rejected submit still costs a round trip and
// the count is something we can be certain about locally.
var ErrImageCount = fmt.Errorf("meshy: multi-image-to-3d takes %d..%d images, the first being the front view", MinImages, MaxImages)

// ErrPromptTooLong is returned when texture_prompt exceeds MaxTexturePrompt. Like ErrImageCount it
// is raised BEFORE the request leaves: the length is a local fact, and the provider's answer to it
// is a 400 that a retry reproduces exactly.
var ErrPromptTooLong = fmt.Errorf("meshy: texture_prompt is capped at %d characters", MaxTexturePrompt)

// ErrBadRequest is the provider's own 4xx for a request it will not accept (400, 422 and the rest
// that are not a rejected key, a rate limit or an unknown task).
//
// ⚠ WITHOUT IT A 4xx WAS WEATHER. The classifier's default leans retryable on purpose — a reset
// connection really is weather — but that default swallowed every "you sent something wrong"
// answer this provider gives, and burnt the whole attempt cap re-sending it. A refusal that names
// the request is the one failure category a retry provably cannot fix.
var ErrBadRequest = errors.New("meshy: the provider refused the request")

// ErrBadImageURL is returned for a reference that is neither an http(s) url nor a data: uri. The
// provider must be able to FETCH these itself, so a bucket key, a relative path or a file:// url is
// a mistake worth catching here rather than as a provider-side failure minutes later.
var ErrBadImageURL = errors.New("meshy: image references must be public http(s) urls or data: uris")

// ErrNotReady is returned by Collect when the task is still PENDING or IN_PROGRESS. It is the
// signal a polling caller loops on — and, for the worker, the signal to put the run back on the
// queue with a later next_attempt_at rather than to submit (and pay for) anything again.
var ErrNotReady = errors.New("meshy: the task has not finished yet")

// ErrTaskFailed is returned when the provider itself ends the task as FAILED or CANCELED. It is
// terminal: nothing about it improves on a retry. Meshy refunds the credits of a failed task.
var ErrTaskFailed = errors.New("meshy: the provider failed the task")

// ErrTimedOut is returned by Await when the poll ceiling passes with the task still running. It is
// NOT the same as "the task failed" — the task is very likely still alive at the provider, the id
// is in the error, and a later Collect can still fetch the result while the links live.
var ErrTimedOut = errors.New("meshy: the task did not finish within the poll ceiling")

// ErrNoGLB is returned when a SUCCEEDED task carries no glb url. We ask for exactly one format, so
// its absence is a broken answer rather than a format we could fall back on: an fbx is not
// something the band can show.
var ErrNoGLB = errors.New("meshy: the finished task carries no glb url")

// ErrTaskNotFound is returned when the provider answers 404 for a task id we hold. It is terminal
// and it is a fact worth distinguishing: it means the id in our attempt row buys nothing, so the
// only way forward is a new (paid) submit — a decision for the worker, not for this client.
var ErrTaskNotFound = errors.New("meshy: the provider does not know this task")

// ErrUnauthorized is returned on 401/403. Like a retired model slug, it is a CONFIGURATION fault
// wearing the clothes of a transient one: retrying an invalid key produces the same answer forever
// while telling the operator it is weather.
var ErrUnauthorized = errors.New("meshy: the API key was rejected")

// ErrOutOfCredit is returned on 402: the key is good, the request is good, and the account has no
// balance left to build a model with.
//
// IT IS ITS OWN SENTINEL BECAUSE IT IS ITS OWN INSTRUCTION. A rejected key is fixed by an operator
// with a new key, a bad request is fixed by the caller, and an empty account is fixed by nobody in
// this process — but all three are equally terminal, and only a named one can say so. Untold, an
// empty balance is a plain error, and a plain error from a provider is weather: five paid-looking
// attempts against a till with nothing in it, then a history row blaming the provider's
// availability for the owner's own unpaid invoice.
//
// ⚠ IT IS RECOGNISED BY THE STATUS CODE ALONE. If Meshy ever starts signalling an exhausted
// balance some other way, this is the place to teach it — never providerMessage, which is display
// only, so that a reworded sentence cannot change what an error means.
var ErrOutOfCredit = errors.New("meshy: the account has no credits left")

// ErrRateLimited is returned on 429, so a caller can back off instead of hammering. Submits are
// not retried here in any case (see doc.go).
var ErrRateLimited = errors.New("meshy: rate limited by the provider")

// ErrUnexpectedResponse is returned when the provider answers 2xx with something this client
// cannot read — no task id, no status. It is loud on purpose: the quiet alternative is to treat an
// unparsed answer as an empty one and call a running task pending forever.
var ErrUnexpectedResponse = errors.New("meshy: unreadable response from the provider")

// ErrTooLarge is returned when an artifact exceeds its cap. Refusal, not truncation: see
// maxModelBytes.
var ErrTooLarge = errors.New("meshy: artifact is larger than the allowed maximum")

// Config is the client configuration. Bound in config/cfg.go — EVERY field below has its own
// explicit viper.BindEnv line and a test, because viper.AutomaticEnv is off in this repo and an
// unbound variable reads as empty without a word of complaint.
type Config struct {
	APIKey          string        `mapstructure:"api_key"`          // MESHY_API_KEY; empty = disabled
	BaseURL         string        `mapstructure:"base_url"`         // MESHY_BASE_URL; empty = defaultBaseURL
	HTTPTimeout     time.Duration `mapstructure:"http_timeout"`     // MESHY_HTTP_TIMEOUT; <=0 = defaultHTTPTimeout
	PollInterval    time.Duration `mapstructure:"poll_interval"`    // MESHY_POLL_INTERVAL; <=0 = defaultPollInterval
	PollTimeout     time.Duration `mapstructure:"poll_timeout"`     // MESHY_POLL_TIMEOUT; <=0 = defaultPollTimeout
	DownloadTimeout time.Duration `mapstructure:"download_timeout"` // MESHY_DOWNLOAD_TIMEOUT; <=0 = defaultDownloadTimeout
	CreditUSD       float64       `mapstructure:"credit_usd"`       // MESHY_CREDIT_USD; <=0 = defaultCreditUSD
}

// String renders the config with the API key redacted, so an accidental %v / %+v / %s of it — in a
// log line, an error, or a test print — cannot leak the key. fmt routes all three through Stringer.
// The same guard the bucket config carries, for the same reason.
func (c Config) String() string {
	key := ""
	if strings.TrimSpace(c.APIKey) != "" {
		// Empty stays empty: whether the provider is configured at all is diagnostic, and hiding
		// that would turn a redaction into a second mystery.
		key = "***REDACTED***"
	}
	return fmt.Sprintf("meshy.Config{APIKey:%s BaseURL:%s HTTPTimeout:%s PollInterval:%s "+
		"PollTimeout:%s DownloadTimeout:%s CreditUSD:%v}",
		key, c.BaseURL, c.HTTPTimeout, c.PollInterval, c.PollTimeout, c.DownloadTimeout, c.CreditUSD)
}

// Client is a configured Meshy client. A nil *Client is valid and permanently disabled, so callers
// need not nil-check before asking Enabled().
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
	if cfg.CreditUSD <= 0 {
		cfg.CreditUSD = defaultCreditUSD
	}
	return &Client{
		// The shared http.Client carries NO Timeout of its own. Every request below gets its
		// deadline from its own context, and the two budgets differ by an order of magnitude: a
		// status lookup may not take half a minute, a download may take five. One client-wide
		// Timeout would have to be the larger of the two, which would leave a hung lookup holding a
		// poll slot for minutes.
		cfg:  cfg,
		http: &http.Client{},
		log:  slog.Default(),
	}
}

// Enabled reports whether an API key is configured. Nil-safe.
func (c *Client) Enabled() bool {
	return c != nil && c.cfg.APIKey != ""
}

// PollInterval and PollTimeout expose the effective waiting shape, so a worker can size its own
// lease and its own next_attempt_at against the same numbers rather than a guess.
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

// CostUSD converts consumed credits into money at the configured rate (MESHY_CREDIT_USD). It is
// the only place that knows the conversion, so the price written into an attempt row and the price
// shown on a button cannot drift apart.
func (c *Client) CostUSD(credits int) decimal.Decimal {
	if c == nil || credits <= 0 {
		return decimal.Zero
	}
	return decimal.NewFromFloat(c.cfg.CreditUSD).Mul(decimal.NewFromInt(int64(credits)))
}

// Request is one multi-image-to-3d job.
type Request struct {
	// ImageURLs are the reference views, 1..4 of them, publicly fetchable BY THE PROVIDER (http(s)
	// or a data: uri). ORDER IS MEANING: the first is the front view.
	ImageURLs []string
	// TexturePrompt optionally steers the texture ("matte black technical nylon, no logos").
	// Optional; the provider caps it at 600 characters.
	TexturePrompt string
	// TargetPolycount optionally overrides the provider's default mesh density. Zero means "the
	// provider's default", which is the right answer for a garment shown in a browser.
	TargetPolycount int
	// AIModel optionally names the provider's generation model. Empty — the normal state — means
	// the provider's own current default, deliberately: see doc.go on baked-in slugs.
	AIModel string
}

// Sink is where the bytes of a finished task go. Model is required. Thumbnail is optional and, if
// given, receives the provider's preview image — the tile the band shows for a kind='threed'
// picture, since a GLB is not something a list view can render.
//
// Both are io.Writer rather than []byte returns on purpose: this backend runs on a 0.5 GB
// instance, and a model belongs in the bucket, not in the heap.
//
// ON ERROR A SINK MAY HOLD A PARTIAL WRITE. A transfer that dies halfway has already handed over
// what it received, and this package cannot un-write somebody else's writer. A caller that streams
// straight into a bucket object must therefore delete that object when Collect returns an error —
// the same orphan compensation the media upload path already performs — or a half a GLB will sit
// there looking like a model.
type Sink struct {
	Model     io.Writer
	Thumbnail io.Writer
}

// Result describes ONE delivered model. It carries no url of any kind, and that omission is the
// point of the package: the provider's links expire in three days, so the only honest thing to
// hand back from a finished task is the bytes (already written to the Sink) and what they cost.
// TestResultCarriesNoExpiringURL enforces the omission.
type Result struct {
	// TaskID is the provider's id for the job. It is durable and free to look up again; it is also
	// the one thing worth storing, which is why it is here and the links are not.
	TaskID string
	// Format is always formatGLB. It is stated rather than assumed so a caller writing a media row
	// does not have to hardcode the extension in a second place.
	Format string
	// ModelBytes and ThumbnailBytes are what was actually written to the Sink. ThumbnailBytes is
	// zero when no thumbnail was requested, offered, or successfully fetched.
	ModelBytes     int64
	ThumbnailBytes int64
	// ModelSHA256 is the hex digest of the model bytes, computed while streaming, for the content
	// hash of the stored object.
	ModelSHA256 string
	// ConsumedCredits is what the provider says the task cost. Zero means the provider did not say.
	ConsumedCredits int
}

// Submit creates a task and returns its provider id. It does not wait, and it does not retry: a
// second submit is a second charge (see doc.go).
func (c *Client) Submit(ctx context.Context, req Request) (string, error) {
	if !c.Enabled() {
		return "", ErrNotConfigured
	}
	if len(req.ImageURLs) < MinImages || len(req.ImageURLs) > MaxImages {
		return "", fmt.Errorf("%w (got %d)", ErrImageCount, len(req.ImageURLs))
	}
	// THE LENGTH IS MEASURED IN CHARACTERS, NOT BYTES. The provider counts what a person typed;
	// counting utf-8 bytes here would refuse a perfectly legal Cyrillic prompt at half the cap.
	if prompt := strings.TrimSpace(req.TexturePrompt); len([]rune(prompt)) > MaxTexturePrompt {
		return "", fmt.Errorf("%w (got %d)", ErrPromptTooLong, len([]rune(prompt)))
	}
	images := make([]string, 0, len(req.ImageURLs))
	for i, raw := range req.ImageURLs {
		u := strings.TrimSpace(raw)
		if err := validateImageRef(u); err != nil {
			return "", fmt.Errorf("image %d: %w", i, err)
		}
		images = append(images, u)
	}

	body := submitBody{
		ImageURLs: images,
		// Exactly one format, always. The band shows GLB and only GLB.
		TargetFormats: []string{formatGLB},
		// A flat drawing becomes a garment only with its colour and print on it; an untextured
		// mesh would answer a different question than the one the designer asked.
		ShouldTexture: true,
		// PBR maps quadruple the download for lighting nuance a product tile does not show.
		EnablePBR:       false,
		TexturePrompt:   strings.TrimSpace(req.TexturePrompt),
		TargetPolycount: req.TargetPolycount,
		AIModel:         strings.TrimSpace(req.AIModel),
	}

	var out struct {
		Result string `json:"result"`
	}
	if err := c.callJSON(ctx, http.MethodPost, multiImagePath, body, &out); err != nil {
		return "", err
	}
	id := strings.TrimSpace(out.Result)
	if id == "" {
		return "", fmt.Errorf("%w: submit returned no task id", ErrUnexpectedResponse)
	}
	return id, nil
}

// Collect performs ONE status lookup and, when the task has succeeded, downloads the artifacts
// into dst BEFORE returning. It is the whole answer to the expiring-link trap: there is no moment
// between "we learned the url" and "we have the bytes" in which a caller could store the url.
//
// It returns ErrNotReady while the task is still running, ErrTaskFailed when the provider ended it,
// and a *Result only when the bytes are already in dst.
func (c *Client) Collect(ctx context.Context, taskID string, dst Sink) (*Result, error) {
	// Both budgets come from the caller here. Await is the one that separates them.
	return c.collect(ctx, ctx, taskID, dst)
}

// collect splits the two budgets: lookupCtx bounds the status request (and is therefore what a
// poll ceiling constrains), fetchCtx bounds the download. They are the same context for a direct
// Collect and deliberately different inside Await.
func (c *Client) collect(lookupCtx, fetchCtx context.Context, taskID string, dst Sink) (*Result, error) {
	if !c.Enabled() {
		return nil, ErrNotConfigured
	}
	if strings.TrimSpace(taskID) == "" {
		return nil, fmt.Errorf("%w: empty task id", ErrUnexpectedResponse)
	}
	if dst.Model == nil {
		return nil, errors.New("meshy: Sink.Model is required — there is nowhere to put the model")
	}

	var t task
	if err := c.callJSON(lookupCtx, http.MethodGet, multiImagePath+"/"+url.PathEscape(taskID), nil, &t); err != nil {
		return nil, err
	}
	status := Status(strings.ToUpper(strings.TrimSpace(t.Status)))
	switch status {
	case "":
		return nil, fmt.Errorf("%w: task %s came back with no status", ErrUnexpectedResponse, taskID)
	case StatusPending, StatusInProgress:
		return nil, fmt.Errorf("%w: task %s is %s (%d%%)", ErrNotReady, taskID, status, t.Progress)
	case StatusFailed, StatusCanceled:
		msg := strings.TrimSpace(t.TaskError.Message)
		if msg == "" {
			msg = "no reason given"
		}
		return nil, fmt.Errorf("%w: task %s is %s: %s", ErrTaskFailed, taskID, status, msg)
	case StatusSucceeded:
		// fall through
	default:
		return nil, fmt.Errorf("%w: task %s has unknown status %q", ErrUnexpectedResponse, taskID, status)
	}

	modelURL := strings.TrimSpace(t.ModelURLs.GLB)
	if modelURL == "" {
		return nil, fmt.Errorf("%w: task %s", ErrNoGLB, taskID)
	}

	res := &Result{TaskID: taskID, Format: formatGLB, ConsumedCredits: t.ConsumedCredits}

	// The paid artifact first, and immediately. Everything above this line is a lookup; everything
	// below is the reason the lookup happened.
	n, sum, err := c.fetch(fetchCtx, modelURL, dst.Model, maxModelBytes)
	if err != nil {
		return nil, fmt.Errorf("meshy: downloading the model of task %s: %w", taskID, err)
	}
	res.ModelBytes, res.ModelSHA256 = n, sum

	// The thumbnail is a courtesy: it makes a tile, it is not what was paid for. Losing it must not
	// lose the run, so its failure is logged and the model still comes back.
	if thumb := strings.TrimSpace(t.ThumbnailURL); thumb != "" && dst.Thumbnail != nil {
		tn, _, terr := c.fetch(fetchCtx, thumb, dst.Thumbnail, maxThumbnailBytes)
		if terr != nil {
			c.log.WarnContext(fetchCtx, "meshy: thumbnail of a delivered model could not be fetched",
				slog.String("task_id", taskID), slog.String("err", terr.Error()))
		} else {
			res.ThumbnailBytes = tn
		}
	}
	return res, nil
}

// Await polls a submitted task until it finishes and returns its Result with the bytes already in
// dst. It is cancellable through ctx and bounded by PollTimeout.
//
// THE CEILING BOUNDS THE WAIT, NEVER THE FETCH. The lookups run under a derived context that
// expires at the ceiling; the download runs under the caller's context with a budget of its own.
// A single ceiling over both would mean that a task finishing in the last second of the wait gets
// its download cut — the credits spent, the model built, and nothing to show but a link that dies
// in three days. That is the most expensive thing that can happen in this package, and it is
// avoided here, in these two lines.
func (c *Client) Await(ctx context.Context, taskID string, dst Sink) (*Result, error) {
	if !c.Enabled() {
		return nil, ErrNotConfigured
	}
	ceiling := c.cfg.PollTimeout
	waitCtx, cancel := context.WithTimeout(ctx, ceiling)
	defer cancel()

	timer := time.NewTimer(c.cfg.PollInterval)
	defer timer.Stop()

	// A 404 IN THE FIRST SECONDS IS A LAG, NOT AN ANSWER — see notFoundGrace. The grace never eats
	// more than half the ceiling, so a task that really is unknown still gets its terminal verdict
	// inside the wait rather than surfacing as a timeout, which points a worker the other way.
	grace := notFoundGrace
	if half := ceiling / 2; grace > half {
		grace = half
	}
	started := time.Now()

	for {
		res, err := c.collect(waitCtx, ctx, taskID, dst)
		if err == nil {
			return res, nil
		}
		// THE FIRST LOOKUP CAN LAND BEFORE THE PROVIDER HAS FINISHED KNOWING ABOUT THE SUBMIT.
		// Generate polls immediately after the create call returns, and ErrTaskNotFound is
		// TERMINAL: taken at face value there, a read-after-write lag of a second would close a
		// PAID task within seconds of buying it, and the only way forward from a discarded id is
		// buying another one. Inside the grace a 404 is therefore treated as "not yet" — the same
		// road ErrNotReady takes — and after it, it means what it says.
		if errors.Is(err, ErrTaskNotFound) && time.Since(started) < grace {
			err = fmt.Errorf("%w: task %s is not visible to the provider yet", ErrNotReady, taskID)
		}
		if !errors.Is(err, ErrNotReady) {
			// A LOOKUP killed by the ceiling must read as a ceiling, not as a transport hiccup —
			// the task is very probably still alive and the id is still worth something.
			//
			// A DOWNLOAD that failed on its own terms must NOT be relabelled that way, even though
			// the ceiling has by then usually passed (it is shorter than a download budget by
			// design). "The wait ran out, look again later" and "the artifact would not come down"
			// point a worker in opposite directions, and only the second one is true here. So the
			// ceiling is claimed only for errors that ARE the deadline.
			if waitCtx.Err() != nil && (errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled)) {
				return nil, waitErr(ctx, taskID, ceiling)
			}
			return nil, err
		}
		select {
		case <-waitCtx.Done():
			return nil, waitErr(ctx, taskID, ceiling)
		case <-timer.C:
			timer.Reset(c.cfg.PollInterval)
		}
	}
}

// Generate is the whole cycle: submit, wait, download. It returns the task id inside the Result,
// and — on any failure after a successful submit — inside the error, because that id is the only
// thing that can find a paid task again.
func (c *Client) Generate(ctx context.Context, req Request, dst Sink) (*Result, error) {
	id, err := c.Submit(ctx, req)
	if err != nil {
		return nil, err
	}
	res, err := c.Await(ctx, id, dst)
	if err != nil {
		return nil, fmt.Errorf("meshy: task %s: %w", id, err)
	}
	return res, nil
}

// waitErr tells the two ways of running out of time apart. The caller's own cancellation is the
// caller's business; the ceiling is ours, and it is not a failure of the task.
func waitErr(parent context.Context, taskID string, ceiling time.Duration) error {
	if err := parent.Err(); err != nil {
		return fmt.Errorf("meshy: waiting for task %s: %w", taskID, err)
	}
	return fmt.Errorf("%w: task %s, waited %s", ErrTimedOut, taskID, ceiling)
}

// submitBody is the create-task payload. Field names are the provider's.
type submitBody struct {
	ImageURLs       []string `json:"image_urls"`
	TargetFormats   []string `json:"target_formats"`
	ShouldTexture   bool     `json:"should_texture"`
	EnablePBR       bool     `json:"enable_pbr"`
	TexturePrompt   string   `json:"texture_prompt,omitempty"`
	TargetPolycount int      `json:"target_polycount,omitempty"`
	AIModel         string   `json:"ai_model,omitempty"`
}

// task is the retrieve-task payload, narrowed to what this client uses. The url fields are read
// into it and never leave the package: nothing that crosses the package boundary carries them.
type task struct {
	ID        string `json:"id"`
	Status    string `json:"status"`
	Progress  int    `json:"progress"`
	ModelURLs struct {
		GLB string `json:"glb"`
	} `json:"model_urls"`
	ThumbnailURL string `json:"thumbnail_url"`
	TaskError    struct {
		Message string `json:"message"`
	} `json:"task_error"`
	ConsumedCredits int `json:"consumed_credits"`
}

// callJSON performs one control-plane request against the Meshy API and decodes its JSON answer.
func (c *Client) callJSON(ctx context.Context, method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		raw, err := json.Marshal(in)
		if err != nil {
			return fmt.Errorf("meshy: encoding request: %w", err)
		}
		body = bytes.NewReader(raw)
	}

	reqCtx, cancel := context.WithTimeout(ctx, c.cfg.HTTPTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, method, c.cfg.BaseURL+path, body)
	if err != nil {
		return fmt.Errorf("meshy: building request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.cfg.APIKey)
	req.Header.Set("Accept", "application/json")
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("meshy: %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusAccepted {
		return c.statusError(resp, method, path)
	}

	raw, err := readCapped(resp.Body, maxAPIResponseBytes)
	if err != nil {
		return fmt.Errorf("meshy: reading %s %s: %w", method, path, err)
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("%w: %s %s: %v", ErrUnexpectedResponse, method, path, err)
	}
	return nil
}

// statusError turns a non-2xx answer into the sentinel that says what to DO about it: a rejected
// key is a setting to fix, a 429 is a reason to wait, a 404 on a lookup means the id is worthless,
// and everything else is weather with a status code attached.
func (c *Client) statusError(resp *http.Response, method, path string) error {
	raw, _ := readCapped(resp.Body, maxErrorBodyBytes)
	detail := providerMessage(raw)

	switch resp.StatusCode {
	case http.StatusUnauthorized, http.StatusForbidden:
		return fmt.Errorf("%w (HTTP %d): %s", ErrUnauthorized, resp.StatusCode, detail)
	case http.StatusPaymentRequired:
		// 402 IS A DRAINED BALANCE, AND IT MUST NOT SHARE A ROAD WITH «BAD REQUEST» OR WITH
		// WEATHER. Every other provider in this feature already names it — orimages.ErrOutOfCredit,
		// recraft.ErrInsufficientCredits — and the classifier turns those into a terminal
		// provider_out_of_credit. Meshy had no such sentinel, so an empty account arrived as a
		// generic error, fell into the classifier's retryable default and spent the entire attempt
		// cap knocking on a till with nothing in it, while the history row said the provider was
		// unavailable. Nothing about an empty balance improves on a retry, and the operator needs
		// to read the word «credit», not the word «unavailable».
		return fmt.Errorf("%w (HTTP %d): %s", ErrOutOfCredit, resp.StatusCode, detail)
	case http.StatusTooManyRequests:
		return fmt.Errorf("%w (HTTP %d): %s", ErrRateLimited, resp.StatusCode, detail)
	case http.StatusNotFound:
		if method == http.MethodGet {
			return fmt.Errorf("%w (HTTP 404): %s", ErrTaskNotFound, detail)
		}
	}
	// EVERY OTHER 4xx IS «WE SENT SOMETHING WRONG», AND THAT IS NOT WEATHER. Leaving it to the
	// generic sentence below meant the caller's classifier read it as a transient fault and spent
	// the whole attempt cap re-sending a request the provider had already judged. 5xx keeps the
	// generic form: a server that is failing today may well answer tomorrow.
	if resp.StatusCode >= 400 && resp.StatusCode < 500 {
		return fmt.Errorf("%w: %s %s: HTTP %d: %s", ErrBadRequest, method, path, resp.StatusCode, detail)
	}
	return fmt.Errorf("meshy: %s %s: HTTP %d: %s", method, path, resp.StatusCode, detail)
}

// providerMessage extracts the provider's own sentence from an error body, falling back to the raw
// body. It is used for DISPLAY only — never to classify a fault, so that a reworded provider
// message cannot silently change how an error is handled.
func providerMessage(raw []byte) string {
	var e struct {
		Message string `json:"message"`
		Error   string `json:"error"`
	}
	if err := json.Unmarshal(raw, &e); err == nil {
		if m := strings.TrimSpace(e.Message); m != "" {
			return m
		}
		if m := strings.TrimSpace(e.Error); m != "" {
			return m
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
// IT SENDS NO AUTHORIZATION HEADER. The url comes out of the provider's JSON and points at
// whatever host the provider names; attaching our API key to a request at an address we did not
// choose would hand the key to that host. Meshy's artifact links are pre-signed and need no key.
func (c *Client) fetch(ctx context.Context, rawURL string, dst io.Writer, limit int64) (int64, string, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return 0, "", fmt.Errorf("meshy: unparsable artifact url: %w", err)
	}
	if u.Scheme != "https" && u.Scheme != "http" {
		return 0, "", fmt.Errorf("meshy: refusing artifact url with scheme %q", u.Scheme)
	}

	fetchCtx, cancel := context.WithTimeout(ctx, c.cfg.DownloadTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(fetchCtx, http.MethodGet, u.String(), nil)
	if err != nil {
		return 0, "", fmt.Errorf("meshy: building artifact request: %w", err)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return 0, "", fmt.Errorf("meshy: fetching artifact: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := readCapped(resp.Body, maxErrorBodyBytes)
		return 0, "", fmt.Errorf("meshy: fetching artifact: HTTP %d: %s", resp.StatusCode, providerMessage(raw))
	}

	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(dst, h), newCapReader(resp.Body, limit))
	if err != nil {
		return n, "", err
	}
	return n, hex.EncodeToString(h.Sum(nil)), nil
}

// readCapped reads at most limit bytes and REFUSES if there are more, instead of silently handing
// back a prefix. A JSON body cut at the boundary fails to parse and blames the provider; a model
// cut at the boundary is a file that opens in nothing.
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

// capReader is the streaming half of the same rule: it fails once more than limit bytes have
// passed through it. io.LimitReader would report a clean EOF at the boundary, and a truncated GLB
// that arrived "successfully" is indistinguishable from a corrupt one for as long as it takes
// somebody to compare byte counts.
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
