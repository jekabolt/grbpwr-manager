package designgen

import (
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
)

// Config is the worker's configuration.
//
// The mapstructure tags mount this section on config.Config as `design_generation`, which is where
// the binary reads it from; ConfigFromEnv below is a leftover of the days before that field existed.
type Config struct {
	// Enabled gates the whole feature. Off means the worker is NOT CONSTRUCTED and NOT REGISTERED
	// — not "constructed and idle" — so a deployment that has not been given provider keys runs
	// exactly the code it ran before this package existed.
	Enabled bool `mapstructure:"enabled"`
	// WorkerInterval is how often the queue is looked at. Generation is a human-initiated,
	// low-rate action: a few seconds of latency on a job that takes half a minute is invisible,
	// while a tight loop is a pointless query every tick on an idle table.
	WorkerInterval time.Duration `mapstructure:"worker_interval"`
	// BatchSize is how many runs one tick claims. It is SMALL on purpose, and applyDefaults caps it
	// at what ONE lease can carry: the whole batch is leased at the instant of the claim, and the
	// worker then executes it one run at a time, so the last row of a batch of N waits out N-1
	// predecessors with its own lease already running. See applyDefaults for why the batch is the
	// number that gives.
	//
	// It buys no parallelism — runOnce is strictly sequential — only one queue round trip per N
	// runs instead of N. Raising throughput is done by lengthening ClaimLease (which lets the cap
	// rise), never by naming a batch the lease cannot cover.
	BatchSize int `mapstructure:"batch_size"`
	// ClaimLease is how long a claim survives without being renewed. THE ONE INVARIANT THAT
	// MATTERS: it must exceed the longest possible provider call — TIMES THE BATCH, because
	// ClaimRuns stamps one expiry on every claimed row and there is no verb that renews it. If it
	// does not, the queue revives a run whose worker is still alive and paying — and then two
	// workers hold what each believes is the same job, exactly the way the store's
	// ReviveExpiredRuns comment describes.
	//
	// It is also the delay before a run whose worker really died comes back, and the time its
	// budget reservation stays held, so it is not free to stretch.
	ClaimLease time.Duration `mapstructure:"claim_lease"`
	// RunTimeout bounds ONE pass over ONE run, provider call included. applyDefaults keeps
	// BatchSize × RunTimeout under ClaimLease, which is what makes the invariant above true by
	// construction rather than by an operator getting three numbers right.
	RunTimeout time.Duration `mapstructure:"run_timeout"`
	// ImageQuality is the image provider's price dial ("auto" | "low" | "medium" | "high"). It is
	// configuration rather than a constant because it is the single largest multiplier on what a
	// press costs — roughly four times between medium and high on gpt-image-1 — and moving it must
	// not require a deploy.
	//
	// ⚠ IT MUST AGREE WITH WHAT THE HANDLER RESERVED. The reservation is a static estimate taken
	// before the call; raising this dial without raising that estimate makes the daily budget
	// under-count real spend, silently, in the direction that overspends.
	ImageQuality string `mapstructure:"image_quality"`
	// ImageQualityFlat is the SAME dial held separately for the flat route, and it defaults to the
	// TOP of the enum rather than to ImageQuality.
	//
	// ⚠ «MAXIMUM RESOLUTION» IS NOT A PARAMETER THIS ENDPOINT HAS, and pretending otherwise is the
	// mistake this comment exists to prevent. The images endpoint takes `aspect_ratio` — a RATIO —
	// and has no pixel-size field at all (orimages.Request lists every key the provider accepts,
	// measured against the live API). The only dial that decides how much the model spends on one
	// picture is `quality`, and on GPT Image it is a TOKEN COUNT: `high` buys roughly four times
	// the output tokens of `medium`. So this is the whole of what «draw the flat as large as it
	// can be drawn» can mean here, and it is named for the dial it moves, not for a promise about
	// pixels.
	//
	// WHY THE FLAT AND NOT EVERYTHING. A flat is the drawing the pattern room works from: hairline
	// topstitching, a zip tape, a bar-tack. It is also the picture that gets vectorised afterwards,
	// and a vectoriser cannot recover a stitch the raster never resolved. A render is looked at; a
	// flat is read.
	//
	// THE MONEY WAS ALREADY COVERED, WHICH IS WHY THIS IS SAFE. designPriceEstimate reserves every
	// image kind at the CEILING of this dial (designImageQualityCeiling) rather than at the
	// configured position, precisely so that moving the dial cannot make the daily budget
	// under-count. Raising the flat to the ceiling spends inside a reservation that was already
	// being held for it. What it does change is the real bill, so the knob stays a knob.
	ImageQualityFlat string `mapstructure:"image_quality_flat"`
	// ThreedProvider names WHICH 3D route is wired: fal | meshy.
	//
	// ⚠ IT IS AN EXPLICIT WORD AND NOT «WHICHEVER KEY HAPPENS TO BE SET», AND THAT IS THE WHOLE
	// POINT. This setting decides WHO GETS PAID for a turntable. A rule like «use fal if FAL_KEY is
	// present, otherwise Meshy» would move the owner's money from one vendor to another as a side
	// effect of typing a key into a dashboard, silently, with the history row the only trace. A word
	// somebody wrote down is the only honest way to say a thing like that.
	//
	// THE DEFAULT IS `fal`, BECAUSE THE OWNER NAMED IT: «для 3d как референсы должны использоваться
	// hitem3d/hi3d/v3.0/multi-view-to-3d и нам нужна интеграция с fal.ai». The consequence is
	// deliberate and is the behaviour the requirement asks for — with no FAL_KEY the 3D button
	// refuses IN WORDS, naming the variable, instead of quietly falling back to a provider the owner
	// did not ask for and reporting success. Meshy stays one variable away.
	ThreedProvider string `mapstructure:"threed_provider"`
}

// Environment variable names. AutomaticEnv is switched off in this repo, so a name that is not
// read explicitly is silently empty — which is also what a correctly-unset override looks like.
const (
	EnvEnabled          = "DESIGN_GENERATION_ENABLED"
	EnvInterval         = "DESIGN_WORKER_INTERVAL"
	EnvBatchSize        = "DESIGN_WORKER_BATCH_SIZE"
	EnvClaimLease       = "DESIGN_WORKER_CLAIM_LEASE"
	EnvRunTimeout       = "DESIGN_WORKER_RUN_TIMEOUT"
	EnvImageQuality     = "DESIGN_IMAGE_QUALITY"
	EnvImageQualityFlat = "DESIGN_IMAGE_QUALITY_FLAT"
	EnvThreedProvider   = "DESIGN_THREED_PROVIDER"
)

// ImageQualityMax is the top position of the provider's quality dial — the most this deployment can
// ask a single picture to be worth.
//
// It is a WORD, not a number, because the dial is an enum the provider owns ("auto" | "low" |
// "medium" | "high", the same four on every GPT Image slug as of 2026-08-30 — see
// orimages.Request.Quality). "auto" is not the top: it is the provider deciding, which is the one
// answer a caller asking for the maximum has ruled out.
const ImageQualityMax = "high"

// QualityFor is THE ONE EXPRESSION that answers «what quality does this run ask for».
//
// Two readers of a dial are two numbers, and two numbers disagree the day one of them is edited —
// the defect this whole package's comments keep coming back to. Every caller goes through here;
// nothing anywhere else reads c.ImageQuality or c.ImageQualityFlat.
func (c Config) QualityFor(kind string) string {
	if kind == entity.DesignRunKindFlat {
		if q := strings.TrimSpace(c.ImageQualityFlat); q != "" {
			return q
		}
	}
	return c.ImageQuality
}

// DefaultConfig is the shape of a deployment nobody has tuned.
func DefaultConfig() Config {
	return Config{
		Enabled:        false,
		WorkerInterval: 5 * time.Second,
		// ONE. Not because a tick may only do one thing, but because the default lease pays for
		// exactly one 15-minute pass and a batch of two would put the second run outside it — the
		// double-payment window this config's invariant exists to close. A sequential worker loses
		// nothing by it: two runs one tick apart and two runs in one tick take the same time, minus
		// one WorkerInterval. This is a fixed point of applyDefaults.
		BatchSize:        1,
		ClaimLease:       20 * time.Minute,
		RunTimeout:       15 * time.Minute,
		ImageQuality:     "medium",
		ImageQualityFlat: ImageQualityMax,
		ThreedProvider:   ThreedProviderFal,
	}
}

// Normalize applies this package's own defaults and ceilings to a configuration IN PLACE.
//
// ⚠ IT EXISTS SO NOBODY READS A FIELD BEFORE IT MEANS ANYTHING, and that is not hypothetical: the
// first version of app.go compared ThreedProvider against `meshy` BEFORE constructing the worker,
// i.e. before applyDefaults had lower-cased it — so an operator who wrote `MESHY` in the dashboard
// got fal, silently, and the log line said fal too. New() normalises as a matter of course; a
// caller that needs to READ a normalised value before that point calls this. It is idempotent.
func Normalize(c *Config) {
	if c == nil {
		return
	}
	applyDefaults(c)
}

func applyDefaults(c *Config) {
	d := DefaultConfig()
	if c.WorkerInterval <= 0 {
		c.WorkerInterval = d.WorkerInterval
	}
	if c.BatchSize <= 0 || c.BatchSize > 16 {
		c.BatchSize = d.BatchSize
	}
	if c.ClaimLease <= 0 {
		c.ClaimLease = d.ClaimLease
	}
	if c.RunTimeout <= 0 {
		c.RunTimeout = d.RunTimeout
	}
	if strings.TrimSpace(c.ImageQuality) == "" {
		c.ImageQuality = d.ImageQuality
	}
	if strings.TrimSpace(c.ImageQualityFlat) == "" {
		c.ImageQualityFlat = d.ImageQualityFlat
	}
	// AN UNKNOWN WORD FALLS BACK TO THE DEFAULT AND app.go SAYS WHICH ROUTE IT WIRED, at info level,
	// on every boot. Refusing to boot over a typo in a route name would take the whole backend down;
	// silently picking one and never mentioning it is how a deployment comes to pay a vendor nobody
	// chose. The pair — normalise here, announce there — is the cheap version of both.
	switch strings.ToLower(strings.TrimSpace(c.ThreedProvider)) {
	case ThreedProviderFal, ThreedProviderMeshy:
		c.ThreedProvider = strings.ToLower(strings.TrimSpace(c.ThreedProvider))
	default:
		c.ThreedProvider = d.ThreedProvider
	}
	// THE INVARIANT, ENFORCED RATHER THAN DOCUMENTED — AND IT IS ABOUT THE WHOLE BATCH.
	//
	// A LEASE IS GRANTED ONCE, TO EVERY ROW OF THE BATCH, AT THE MOMENT OF THE CLAIM. ClaimRuns
	// stamps claim_expires_at = now + ClaimLease on all N rows and nothing ever renews it; runOnce
	// then walks those rows ONE AT A TIME, each under its own RunTimeout. So run number k of a batch
	// can still be inside a paid provider call k × RunTimeout after the claim, while its lease died
	// at ClaimLease. "RunTimeout < ClaimLease" is therefore an invariant about k = 1 only, and it
	// proves nothing about the rest of the batch.
	//
	// With the numbers this file used to ship (batch 2, run 15m, lease 20m) the SECOND run of every
	// batch executed between minute 15 and minute 30 on a lease that expired at minute 20. A second
	// instance — overlapping containers on a deploy are the ordinary case, not the exception —
	// sweeps that row back to `pending` with ReviveExpiredRuns, claims it and PAYS FOR IT A SECOND
	// TIME, while the first worker comes back with a paid result and loses it as a lost claim. At
	// the old batch ceiling of 16 the tail of a batch could start three and three quarter hours
	// after its lease was already dead.
	//
	// The invariant that is actually needed is BatchSize × RunTimeout ≤ ¾ × ClaimLease, and of the
	// three numbers it is THE BATCH that gives:
	//
	//   - RunTimeout bounds a PAID call. Dividing it by the batch would cut a provider wait the
	//     operator sized deliberately (Meshy alone polls for twelve minutes before it has anything),
	//     which is the money-losing direction.
	//   - ClaimLease is how long a genuinely dead worker's run stays stuck in `running` holding its
	//     budget reservation. Multiplying it by up to the batch ceiling would turn one redeploy into
	//     hours of frozen runs and reserved daily budget.
	//   - BatchSize has NOTHING to lose. The worker is sequential, so a batch of N is not N runs at
	//     once, it is N runs in a row — the same work N successive ticks would do, minus one
	//     WorkerInterval of latency against a call measured in minutes. What a batch does buy is
	//     exposure: every row past the first burns its lease waiting for its predecessors.
	//
	// So the batch is capped at what one lease can carry, and an operator who wants a larger batch
	// gets it by naming a lease that covers it. Worker.Start logs the effective value.
	if c.RunTimeout >= c.ClaimLease {
		c.RunTimeout = c.ClaimLease / 4 * 3
	}
	if c.RunTimeout <= 0 {
		// A lease so short that three quarters of it round to zero is a typo, not a policy, and a
		// zero RunTimeout would make every pass expire before it started. Fall back to the PAIR that
		// is known to hold rather than to one half of it.
		c.ClaimLease, c.RunTimeout = d.ClaimLease, d.RunTimeout
	}
	// Three quarters, as before: the last run of the batch still needs room to write down the
	// result of the call it already paid for, after the last provider byte arrived.
	if maxBatch := int(c.ClaimLease / 4 * 3 / c.RunTimeout); c.BatchSize > maxBatch {
		if maxBatch < 1 {
			// k = 1 is already covered by the clamp above; a batch of zero would drain nothing.
			maxBatch = 1
		}
		c.BatchSize = maxBatch
	}
}

// ConfigFromEnv reads the worker's settings straight from the process environment.
//
// ⚠ THE BRIDGE IS ALREADY CROSSED, AND THIS IS THE PLANK NOBODY PULLED UP. config.Config now
// carries `DesignGen designgen.Config \`mapstructure:"design_generation"\“, bindEnvVars carries the
// six BindEnv lines quoted below, and app.go passes &a.c.DesignGen — so NOTHING IN THE BINARY CALLS
// THIS FUNCTION any more. Do not follow the instructions this comment used to give: adding that
// section a second time is the only way to make the two readers disagree.
//
// It survives because TestConfigFromEnvReadsEveryVariable and TestConfigFromEnvDefaultsToOff in
// worker_test.go still call it. Deleting those two tests and this function is a tidy-up of a dead
// path, not a behaviour change.
//
//	viper.BindEnv("design_generation.enabled", "DESIGN_GENERATION_ENABLED")
//	viper.BindEnv("design_generation.worker_interval", "DESIGN_WORKER_INTERVAL")
//	viper.BindEnv("design_generation.batch_size", "DESIGN_WORKER_BATCH_SIZE")
//	viper.BindEnv("design_generation.claim_lease", "DESIGN_WORKER_CLAIM_LEASE")
//	viper.BindEnv("design_generation.run_timeout", "DESIGN_WORKER_RUN_TIMEOUT")
//	viper.BindEnv("design_generation.image_quality", "DESIGN_IMAGE_QUALITY")
//	viper.BindEnv("design_generation.threed_provider", "DESIGN_THREED_PROVIDER")
//
// Until then the two readers agree on ONE spelling of every variable, which is what the constants
// above are for. Unparseable values fall back to the default rather than refusing to boot: a typo
// in a tuning knob must not take the whole backend down, and the default is always safe.
func ConfigFromEnv() Config {
	c := DefaultConfig()
	c.Enabled = envBool(EnvEnabled, c.Enabled)
	c.WorkerInterval = envDuration(EnvInterval, c.WorkerInterval)
	c.BatchSize = envInt(EnvBatchSize, c.BatchSize)
	c.ClaimLease = envDuration(EnvClaimLease, c.ClaimLease)
	c.RunTimeout = envDuration(EnvRunTimeout, c.RunTimeout)
	if v := strings.TrimSpace(os.Getenv(EnvImageQuality)); v != "" {
		c.ImageQuality = v
	}
	if v := strings.TrimSpace(os.Getenv(EnvImageQualityFlat)); v != "" {
		c.ImageQualityFlat = v
	}
	if v := strings.TrimSpace(os.Getenv(EnvThreedProvider)); v != "" {
		c.ThreedProvider = v
	}
	applyDefaults(&c)
	return c
}

// envBool accepts what viper accepts for a bool: 1/t/T/TRUE/true/True and their negatives.
func envBool(name string, def bool) bool {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return def
	}
	v, err := strconv.ParseBool(raw)
	if err != nil {
		return def
	}
	return v
}

func envInt(name string, def int) int {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return def
	}
	v, err := strconv.Atoi(raw)
	if err != nil {
		return def
	}
	return v
}

func envDuration(name string, def time.Duration) time.Duration {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return def
	}
	v, err := time.ParseDuration(raw)
	if err != nil {
		return def
	}
	return v
}
