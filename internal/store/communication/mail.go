package communication

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
)

// sendEmailRequestUnsentColumns lists the columns returned for unsent rows: what the worker needs to
// send, plus error_msg — populated on the withError=true path (GetAllUnsent), which surfaces exhausted
// / dead rows including why they last failed.
// Table-qualified for use with JOINs (email_suppression also has id).
const sendEmailRequestUnsentColumns = `
	send_email_request.id,
	send_email_request.from_email,
	send_email_request.to_email,
	send_email_request.html,
	send_email_request.subject,
	send_email_request.reply_to,
	send_email_request.send_attempt_count,
	send_email_request.error_msg,
	send_email_request.created_at,
	send_email_request.kind,
	send_email_request.expires_at,
	send_email_request.idempotency_key,
	send_email_request.first_dispatch_at
`

// AddMail queues a new email to be sent. A row without an idempotency key gets a fresh one, written
// back to ser, so every send of the row (inline or by a worker) uses the same key.
func (s *Store) AddMail(ctx context.Context, ser *entity.SendEmailRequest) (int, error) {
	if !ser.IdempotencyKey.Valid || ser.IdempotencyKey.String == "" {
		ser.IdempotencyKey = sql.NullString{String: "mail-" + uuid.NewString(), Valid: true}
	}
	query := `
	INSERT INTO 
	send_email_request 
		(from_email, to_email, html, subject, reply_to, sent, sent_at, next_retry_at, kind, expires_at, idempotency_key)
	VALUES
		(:fromEmail, :toEmail, :html, :subject, :replyTo, :sent, :sentAt, :nextRetryAt, :kind, :expiresAt, :idemKey)
	`
	params := map[string]any{
		"idemKey":     ser.IdempotencyKey,
		"fromEmail":   ser.From,
		"toEmail":     ser.To,
		"html":        ser.Html,
		"subject":     ser.Subject,
		"replyTo":     ser.ReplyTo,
		"sent":        ser.Sent,
		"nextRetryAt": ser.NextRetryAt,
		"kind":        ser.Kind,
		"expiresAt":   ser.ExpiresAt,
	}
	if ser.Sent {
		params["sentAt"] = sql.NullTime{Time: time.Now().UTC(), Valid: true}
	} else {
		params["sentAt"] = sql.NullTime{Time: time.Now().UTC(), Valid: false}
	}

	id, err := storeutil.ExecNamedLastId(ctx, s.DB, query, params)
	if err != nil {
		return 0, fmt.Errorf("failed to add mail: %w", err)
	}

	if metricErr := s.IncrementEmailMetric(ctx, "sent", time.Now().UTC()); metricErr != nil {
		slog.Default().WarnContext(ctx, "failed to increment sent email metric", slog.String("err", metricErr.Error()))
	}

	return id, nil
}

// GetAllUnsent returns unsent email requests, always excluding addresses in email_suppression.
// If withError is false, only rows eligible for the worker are returned: under maxSendAttempts and past next_retry_at.
// If withError is true, every unsent non-suppressed row is returned (including exhausted retries), ordered by id.
func (s *Store) GetAllUnsent(ctx context.Context, withError bool, maxSendAttempts int, nowUTC time.Time) ([]entity.SendEmailRequest, error) {
	var query string
	params := map[string]any{}

	if withError {
		query = `
SELECT
` + sendEmailRequestUnsentColumns + `
FROM
	send_email_request
LEFT JOIN
	email_suppression ON send_email_request.to_email = email_suppression.email
WHERE
	send_email_request.sent = false
	AND email_suppression.id IS NULL
ORDER BY
	send_email_request.id
`
	} else {
		query = `
SELECT
` + sendEmailRequestUnsentColumns + `
FROM
	send_email_request
LEFT JOIN
	email_suppression ON send_email_request.to_email = email_suppression.email
WHERE
	send_email_request.sent = false
	AND send_email_request.quarantined_at IS NULL
	AND send_email_request.send_attempt_count < :maxAttempts
	AND (send_email_request.next_retry_at IS NULL OR send_email_request.next_retry_at <= :nowUTC)
	AND email_suppression.id IS NULL
ORDER BY
	send_email_request.id
`
		params["maxAttempts"] = maxSendAttempts
		params["nowUTC"] = nowUTC
	}

	srs, err := storeutil.QueryListNamed[entity.SendEmailRequest](ctx, s.DB, query, params)
	if err != nil {
		return nil, fmt.Errorf("failed to get unsent emails: %w", err)
	}

	return srs, nil
}

// AddSuppression adds an email address to the suppression list. Idempotent — duplicate inserts are ignored.
func (s *Store) AddSuppression(ctx context.Context, email string, reason entity.SuppressionReason) error {
	query := `
INSERT INTO email_suppression (email, reason)
VALUES (:email, :reason)
ON DUPLICATE KEY UPDATE reason = VALUES(reason)
`
	err := storeutil.ExecNamed(ctx, s.DB, query, map[string]any{
		"email":  email,
		"reason": string(reason),
	})
	if err != nil {
		return fmt.Errorf("failed to add suppression for %s: %w", email, err)
	}
	return nil
}

// IsSuppressed returns true if the email address is on the suppression list.
func (s *Store) IsSuppressed(ctx context.Context, email string) (bool, error) {
	query := `
SELECT COUNT(*)
FROM email_suppression
WHERE email = :email
`
	count, err := storeutil.QueryCountNamed(ctx, s.DB, query, map[string]any{"email": email})
	if err != nil {
		return false, fmt.Errorf("failed to check suppression for %s: %w", email, err)
	}
	return count > 0, nil
}

// UpdateSent marks an email as sent.
func (s *Store) UpdateSent(ctx context.Context, id int) error {
	query := `UPDATE send_email_request SET sent = true, sent_at = :sentAt, claim_token = NULL WHERE id = :id`
	err := storeutil.ExecNamed(ctx, s.DB, query, map[string]any{
		"id":     id,
		"sentAt": sql.NullTime{Time: time.Now().UTC(), Valid: true},
	})
	if err != nil {
		return fmt.Errorf("failed to update sent: %w", err)
	}
	return nil
}

// ClearNextRetryAt sets next_retry_at to NULL for an unsent row so the worker can pick it up.
func (s *Store) ClearNextRetryAt(ctx context.Context, id int) error {
	query := `
UPDATE
	send_email_request
SET
	next_retry_at = NULL
WHERE
	id = :id
	AND sent = false
`
	err := storeutil.ExecNamed(ctx, s.DB, query, map[string]any{
		"id": id,
	})
	if err != nil {
		return fmt.Errorf("failed to clear next_retry_at: %w", err)
	}
	return nil
}

// ScheduleSendRetry records a failed transient attempt and when to try again.
func (s *Store) ScheduleSendRetry(ctx context.Context, id int, errMsg string, nextRetryAt time.Time) error {
	query := `
UPDATE
	send_email_request
SET
	send_attempt_count = send_attempt_count + 1,
	error_msg = :err,
	next_retry_at = :nextRetry,
	claim_token = NULL
WHERE
	id = :id
`
	err := storeutil.ExecNamed(ctx, s.DB, query, map[string]any{
		"id":        id,
		"err":       errMsg,
		"nextRetry": nextRetryAt.UTC(),
	})
	if err != nil {
		return fmt.Errorf("failed to schedule send retry: %w", err)
	}
	return nil
}

// IncrementEmailMetric atomically increments a counter in email_daily_metrics for the given date.
// metricType must be one of: "sent", "delivered", "bounced", "opened", "clicked".
func (s *Store) IncrementEmailMetric(ctx context.Context, metricType string, date time.Time) error {
	allowedColumns := map[string]string{
		"sent":      "emails_sent",
		"delivered": "emails_delivered",
		"bounced":   "emails_bounced",
		"opened":    "emails_opened",
		"clicked":   "emails_clicked",
	}
	col, ok := allowedColumns[metricType]
	if !ok {
		return fmt.Errorf("unknown email metric type: %s", metricType)
	}

	query := fmt.Sprintf(`
INSERT INTO email_daily_metrics (date, %s)
VALUES (:date, 1)
ON DUPLICATE KEY UPDATE %s = %s + 1
`, col, col, col)

	err := storeutil.ExecNamed(ctx, s.DB, query, map[string]any{
		"date": date.UTC().Format("2006-01-02"),
	})
	if err != nil {
		return fmt.Errorf("failed to increment email metric %s: %w", metricType, err)
	}
	return nil
}

// GetEmailMetrics returns daily email metric rows for a date range (inclusive).
func (s *Store) GetEmailMetrics(ctx context.Context, from, to time.Time) ([]entity.EmailDailyMetrics, error) {
	query := `
SELECT
	id,
	date,
	emails_sent,
	emails_delivered,
	emails_bounced,
	emails_opened,
	emails_clicked,
	created_at,
	updated_at
FROM
	email_daily_metrics
WHERE
	date >= :from
	AND date <= :to
ORDER BY
	date
`
	rows, err := storeutil.QueryListNamed[entity.EmailDailyMetrics](ctx, s.DB, query, map[string]any{
		"from": from.UTC().Format("2006-01-02"),
		"to":   to.UTC().Format("2006-01-02"),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to get email metrics: %w", err)
	}
	return rows, nil
}

// MarkSendDead stops further worker attempts (permanent failure or retries exhausted).
func (s *Store) MarkSendDead(ctx context.Context, id int, errMsg string, maxSendAttempts int) error {
	query := `
UPDATE
	send_email_request
SET
	error_msg = :err,
	send_attempt_count = :maxAttempts,
	next_retry_at = NULL,
	claim_token = NULL
WHERE
	id = :id
`
	err := storeutil.ExecNamed(ctx, s.DB, query, map[string]any{
		"id":          id,
		"err":         errMsg,
		"maxAttempts": maxSendAttempts,
	})
	if err != nil {
		return fmt.Errorf("failed to mark send dead: %w", err)
	}
	return nil
}

// sendEmailClaimableSQL is the worker-eligibility predicate of an outbox row: unsent, not
// quarantined, under the attempt cap, its retry time (or lease) passed, not expired, recipient not
// suppressed. Storefront rows carry no expires_at, so the expiry term never holds them back.
const sendEmailClaimableSQL = `
	send_email_request.sent = false
	AND send_email_request.quarantined_at IS NULL
	AND send_email_request.send_attempt_count < :maxAttempts
	AND (send_email_request.next_retry_at IS NULL OR send_email_request.next_retry_at <= :now)
	AND (send_email_request.expires_at IS NULL OR send_email_request.expires_at > :now)
	AND NOT EXISTS (SELECT 1 FROM email_suppression WHERE email_suppression.email = send_email_request.to_email)`

// errBadMailClaim marks a claim that could never be closed or would never expire.
var errBadMailClaim = errors.New("invalid outbox claim")

// ClaimUnsent leases up to q.Limit worker-eligible rows to q.Token: next_retry_at becomes the lease
// end (q.Now+q.Lease), so no other worker sees them until the lease runs out. Two instances never
// get the same row — the pick is SELECT … FOR UPDATE SKIP LOCKED and the lease UPDATE re-checks the
// predicate. Rows come back ordered by id.
func (s *Store) ClaimUnsent(ctx context.Context, q entity.MailClaim) ([]entity.SendEmailRequest, error) {
	if q.Limit <= 0 || q.Lease <= 0 || q.Token == "" || q.MaxAttempts <= 0 {
		return nil, fmt.Errorf("%w: limit %d, lease %s, token set %t, max attempts %d",
			errBadMailClaim, q.Limit, q.Lease, q.Token != "", q.MaxAttempts)
	}
	if s.txFunc == nil {
		return nil, fmt.Errorf("%w: store has no transaction", errBadMailClaim)
	}
	now := q.Now.UTC()
	if q.Now.IsZero() {
		now = time.Now().UTC()
	}
	where := sendEmailClaimableSQL
	params := map[string]any{
		"maxAttempts": q.MaxAttempts,
		"now":         now,
		"n":           q.Limit,
	}
	if len(q.Kinds) > 0 {
		where += `
	AND send_email_request.kind IN (:kinds)`
		params["kinds"] = q.Kinds
	}
	if !q.NotBefore.IsZero() {
		// UNIX timestamps on both sides: the session time zone cannot shift the cutover.
		where += `
	AND UNIX_TIMESTAMP(send_email_request.created_at) >= :notBefore`
		params["notBefore"] = q.NotBefore.Unix()
	}

	var claimed []entity.SendEmailRequest
	err := s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		claimed = nil
		db := rep.DB()
		ids, err := storeutil.QueryScalarListNamed[int](ctx, db, `
SELECT send_email_request.id FROM send_email_request
WHERE `+where+`
ORDER BY send_email_request.id
LIMIT :n
FOR UPDATE OF send_email_request SKIP LOCKED`, params)
		if err != nil {
			return fmt.Errorf("failed to pick unsent emails: %w", err)
		}
		if len(ids) == 0 {
			return nil
		}
		taken := make([]int, 0, len(ids))
		for _, id := range ids {
			p := make(map[string]any, len(params)+3)
			for k, v := range params {
				p[k] = v
			}
			p["id"] = id
			p["tok"] = q.Token
			p["leaseEnd"] = now.Add(q.Lease)
			rows, err := storeutil.ExecNamedRows(ctx, db, `
UPDATE send_email_request
SET next_retry_at = :leaseEnd, claim_token = :tok
WHERE send_email_request.id = :id AND `+where, p)
			if err != nil {
				return fmt.Errorf("failed to claim email %d: %w", id, err)
			}
			if rows == 1 {
				taken = append(taken, id)
			}
		}
		if len(taken) == 0 {
			return nil
		}
		claimed, err = storeutil.QueryListNamed[entity.SendEmailRequest](ctx, db, `
SELECT `+sendEmailRequestUnsentColumns+`
FROM send_email_request
WHERE send_email_request.id IN (:ids) AND send_email_request.claim_token = :tok
ORDER BY send_email_request.id`, map[string]any{"ids": taken, "tok": q.Token})
		if err != nil {
			return fmt.Errorf("failed to read claimed emails: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return claimed, nil
}

// ReleaseClaims hands rows still leased to token back to the queue without counting an attempt:
// the worker never reached the provider for them (local budget, deadline, shutdown, 429).
func (s *Store) ReleaseClaims(ctx context.Context, ids []int, token string) error {
	if len(ids) == 0 {
		return nil
	}
	if token == "" {
		return fmt.Errorf("%w: release without a token", errBadMailClaim)
	}
	err := storeutil.ExecNamed(ctx, s.DB, `
UPDATE send_email_request
SET next_retry_at = NULL, claim_token = NULL
WHERE id IN (:ids) AND claim_token = :tok AND sent = false`, map[string]any{"ids": ids, "tok": token})
	if err != nil {
		return fmt.Errorf("failed to release claimed emails: %w", err)
	}
	return nil
}

// MarkMailDispatched records that the worker holding token is about to offer row id to the provider:
// first_dispatch_at keeps the first such moment, dispatch_count counts them. It returns
// entity.ErrMailClaimLost when the row is no longer leased to token (then nothing may be sent). The
// count always changes, so the driver's changed-rows answer is exact.
func (s *Store) MarkMailDispatched(ctx context.Context, id int, token string, now time.Time) error {
	if token == "" {
		return fmt.Errorf("%w: dispatch mark without a token", errBadMailClaim)
	}
	if now.IsZero() {
		now = time.Now()
	}
	n, err := storeutil.ExecNamedRows(ctx, s.DB, `
UPDATE send_email_request
SET first_dispatch_at = COALESCE(first_dispatch_at, :now), dispatch_count = dispatch_count + 1
WHERE id = :id AND claim_token = :tok AND sent = false AND quarantined_at IS NULL`, map[string]any{
		"id":  id,
		"tok": token,
		"now": now.UTC(),
	})
	if err != nil {
		return fmt.Errorf("failed to mark email %d dispatched: %w", id, err)
	}
	if n != 1 {
		return entity.ErrMailClaimLost
	}
	return nil
}

// SettleMail records the outcome of a claimed row (sent, retry, dead, quarantined) and ends its
// lease. It applies only while the row is unsent and still leased to q.Token, and returns
// entity.ErrMailClaimLost otherwise. Every outcome clears claim_token, so a matching row always
// counts as changed.
func (s *Store) SettleMail(ctx context.Context, q entity.MailSettle) error {
	if q.Token == "" {
		return fmt.Errorf("%w: settle without a token", errBadMailClaim)
	}
	now := q.Now.UTC()
	if q.Now.IsZero() {
		now = time.Now().UTC()
	}
	params := map[string]any{"id": q.ID, "tok": q.Token, "now": now, "err": q.ErrMsg}
	var set string
	switch q.Outcome {
	case entity.MailSent:
		set = `sent = true, sent_at = :now, provider_email_id = :pid`
		params["pid"] = sql.NullString{String: q.ProviderID, Valid: q.ProviderID != ""}
	case entity.MailRetry:
		if q.NextRetryAt.IsZero() {
			return fmt.Errorf("%w: retry without a time", errBadMailClaim)
		}
		set = `send_attempt_count = send_attempt_count + 1, error_msg = :err, next_retry_at = :next`
		params["next"] = q.NextRetryAt.UTC()
	case entity.MailDead:
		if q.MaxAttempts <= 0 {
			return fmt.Errorf("%w: dead letter without an attempt cap", errBadMailClaim)
		}
		set = `send_attempt_count = :max, error_msg = :err, next_retry_at = NULL`
		params["max"] = q.MaxAttempts
	case entity.MailQuarantined:
		set = `quarantined_at = :now, error_msg = :err, next_retry_at = NULL`
	default:
		return fmt.Errorf("%w: unknown outcome %d", errBadMailClaim, q.Outcome)
	}
	n, err := storeutil.ExecNamedRows(ctx, s.DB, `
UPDATE send_email_request
SET `+set+`, claim_token = NULL
WHERE id = :id AND claim_token = :tok AND sent = false AND quarantined_at IS NULL`, params)
	if err != nil {
		return fmt.Errorf("failed to settle email %d: %w", q.ID, err)
	}
	if n != 1 {
		return entity.ErrMailClaimLost
	}
	return nil
}

// ProviderMailRecipient returns the recipient of outbox row id when the provider accepted it under
// providerID, else "" (no such row, another letter's id, never accepted).
func (s *Store) ProviderMailRecipient(ctx context.Context, id int, providerID string) (string, error) {
	if id <= 0 || providerID == "" {
		return "", nil
	}
	tos, err := storeutil.QueryScalarListNamed[string](ctx, s.DB, `
SELECT to_email FROM send_email_request WHERE id = :id AND provider_email_id = :pid`,
		map[string]any{"id": id, "pid": providerID})
	if err != nil {
		return "", fmt.Errorf("failed to find email %d by provider id: %w", id, err)
	}
	if len(tos) == 0 {
		return "", nil
	}
	return tos[0], nil
}

// QuarantineOutside applies the team-shop lane's never-send rule (entity.MailCutover) to every
// unsent, unquarantined row that nobody holds right now: a NULL or foreign kind, an expired letter,
// or a row created before the shop opened. Returns how many rows it quarantined. A row under an
// active lease or retry delay is left for a later pass (the lane re-checks claimed rows itself). A
// row that already failed keeps its last error as error_msg; quarantined_at says it is out.
// created_at is compared as a UNIX timestamp, so the session time zone cannot shift the cutover.
func (s *Store) QuarantineOutside(ctx context.Context, rule entity.MailCutover) (int64, error) {
	if len(rule.Kinds) == 0 {
		return 0, fmt.Errorf("%w: cutover without kinds", errBadMailClaim)
	}
	now := rule.Now.UTC()
	if rule.Now.IsZero() {
		now = time.Now().UTC()
	}
	var opened int64
	if !rule.OpenedAt.IsZero() {
		opened = rule.OpenedAt.Unix()
	}
	n, err := storeutil.ExecNamedRows(ctx, s.DB, `
UPDATE send_email_request
SET quarantined_at = :now, next_retry_at = NULL, claim_token = NULL, error_msg = COALESCE(error_msg, :reason)
WHERE sent = false
	AND quarantined_at IS NULL
	AND (next_retry_at IS NULL OR next_retry_at <= :now)
	AND (
		kind IS NULL
		OR kind NOT IN (:kinds)
		OR (expires_at IS NOT NULL AND expires_at <= :now)
		OR UNIX_TIMESTAMP(created_at) < :opened
	)`, map[string]any{
		"now":    now,
		"reason": rule.Reason,
		"kinds":  rule.Kinds,
		"opened": opened,
	})
	if err != nil {
		return 0, fmt.Errorf("failed to quarantine stale emails: %w", err)
	}
	return n, nil
}
