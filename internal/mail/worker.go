package mail

import (
	"context"
	"errors"
	"fmt"
	"time"

	"log/slog"

	"github.com/jekabolt/grbpwr-manager/internal/saferun"
)

// tickTimeout bounds the DB + provider work done in a single send tick, so one
// stuck query or hung HTTP send can't block the loop forever or stall graceful
// shutdown.
const tickTimeout = 20 * time.Second

// Backoff bounds for consecutive-failure backoff. On a failed tick an extra
// delay (base * 2^(n-1), capped at backoffMax) is waited before the next
// iteration so a persistently failing dependency (DB / Resend) isn't hammered
// every WorkerInterval. A successful tick resets the backoff. See ga4sync for
// the established pattern in this codebase.
const (
	backoffBase = 30 * time.Second
	backoffMax  = 5 * time.Minute
)

// Start starts the worker
func (m *Mailer) Start(ctx context.Context) error {
	if m.ctx != nil && m.cancel != nil {
		return fmt.Errorf("Mailer already started")
	}

	m.ctx, m.cancel = context.WithCancel(ctx)
	m.wg.Go(func() {
		m.worker(m.ctx)
	})
	return nil
}

// Stop signals the worker to stop and waits for its goroutine to exit, so the
// caller can safely close shared resources (e.g. the DB) afterwards.
func (m *Mailer) Stop() error {
	if m.cancel == nil {
		return fmt.Errorf("Mailer already stopped or not started")
	}

	m.cancel() // This will cancel the context used by the worker
	m.cancel = nil
	m.wg.Wait()
	return nil
}

func (m *Mailer) worker(ctx context.Context) {
	ticker := time.NewTicker(m.c.WorkerInterval)
	defer ticker.Stop()

	// consecutiveFailures drives the extra backoff delay applied after a failed
	// tick. Reset to 0 on the first successful tick.
	var consecutiveFailures int

	for {
		select {
		case <-ticker.C:
			if m.runOnce(ctx) {
				consecutiveFailures = 0
				continue
			}
			consecutiveFailures++
			delay := backoffDelay(consecutiveFailures)
			slog.Default().WarnContext(ctx, "mail: backing off after failed tick",
				slog.Int("consecutive_failures", consecutiveFailures),
				slog.Duration("delay", delay),
			)
			// Wait the extra backoff on top of the ticker interval, but stay
			// responsive to shutdown — never time.Sleep blindly.
			timer := time.NewTimer(delay)
			select {
			case <-timer.C:
			case <-ctx.Done():
				timer.Stop()
				return
			}
		case <-ctx.Done():
			return
		}
	}
}

// backoffDelay returns the extra inter-iteration delay for the given number of
// consecutive failures: base * 2^(n-1), capped at backoffMax.
func backoffDelay(consecutiveFailures int) time.Duration {
	delay := backoffBase
	for i := 1; i < consecutiveFailures; i++ {
		delay *= 2
		if delay >= backoffMax {
			return backoffMax
		}
	}
	return delay
}

// runOnce performs a single send tick and reports whether it drained without a
// fatal error. The deferred saferun.Recover keeps a panic in this iteration from
// killing the worker loop.
func (m *Mailer) runOnce(ctx context.Context) bool {
	defer saferun.Recover(ctx, "mail")

	ctx, cancel := context.WithTimeout(ctx, tickTimeout)
	defer cancel()

	if err := m.handleUnsent(ctx); err != nil {
		m.tracker.MarkError(err)
		slog.Default().ErrorContext(ctx, "can't handle unsent mails",
			slog.String("err", err.Error()),
		)
		return false
	}

	// Record success only when the send tick drained without a fatal error.
	m.tracker.MarkSuccess()
	return true
}

func (m *Mailer) handleUnsent(ctx context.Context) error {
	now := time.Now().UTC()
	unsentEmails, err := m.mailRepository.GetAllUnsent(ctx, false, m.c.MaxSendAttempts, now)
	if err != nil {
		return fmt.Errorf("can't get unsent mails: %w", err)
	}

	for _, email := range unsentEmails {
		// Check for a stop signal before processing each email
		if err := ctx.Err(); err != nil {
			return err
		}

		if err := m.sendRaw(ctx, &email); err != nil {
			slog.Default().ErrorContext(ctx, "can't send mail",
				slog.String("err", err.Error()),
				slog.Int("mailId", email.Id),
				slog.Int("sendAttemptCount", email.SendAttemptCount),
			)

			if errors.Is(err, mailApiLimitReached) {
				return nil // Stop sending mails if API limit is reached
			}
			if errors.Is(err, context.Canceled) {
				return err
			}

			errMsg := err.Error()
			newAttemptCount := email.SendAttemptCount + 1
			transient := IsTransientSendFailure(err)
			exhausted := newAttemptCount >= m.c.MaxSendAttempts
			if !transient || exhausted {
				if err := m.mailRepository.MarkSendDead(ctx, email.Id, errMsg, m.c.MaxSendAttempts); err != nil {
					return fmt.Errorf("can't mark send dead for email %v: %w", email.Id, err)
				}
				continue
			}

			delay := RetryDelayAfterAttempt(m.c.RetryBaseInterval, m.c.RetryMaxInterval, newAttemptCount)
			next := time.Now().UTC().Add(delay)
			if err := m.mailRepository.ScheduleSendRetry(ctx, email.Id, errMsg, next); err != nil {
				return fmt.Errorf("can't schedule retry for email %v: %w", email.Id, err)
			}
		} else {
			// Update the database to mark the email as sent
			if err := m.mailRepository.UpdateSent(ctx, email.Id); err != nil {
				return fmt.Errorf("can't update sent status for email %v: %w", email.Id, err)
			}
		}
	}

	return nil
}
