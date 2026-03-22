package communication

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
)

// sendEmailRequestUnsentColumns lists only columns needed by the worker for sending.
// Table-qualified for use with JOINs (email_suppression also has id).
const sendEmailRequestUnsentColumns = `
	send_email_request.id,
	send_email_request.from_email,
	send_email_request.to_email,
	send_email_request.html,
	send_email_request.subject,
	send_email_request.reply_to,
	send_email_request.send_attempt_count
`

// AddMail queues a new email to be sent.
func (s *Store) AddMail(ctx context.Context, ser *entity.SendEmailRequest) (int, error) {
	query := `
	INSERT INTO 
	send_email_request 
		(from_email, to_email, html, subject, reply_to, sent, sent_at, next_retry_at)
	VALUES
		(:fromEmail, :toEmail, :html, :subject, :replyTo, :sent, :sentAt, :nextRetryAt)
	`
	params := map[string]any{
		"fromEmail":   ser.From,
		"toEmail":     ser.To,
		"html":        ser.Html,
		"subject":     ser.Subject,
		"replyTo":     ser.ReplyTo,
		"sent":        ser.Sent,
		"nextRetryAt": ser.NextRetryAt,
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
	query := `UPDATE send_email_request SET sent = true, sent_at = :sentAt WHERE id = :id`
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
	next_retry_at = :nextRetry
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
	next_retry_at = NULL
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
