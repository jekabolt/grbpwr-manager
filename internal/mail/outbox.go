package mail

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	resend "github.com/jekabolt/grbpwr-manager/openapi/gen/resend"
)

const (
	// outboxLease is how long a claimed row stays invisible to every other worker. It is far longer
	// than a tick (tickTimeout plus outboxBookTimeout), so a live worker always records or releases
	// its rows before the lease runs out; a crashed one's rows come back after it.
	outboxLease = 2 * time.Minute
	// outboxBookTimeout bounds one bookkeeping write. It runs on a context detached from the tick's
	// deadline (keeping the team), so a letter the provider accepted is still marked sent.
	outboxBookTimeout = 5 * time.Second
	// outboxMaxBatches caps the claim rounds of one storefront drain; the tick deadline caps it too.
	outboxMaxBatches = 20
)

// rowVerdict is what a lane decides about a claimed row before any provider call.
type rowVerdict int

const (
	rowSend       rowVerdict = iota
	rowMarkSent              // dispatch nothing and count it sent (bulk mail while MAILER_DISABLED)
	rowQuarantine            // never send it: quarantined with the reason
)

// rowPrepare turns a claimed row into the provider request, or a verdict that skips the provider.
// An error is a send failure of that row (counted, retried or dead-lettered like a provider error).
type rowPrepare func(*entity.SendEmailRequest) (resend.SendEmailRequest, rowVerdict, string, error)

// outboxLane drains one team's outbox (send_email_request): claim a batch under a lease, send each
// row with a stable idempotency key, record the outcome. A row the worker never offered to the
// provider (local budget, its own deadline or shutdown, a 429) is released without counting an
// attempt; a provider failure counts one and is retried with backoff or dead-lettered.
type outboxLane struct {
	name        string // log label
	repo        dependency.Mail
	cli         dependency.Sender
	teamID      int64
	keySpace    string   // idempotency key namespace (outboxKeySpace)
	kinds       []string // nil: every kind
	notBefore   time.Time
	batch       int
	rounds      int // claim rounds per drain (at least one)
	maxAttempts int
	retryBase   time.Duration
	retryMax    time.Duration
	acquire     func(context.Context) error // the Resend budget; an error releases the row
	cooldown    func(time.Duration)         // a 429's Retry-After
	prepare     rowPrepare
}

// outboxKeySpace is the idempotency namespace of one installation's lane: 8 hex chars of a hash of
// what tells installations apart (sender, base URLs), so two installations sharing a Resend account
// never collide on the same team and row id.
func outboxKeySpace(parts ...string) string {
	h := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return "mail-" + hex.EncodeToString(h[:4])
}

// idempotencyKey is stable for the life of a row: namespace, team, row id and a fingerprint of the
// row's creation time, recipient and subject (none of which change). The fingerprint keeps a reused
// id (a database restored or reseeded within Resend's 24h key window) from replaying an old letter's
// answer instead of sending the new one.
func (l *outboxLane) idempotencyKey(row *entity.SendEmailRequest) string {
	fp := sha256.Sum256([]byte(row.CreatedAt.UTC().Format(time.RFC3339Nano) + "\x00" + row.To + "\x00" + row.Subject))
	return fmt.Sprintf("%s-t%d-m%d-%s", l.keySpace, l.teamID, row.Id, hex.EncodeToString(fp[:4]))
}

// drain sends what is due until the outbox has nothing claimable, the context ends or the budget
// runs dry. Only bookkeeping failures (the database) are returned; send failures stay on their rows.
func (l *outboxLane) drain(ctx context.Context) error {
	for round := 0; round < max(l.rounds, 1); round++ {
		if ctx.Err() != nil {
			return nil
		}
		token := uuid.NewString()
		rows, err := l.repo.ClaimUnsent(ctx, entity.MailClaim{
			Limit:       l.batch,
			MaxAttempts: l.maxAttempts,
			Lease:       outboxLease,
			Token:       token,
			Kinds:       l.kinds,
			NotBefore:   l.notBefore,
			Now:         time.Now().UTC(),
		})
		if err != nil {
			if errors.Is(ctx.Err(), context.Canceled) {
				return nil // shutdown
			}
			return fmt.Errorf("%s: claim unsent: %w", l.name, err)
		}
		if len(rows) == 0 {
			return nil
		}
		more, err := l.sendBatch(ctx, rows, token)
		if err != nil {
			return err
		}
		if !more || len(rows) < l.batch {
			return nil
		}
	}
	return nil
}

// sendBatch sends claimed rows in order. more=false: the lane stopped (budget, deadline, 429) and
// released the rest of the batch. A bookkeeping error does not abandon the batch: the row keeps its
// lease and comes back under the same idempotency key; the first such error is returned.
func (l *outboxLane) sendBatch(ctx context.Context, rows []entity.SendEmailRequest, token string) (bool, error) {
	var firstErr error
	note := func(err error) {
		if err != nil && firstErr == nil {
			firstErr = err
		}
	}
	for i := range rows {
		stop, err := l.sendRow(ctx, &rows[i])
		note(err)
		if !stop {
			continue
		}
		rest := make([]int, 0, len(rows)-i)
		for _, r := range rows[i:] {
			rest = append(rest, r.Id)
		}
		note(l.book(ctx, func(c context.Context) error { return l.repo.ReleaseClaims(c, rest, token) }))
		return false, firstErr
	}
	return true, firstErr
}

// sendRow handles one claimed row. stop=true: the row was not offered to the provider and must be
// released with the rest of the batch, no attempt counted.
func (l *outboxLane) sendRow(ctx context.Context, row *entity.SendEmailRequest) (bool, error) {
	req, verdict, reason, err := l.prepare(row)
	if err != nil {
		return false, l.recordFailure(ctx, row, err)
	}
	switch verdict {
	case rowQuarantine:
		slog.Default().WarnContext(ctx, "mail: outbox row quarantined", slog.String("lane", l.name),
			slog.Int("mailId", row.Id), slog.String("reason", reason))
		return false, l.book(ctx, func(c context.Context) error { return l.repo.QuarantineMail(c, row.Id, reason) })
	case rowMarkSent:
		return false, l.book(ctx, func(c context.Context) error { return l.repo.UpdateSent(c, row.Id) })
	}

	if err := l.acquire(ctx); err != nil {
		return true, nil // local budget or the tick's deadline: back to the queue, no attempt counted
	}
	resp, err := l.cli.PostEmails(ctx, req, idempotencyKeyEditor(l.idempotencyKey(row)))
	if resp != nil {
		defer func() {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
		}()
	}
	if ctx.Err() != nil && (err != nil || resp == nil) {
		// Our own deadline or shutdown cut the call: whether it reached Resend is unknown, and the
		// same idempotency key makes the next send of this row safe either way.
		return true, nil
	}
	if resp != nil && resp.StatusCode == http.StatusTooManyRequests {
		retryAfter := parseRetryAfter(resp.Header.Get("Retry-After"), time.Now())
		if retryAfter <= 0 {
			retryAfter = time.Second
		}
		l.cooldown(retryAfter)
		return true, nil
	}
	if err != nil {
		return false, l.recordFailure(ctx, row, fmt.Errorf("error sending email: %w", err))
	}
	if resp != nil && (resp.StatusCode < 200 || resp.StatusCode >= 300) {
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		herr := &HTTPSendError{StatusCode: resp.StatusCode}
		if readErr == nil {
			herr.Body = string(body)
		}
		return false, l.recordFailure(ctx, row, herr)
	}
	// resp == nil: dependency.Sender test doubles answer (nil, nil); the generated client never does.
	return false, l.book(ctx, func(c context.Context) error { return l.repo.UpdateSent(c, row.Id) })
}

// recordFailure counts one attempt: a retry with backoff while the failure is transient and attempts
// remain, else the row is dead-lettered.
func (l *outboxLane) recordFailure(ctx context.Context, row *entity.SendEmailRequest, sendErr error) error {
	msg := sendErr.Error()
	attempts := row.SendAttemptCount + 1
	slog.Default().ErrorContext(ctx, "can't send mail", slog.String("lane", l.name),
		slog.String("err", msg), slog.Int("mailId", row.Id), slog.Int("sendAttemptCount", row.SendAttemptCount))
	if !IsTransientSendFailure(sendErr) || attempts >= l.maxAttempts {
		return l.book(ctx, func(c context.Context) error { return l.repo.MarkSendDead(c, row.Id, msg, l.maxAttempts) })
	}
	next := time.Now().UTC().Add(RetryDelayAfterAttempt(l.retryBase, l.retryMax, attempts))
	return l.book(ctx, func(c context.Context) error { return l.repo.ScheduleSendRetry(c, row.Id, msg, next) })
}

// book runs one bookkeeping write detached from the tick's deadline but keeping its values (the
// team), so a letter Resend accepted at the last moment is still recorded.
func (l *outboxLane) book(ctx context.Context, write func(context.Context) error) error {
	bctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), outboxBookTimeout)
	defer cancel()
	if err := write(bctx); err != nil {
		slog.Default().ErrorContext(ctx, "mail: outbox bookkeeping failed", slog.String("lane", l.name),
			slog.String("err", err.Error()))
		return fmt.Errorf("%s: outbox bookkeeping: %w", l.name, err)
	}
	return nil
}
