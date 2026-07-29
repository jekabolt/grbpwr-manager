package campaigndispatch

import "time"

type Config struct {
	WorkerInterval time.Duration `mapstructure:"worker_interval"`
	FanoutPageSize int           `mapstructure:"fanout_page_size"`
	BatchSize      int           `mapstructure:"batch_size"`
	ClaimLease     time.Duration `mapstructure:"claim_lease"`
	MaxAttempts    int           `mapstructure:"max_attempts"`
	RetryBase      time.Duration `mapstructure:"retry_base"`
	RetryMax       time.Duration `mapstructure:"retry_max"`
}

func DefaultConfig() Config {
	return Config{
		WorkerInterval: 2 * time.Second,
		FanoutPageSize: 5000,
		BatchSize:      100,
		ClaimLease:     2 * time.Minute,
		MaxAttempts:    8,
		RetryBase:      30 * time.Second,
		RetryMax:       30 * time.Minute,
	}
}

func applyDefaults(c *Config) {
	defaults := DefaultConfig()
	if c.WorkerInterval <= 0 {
		c.WorkerInterval = defaults.WorkerInterval
	}
	if c.FanoutPageSize <= 0 {
		c.FanoutPageSize = defaults.FanoutPageSize
	}
	if c.BatchSize <= 0 || c.BatchSize > 100 {
		c.BatchSize = defaults.BatchSize
	}
	if c.ClaimLease <= 0 {
		c.ClaimLease = defaults.ClaimLease
	}
	if c.MaxAttempts <= 0 {
		c.MaxAttempts = defaults.MaxAttempts
	}
	if c.RetryBase <= 0 {
		c.RetryBase = defaults.RetryBase
	}
	if c.RetryMax <= 0 {
		c.RetryMax = defaults.RetryMax
	}
}
