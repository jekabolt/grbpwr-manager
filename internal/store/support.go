package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
)

type supportStore struct {
	*MYSQLStore
}

// Support returns an object implementing Support interface
func (ms *MYSQLStore) Support() dependency.Support {
	return &supportStore{
		MYSQLStore: ms,
	}
}

// GetSupportTicketsPaged retrieves all support tickets from the database
func (s *supportStore) GetSupportTicketsPaged(ctx context.Context, limit, offset int, orderFactor entity.OrderFactor, status bool) ([]entity.SupportTicket, error) {
	var tickets []entity.SupportTicket
	query := `
		SELECT id, 
		topic,
		subject,
		civility,
		email,
		first_name,
		last_name,
		order_reference,
		notes,
		created_at,
		status,
		resolved_at,
		updated_at
		FROM support_ticket
		WHERE status = :status
		ORDER BY created_at DESC
		LIMIT :limit OFFSET :offset
	`

	tickets, err := QueryListNamed[entity.SupportTicket](ctx, s.DB(), query, map[string]any{
		"limit":  limit,
		"offset": offset,
		"status": status,
	})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return []entity.SupportTicket{}, nil
		}
		return nil, fmt.Errorf("can't get support tickets: %w", err)
	}

	return tickets, nil
}

func (s *supportStore) UpdateStatus(ctx context.Context, id int, status bool) error {
	var resolvedAt time.Time
	if status {
		resolvedAt = time.Now()
	}
	query := `
		UPDATE support_ticket
		SET status = :status, resolved_at = :resolved_at
		WHERE id = :id
	`
	_, err := s.DB().NamedExecContext(ctx, query, map[string]any{
		"id":         id,
		"status":     status,
		"resolvedAt": resolvedAt,
	})
	if err != nil {
		return fmt.Errorf("can't update status: %w", err)
	}
	return nil
}

func (s *supportStore) SubmitTicket(ctx context.Context, ticket entity.SupportTicketInsert) error {
	query := `
		INSERT INTO support_ticket (topic, subject, civility, email, first_name, last_name, order_reference, notes)
		VALUES (:topic, :subject, :civility, :email, :first_name, :last_name, :order_reference, :notes)
	`
	_, err := s.DB().NamedExecContext(ctx, query, ticket)
	if err != nil {
		return fmt.Errorf("can't submit ticket: %w", err)
	}
	return nil
}
