package mail

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/dto"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/saferun"
	"github.com/jekabolt/grbpwr-manager/internal/tenant"
	resend "github.com/jekabolt/grbpwr-manager/openapi/gen/resend"
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

// storefrontClaimBatch is how many rows one claim round of the storefront's outbox leases.
const storefrontClaimBatch = 50

func (m *Mailer) handleUnsent(ctx context.Context) error {
	return m.storefrontLane(ctx).drain(ctx)
}

// storefrontLane drains the storefront team's own outbox: every kind, the storefront Resend budget
// (transactional priority) and the List-Unsubscribe headers, as the worker always sent them.
func (m *Mailer) storefrontLane(ctx context.Context) *outboxLane {
	var teamID int64
	if t, ok := tenant.From(ctx); ok {
		teamID = t.ID
	}
	return &outboxLane{
		name:        "mail",
		repo:        m.mailRepository,
		cli:         m.cli,
		teamID:      teamID,
		keySpace:    outboxKeySpace("storefront", m.c.FromEmail, m.c.UnsubscribeBaseURL),
		batch:       storefrontClaimBatch,
		rounds:      outboxMaxBatches,
		maxAttempts: m.c.MaxSendAttempts,
		retryBase:   m.c.RetryBaseInterval,
		retryMax:    m.c.RetryMaxInterval,
		acquire:     m.budget.WaitTransactional,
		cooldown:    m.budget.CooldownGlobal,
		prepare:     m.prepareStorefrontRow,
	}
}

// prepareStorefrontRow is the worker send of a queued storefront row: bulk mail suppressed while the
// mailer is disabled counts as sent; the stored recipient is re-validated; List-Unsubscribe headers
// are attached.
func (m *Mailer) prepareStorefrontRow(row *entity.SendEmailRequest) (resend.SendEmailRequest, rowVerdict, string, error) {
	if m.suppressed(row.Subject) {
		return resend.SendEmailRequest{}, rowMarkSent, "", nil
	}
	to, err := validateEmailAddress(row.To)
	if err != nil {
		return resend.SendEmailRequest{}, rowSend, "", fmt.Errorf("invalid recipient in stored email: %w", err)
	}
	ser := *row
	ser.To = to
	req, err := dto.EntitySendEmailRequestToResend(&ser)
	if err != nil {
		return resend.SendEmailRequest{}, rowSend, "", fmt.Errorf("error converting email: %w", err)
	}
	req.Headers = m.listUnsubscribeHeaders(to)
	return *req, rowSend, "", nil
}
