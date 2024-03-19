package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
)

type mailStore struct {
	*MYSQLStore
}

// Mail returns an object implementing mail interface
func (ms *MYSQLStore) Mail() dependency.Mail {
	return &mailStore{
		MYSQLStore: ms,
	}
}

func (ms *MYSQLStore) AddMail(ctx context.Context, ser *entity.SendEmailRequest) (int, error) {
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

	id, err := ExecNamedLastId(ctx, ms.DB(), query, params)
	if err != nil {
		return 0, fmt.Errorf("failed to add mail: %w", err)
	}

	return id, nil
}

func (ms *MYSQLStore) GetAllUnsent(ctx context.Context, withError bool) ([]entity.SendEmailRequest, error) {
	var query string

	if withError {
		// Include records with an error_msg
		query = `SELECT * FROM send_email_request WHERE sent = false`
	} else {
		// Exclude records with non-null error_msg
		query = `SELECT * FROM send_email_request WHERE sent = false AND error_msg IS NULL`
	}

	srs, err := QueryListNamed[entity.SendEmailRequest](ctx, ms.DB(), query, map[string]any{})
	if err != nil {
		return nil, fmt.Errorf("failed to get media: %w", err)
	}

	return srs, nil
}
func (ms *MYSQLStore) UpdateSent(ctx context.Context, id int) error {
	query := `UPDATE send_email_request SET sent = true, sent_at = :sentAt WHERE id = :id`
	err := ExecNamed(ctx, ms.DB(), query, map[string]any{
		"id":     id,
		"sentAt": sql.NullTime{Time: time.Now(), Valid: true},
	})
	if err != nil {
		return fmt.Errorf("failed to update sent: %w", err)
	}
	return nil
}

func (ms *MYSQLStore) AddError(ctx context.Context, id int, errMsg string) error {
	query := `UPDATE send_email_request SET error_msg = :err WHERE id = :id`
	err := ExecNamed(ctx, ms.DB(), query, map[string]any{
		"id":  id,
		"err": errMsg,
	})
	if err != nil {
		return fmt.Errorf("failed to update sent: %w", err)
	}
	return nil
}
