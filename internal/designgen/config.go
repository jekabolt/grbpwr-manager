package designgen

import (
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/shopspring/decimal"
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
	// ImageRunCap — the WALL-CLOCK cap of an image.generate run from its first start
	// (entity.DesignImageRunCapDefault, owner 05.10): the worker bounds its own provider phase by it
	// and its overdue sweep closes the row as `timed_out` past it, so a person never waits on a
	// spinner for the 20-minute lease.
	ImageRunCap time.Duration `mapstructure:"image_run_cap"`
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
	// topstitching, a zip tape, a bar-tack. It is also the picture a person traces into the
	// stroke editor, and nobody can recover a stitch the raster never resolved. A render is looked at; a
	// flat is read.
	//
	// THE MONEY WAS ALREADY COVERED, WHICH IS WHY THIS IS SAFE. designPriceEstimate reserves every
	// image kind at the CEILING of this dial (designImageQualityCeiling) rather than at the
	// configured position, precisely so that moving the dial cannot make the daily budget
	// under-count. Raising the flat to the ceiling spends inside a reservation that was already
	// being held for it. What it does change is the real bill, so the knob stays a knob.
	ImageQualityFlat string `mapstructure:"image_quality_flat"`
	// ThreedPBR lets a 3D run ask for realistic materials (params.threed.pbr = on). OFF BY DEFAULT
	// (DESIGN_THREED_PBR), and that is a money decision, not a taste: a PBR build carries extra maps,
	// its GLB size on fal meshy/v7 (standard and detailed geometry) is UNMEASURED, and the transport
	// refuses a model over 64 MiB AFTER the build has been charged (fal.maxModelBytes). Until a beta
	// smoke measures one PBR build of each tier under the cap, the band does not advertise `pbr` and
	// the door refuses pbr=on with option_not_read, for free (G-02, Fable M-3).
	ThreedPBR bool `mapstructure:"threed_pbr"`
	// EngineGemini / EngineSeedream list the phase-3 engine rows (B-16) — Gemini 3 Pro Image and
	// Seedream 5 Pro — in EngineTable: the band advertises them, the door accepts and prices them.
	// OFF BY DEFAULT (DESIGN_ENGINE_GEMINI, DESIGN_ENGINE_SEEDREAM), and that is the owner's money
	// decision, not a taste: a Gemini 4K picture is ≈ $0.24 against GPT Image's ceilings, and a flag
	// goes on only after the beta cost ledger (one call per tier, usage.cost against the ceilings).
	EngineGemini   bool `mapstructure:"engine_gemini"`
	EngineSeedream bool `mapstructure:"engine_seedream"`
	// Engines is the engine table the worker resolves a frozen params.image with and refuses a
	// switched-off engine by (dispatch.go) — THE SAME FUNCTION the door prices against (app.go hands
	// EngineTableFunc to both, B-13), read at every pickup, so a default moved in the panel reaches the
	// worker and the door together. nil (a worker built without app.go: tests) = EngineTable over
	// orimages.DefaultModel and this Config's EngineFlags.
	Engines func() []Engine `mapstructure:"-"`
	// FalRouteModel is the route row's slug of an extend / inpaint / cutout run (B-24: app.go hands
	// FalRouteModel over the live registry — the expression the door's FalRoutesFunc reads); "" = the
	// env slug. Nil = the env slug always (the tests, a worker built without app.go).
	FalRouteModel func(kind string) string `mapstructure:"-"`
	// VideoCeilingUSD is RUNBLOB_VIDEO_CEILING_USD (B-32): the most one video clip reserves against
	// the day — read through VideoCeiling(), which supplies DefaultVideoCeilingUSD for an empty or
	// unparseable value. A string, not a float: it is money, and the door and the ledger read it as a
	// decimal.
	VideoCeilingUSD string `mapstructure:"video_ceiling_usd"`
}

// Environment variable names. AutomaticEnv is switched off in this repo, so a name that is not
// read explicitly is silently empty — which is also what a correctly-unset override looks like.
const (
	EnvEnabled          = "DESIGN_GENERATION_ENABLED"
	EnvInterval         = "DESIGN_WORKER_INTERVAL"
	EnvBatchSize        = "DESIGN_WORKER_BATCH_SIZE"
	EnvClaimLease       = "DESIGN_WORKER_CLAIM_LEASE"
	EnvRunTimeout       = "DESIGN_WORKER_RUN_TIMEOUT"
	EnvImageRunCap      = "DESIGN_IMAGE_RUN_CAP"
	EnvImageQuality     = "DESIGN_IMAGE_QUALITY"
	EnvImageQualityFlat = "DESIGN_IMAGE_QUALITY_FLAT"
	EnvThreedPBR        = "DESIGN_THREED_PBR"
	EnvEngineGemini     = "DESIGN_ENGINE_GEMINI"
	EnvEngineSeedream   = "DESIGN_ENGINE_SEEDREAM"
	// EnvVideoCeilingUSD — RUNBLOB_VIDEO_CEILING_USD, the video route's reserve (Config.VideoCeilingUSD).
	EnvVideoCeilingUSD = "RUNBLOB_VIDEO_CEILING_USD"
)

// DefaultVideoCeilingUSD — what one clip reserves when RUNBLOB_VIDEO_CEILING_USD is unset or
// unparseable: above Kling 2.5 Turbo's documented $0.29 for 5 s with room for the dearer kling_*
// slugs a route row may name (the pro tiers, Kling 3), below a price nobody would call a clip.
const DefaultVideoCeilingUSD = "1.50"

// VideoCeiling — the most one video clip may BOOK on this deployment, as a number: the configured
// RUNBLOB_VIDEO_CEILING_USD, else DefaultVideoCeilingUSD. It is the reserve the door holds for one
// clip (designPriceEstimate) and the line the submit compares runblob's price with — a breach is
// LOGGED, never refused, because after the 201 the money has moved (fal's booked-vs-reserved rule).
// Zero, negative or unparseable = the default: a typo in a money knob must not close the door
// silently, and it must not open it to an unbounded reserve either.
func (c Config) VideoCeiling() decimal.Decimal {
	if v := strings.TrimSpace(c.VideoCeilingUSD); v != "" {
		if d, err := decimal.NewFromString(v); err == nil && d.IsPositive() {
			return d
		}
	}
	return decimal.RequireFromString(DefaultVideoCeilingUSD)
}

// EngineFlags — the flagged engine rows this configuration lists (EngineTable's second argument).
func (c Config) EngineFlags() EngineFlags {
	return EngineFlags{Gemini: c.EngineGemini, Seedream: c.EngineSeedream}
}

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
		ImageRunCap:      entity.DesignImageRunCapDefault,
		ImageQuality:     "medium",
		ImageQualityFlat: ImageQualityMax,
	}
}

// Normalize applies this package's own defaults and ceilings to a configuration IN PLACE.
//
// ⚠ IT EXISTS SO NOBODY READS A FIELD BEFORE IT MEANS ANYTHING: app.go reads the config (the image
// quality, the PBR flag) before constructing the worker. New() normalises as a matter of course; a
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
	if c.ImageRunCap <= 0 {
		c.ImageRunCap = d.ImageRunCap
	}
	if strings.TrimSpace(c.ImageQuality) == "" {
		c.ImageQuality = d.ImageQuality
	}
	if strings.TrimSpace(c.ImageQualityFlat) == "" {
		c.ImageQualityFlat = d.ImageQualityFlat
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
	//     operator sized deliberately (a 3D build alone polls for minutes before it has anything),
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
	c.ThreedPBR = envBool(EnvThreedPBR, c.ThreedPBR)
	c.EngineGemini = envBool(EnvEngineGemini, c.EngineGemini)
	c.EngineSeedream = envBool(EnvEngineSeedream, c.EngineSeedream)
	if v := strings.TrimSpace(os.Getenv(EnvVideoCeilingUSD)); v != "" {
		c.VideoCeilingUSD = v
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
