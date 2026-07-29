package mail

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

var ErrCampaignBudgetUnavailable = errors.New("campaign Resend budget unavailable")

// ResendBudget is shared by every provider call made by one process.
// Incident guarantee (c): campaign acquisition is serialized, refuses while a
// transactional waiter exists, and requires reserve+1 tokens before consuming
// one. A blast therefore cannot take the last reserved transactional token.
type ResendBudget struct {
	mu                    sync.Mutex
	limiter               *rate.Limiter
	transactionalReserve  int
	transactionalWaiters  int
	globalCooldownUntil   time.Time
	campaignCooldownUntil time.Time
	now                   func() time.Time
}

func NewResendBudget(requestsPerSecond float64, burst, transactionalReserve int) *ResendBudget {
	configuredBurst := burst
	configuredReserve := transactionalReserve
	if requestsPerSecond <= 0 {
		requestsPerSecond = 2
	}
	if burst < 2 {
		burst = 2
	}
	if transactionalReserve < 1 {
		transactionalReserve = 1
	}
	if transactionalReserve >= burst {
		transactionalReserve = burst - 1
	}
	if burst != configuredBurst || transactionalReserve != configuredReserve {
		slog.Warn(
			"adjusted invalid Resend budget configuration",
			slog.Int("configured_burst", configuredBurst),
			slog.Int("effective_burst", burst),
			slog.Int("configured_transactional_reserve", configuredReserve),
			slog.Int("effective_transactional_reserve", transactionalReserve),
		)
	}
	return &ResendBudget{
		limiter:              rate.NewLimiter(rate.Limit(requestsPerSecond), burst),
		transactionalReserve: transactionalReserve,
		now:                  time.Now,
	}
}

func (b *ResendBudget) WaitTransactional(ctx context.Context) error {
	b.mu.Lock()
	b.transactionalWaiters++
	b.mu.Unlock()
	defer func() {
		b.mu.Lock()
		b.transactionalWaiters--
		b.mu.Unlock()
	}()

	for {
		b.mu.Lock()
		delay := b.globalCooldownUntil.Sub(b.now())
		b.mu.Unlock()
		if delay <= 0 {
			break
		}
		timer := time.NewTimer(delay)
		select {
		case <-timer.C:
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		}
	}
	return b.limiter.Wait(ctx)
}

func (b *ResendBudget) TryCampaign() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := b.now()
	if b.transactionalWaiters > 0 ||
		now.Before(b.globalCooldownUntil) ||
		now.Before(b.campaignCooldownUntil) {
		return false
	}
	if b.limiter.TokensAt(now) < float64(b.transactionalReserve+1) {
		return false
	}
	return b.limiter.AllowN(now, 1)
}

func (b *ResendBudget) CooldownCampaign(delay time.Duration) {
	b.mu.Lock()
	defer b.mu.Unlock()
	until := b.now().Add(delay)
	if until.After(b.campaignCooldownUntil) {
		b.campaignCooldownUntil = until
	}
}

func (b *ResendBudget) CooldownGlobal(delay time.Duration) {
	b.mu.Lock()
	defer b.mu.Unlock()
	until := b.now().Add(delay)
	if until.After(b.globalCooldownUntil) {
		b.globalCooldownUntil = until
	}
}
