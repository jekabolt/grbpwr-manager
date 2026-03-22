package mail

import (
	"context"
	"errors"
	"time"
)

// IsTransientSendFailure reports whether the error is worth retrying (backoff + attempt limit).
func IsTransientSendFailure(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var he *HTTPSendError
	if errors.As(err, &he) {
		if he.StatusCode >= 500 {
			return true
		}
		if he.StatusCode == 408 {
			return true
		}
		if he.StatusCode >= 400 && he.StatusCode < 500 {
			return false
		}
	}
	// Network / client errors without HTTP status: retry.
	return true
}

// RetryDelayAfterAttempt returns backoff after a failed send; newAttemptCount is the count after increment (1-based).
func RetryDelayAfterAttempt(base, max time.Duration, newAttemptCount int) time.Duration {
	if newAttemptCount < 1 {
		newAttemptCount = 1
	}
	d := base
	for i := 1; i < newAttemptCount; i++ {
		next := d * 2
		if next >= max {
			return max
		}
		d = next
	}
	if d > max {
		return max
	}
	return d
}

func applyMailerRetryDefaults(c *Config) {
	if c.MaxSendAttempts <= 0 {
		c.MaxSendAttempts = 10
	}
	if c.RetryBaseInterval <= 0 {
		c.RetryBaseInterval = time.Minute
	}
	if c.RetryMaxInterval <= 0 {
		c.RetryMaxInterval = time.Hour
	}
	if c.InlineSendLease <= 0 {
		c.InlineSendLease = 10 * time.Minute
	}
}
