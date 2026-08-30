package designgen

import (
	"os"
	"strconv"
	"strings"
	"time"
)

// Config is the worker's configuration.
//
// The mapstructure tags are here so that the section can be mounted on config.Config as
// `design_generation` with a one-line field the day its owner adds it; see ConfigFromEnv below for
// why it is not mounted yet.
type Config struct {
	// Enabled gates the whole feature. Off means the worker is NOT CONSTRUCTED and NOT REGISTERED
	// — not "constructed and idle" — so a deployment that has not been given provider keys runs
	// exactly the code it ran before this package existed.
	Enabled bool `mapstructure:"enabled"`
	// WorkerInterval is how often the queue is looked at. Generation is a human-initiated,
	// low-rate action: a few seconds of latency on a job that takes half a minute is invisible,
	// while a tight loop is a pointless query every tick on an idle table.
	WorkerInterval time.Duration `mapstructure:"worker_interval"`
	// BatchSize is how many runs one tick claims. It is SMALL on purpose: each claimed run
	// occupies this worker for the length of a provider call, and a big batch would mean a long
	// tick holding many leases while doing one thing at a time.
	BatchSize int `mapstructure:"batch_size"`
	// ClaimLease is how long a claim survives without being renewed. THE ONE INVARIANT THAT
	// MATTERS: it must exceed the longest possible provider call. If it does not, the queue
	// revives a run whose worker is still alive and paying — and then two workers hold what each
	// believes is the same job, exactly the way the store's ReviveExpiredRuns comment describes.
	ClaimLease time.Duration `mapstructure:"claim_lease"`
	// RunTimeout bounds ONE pass over ONE run, provider call included. Kept strictly below
	// ClaimLease by applyDefaults, which is what makes the invariant above true by construction
	// rather than by an operator getting two numbers right.
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
}

// Environment variable names. AutomaticEnv is switched off in this repo, so a name that is not
// read explicitly is silently empty — which is also what a correctly-unset override looks like.
const (
	EnvEnabled      = "DESIGN_GENERATION_ENABLED"
	EnvInterval     = "DESIGN_WORKER_INTERVAL"
	EnvBatchSize    = "DESIGN_WORKER_BATCH_SIZE"
	EnvClaimLease   = "DESIGN_WORKER_CLAIM_LEASE"
	EnvRunTimeout   = "DESIGN_WORKER_RUN_TIMEOUT"
	EnvImageQuality = "DESIGN_IMAGE_QUALITY"
)

// DefaultConfig is the shape of a deployment nobody has tuned.
func DefaultConfig() Config {
	return Config{
		Enabled:        false,
		WorkerInterval: 5 * time.Second,
		BatchSize:      2,
		ClaimLease:     20 * time.Minute,
		RunTimeout:     15 * time.Minute,
		ImageQuality:   "medium",
	}
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
	// THE INVARIANT, ENFORCED RATHER THAN DOCUMENTED. A pass that may outlive its own lease is the
	// one configuration mistake that produces two workers on one paid job, and it is invisible
	// until it happens. Clamping to three quarters leaves the pass room to finish writing its
	// result after the last provider byte arrived.
	if c.RunTimeout >= c.ClaimLease {
		c.RunTimeout = c.ClaimLease / 4 * 3
	}
}

// ConfigFromEnv reads the worker's settings straight from the process environment.
//
// ⚠ THIS IS A BRIDGE, AND IT IS MEANT TO BE DELETED. Every other component of this backend is
// configured through config.Config + viper.BindEnv, and this one is not, for one reason only:
// config/cfg.go was owned by another change in the wave that introduced this package, so a
// `design_generation` section could not be added there without two authors writing one file.
//
// To finish the job: add `DesignGen designgen.Config \`mapstructure:"design_generation"\“ to
// config.Config, add the six BindEnv lines below to bindEnvVars, pass &a.c.DesignGen in app.go,
// and delete this function together with its test.
//
//	viper.BindEnv("design_generation.enabled", "DESIGN_GENERATION_ENABLED")
//	viper.BindEnv("design_generation.worker_interval", "DESIGN_WORKER_INTERVAL")
//	viper.BindEnv("design_generation.batch_size", "DESIGN_WORKER_BATCH_SIZE")
//	viper.BindEnv("design_generation.claim_lease", "DESIGN_WORKER_CLAIM_LEASE")
//	viper.BindEnv("design_generation.run_timeout", "DESIGN_WORKER_RUN_TIMEOUT")
//	viper.BindEnv("design_generation.image_quality", "DESIGN_IMAGE_QUALITY")
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
