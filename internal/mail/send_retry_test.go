package mail

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestIsTransientSendFailure(t *testing.T) {
	t.Parallel()

	t.Run("HTTPSendError_5xx", func(t *testing.T) {
		assert.True(t, IsTransientSendFailure(&HTTPSendError{StatusCode: 500, Body: "{}"}))
		assert.True(t, IsTransientSendFailure(&HTTPSendError{StatusCode: 503, Body: "{}"}))
	})
	t.Run("HTTPSendError_408", func(t *testing.T) {
		assert.True(t, IsTransientSendFailure(&HTTPSendError{StatusCode: 408, Body: ""}))
	})
	t.Run("HTTPSendError_4xx", func(t *testing.T) {
		assert.False(t, IsTransientSendFailure(&HTTPSendError{StatusCode: 400, Body: ""}))
		assert.False(t, IsTransientSendFailure(&HTTPSendError{StatusCode: 422, Body: ""}))
	})
	t.Run("deadline", func(t *testing.T) {
		assert.True(t, IsTransientSendFailure(context.DeadlineExceeded))
	})
	t.Run("canceled", func(t *testing.T) {
		assert.False(t, IsTransientSendFailure(context.Canceled))
	})
	t.Run("wrapped", func(t *testing.T) {
		err := fmt.Errorf("wrap: %w", &HTTPSendError{StatusCode: 502, Body: "x"})
		assert.True(t, IsTransientSendFailure(err))
	})
	t.Run("network_style", func(t *testing.T) {
		assert.True(t, IsTransientSendFailure(errors.New("connection reset")))
	})
}

func TestRetryDelayAfterAttempt(t *testing.T) {
	t.Parallel()

	base := time.Minute
	max := time.Hour

	assert.Equal(t, base, RetryDelayAfterAttempt(base, max, 1))
	assert.Equal(t, 2*base, RetryDelayAfterAttempt(base, max, 2))
	assert.Equal(t, 4*base, RetryDelayAfterAttempt(base, max, 3))
	assert.Equal(t, max, RetryDelayAfterAttempt(base, max, 20))
}

func TestApplyMailerRetryDefaults(t *testing.T) {
	t.Parallel()

	c := &Config{}
	applyMailerRetryDefaults(c)
	assert.Equal(t, 10, c.MaxSendAttempts)
	assert.Equal(t, time.Minute, c.RetryBaseInterval)
	assert.Equal(t, time.Hour, c.RetryMaxInterval)
	assert.Equal(t, 10*time.Minute, c.InlineSendLease)
}
