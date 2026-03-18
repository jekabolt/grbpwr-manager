package communication

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
)

// AddMail queues a new email to be sent.
func (s *Store) AddMail(ctx context.Context, ser *entity.SendEmailRequest) (int, error) {
	query := `
	INSERT INTO 
	send_email_request 
		(from_email, to_email, html, subject, reply_to, sent, sent_at)
	VALUES
		(:fromEmail, :toEmail, :html, :subject, :replyTo, :sent, :sentAt)
	`
	params := map[string]any{
		"fromEmail": ser.From,
		"toEmail":   ser.To,
		"html":      ser.Html,
		"subject":   ser.Subject,
		"replyTo":   ser.ReplyTo,
		"sent":      ser.Sent,
	}
	if ser.Sent {
		params["sentAt"] = sql.NullTime{Time: time.Now(), Valid: true}
	} else {
		params["sentAt"] = sql.NullTime{Time: time.Now(), Valid: false}
	}

	id, err := storeutil.ExecNamedLastId(ctx, s.DB, query, params)
	if err != nil {
		return 0, fmt.Errorf("failed to add mail: %w", err)
	}

	return id, nil
}

// GetAllUnsent returns all unsent email requests.
func (s *Store) GetAllUnsent(ctx context.Context, withError bool) ([]entity.SendEmailRequest, error) {
	var query string

	if withError {
		query = `SELECT * FROM send_email_request WHERE sent = false`
	} else {
		query = `SELECT * FROM send_email_request WHERE sent = false AND error_msg IS NULL`
	}

	srs, err := storeutil.QueryListNamed[entity.SendEmailRequest](ctx, s.DB, query, map[string]any{})
	if err != nil {
		return nil, fmt.Errorf("failed to get unsent emails: %w", err)
	}

	return srs, nil
}

// UpdateSent marks an email as sent.
func (s *Store) UpdateSent(ctx context.Context, id int) error {
	query := `UPDATE send_email_request SET sent = true, sent_at = :sentAt WHERE id = :id`
	err := storeutil.ExecNamed(ctx, s.DB, query, map[string]any{
		"id":     id,
		"sentAt": sql.NullTime{Time: time.Now(), Valid: true},
	})
	if err != nil {
		return fmt.Errorf("failed to update sent: %w", err)
	}
	return nil
}

// AddError records an error message for a failed email send attempt.
func (s *Store) AddError(ctx context.Context, id int, errMsg string) error {
	query := `UPDATE send_email_request SET error_msg = :err WHERE id = :id`
	err := storeutil.ExecNamed(ctx, s.DB, query, map[string]any{
		"id":  id,
		"err": errMsg,
	})
	if err != nil {
		return fmt.Errorf("failed to update sent: %w", err)
	}
	return nil
}
