package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
)

type supportStore struct {
	*MYSQLStore
}

func (ms *MYSQLStore) Support() dependency.Support {
	return &supportStore{
		MYSQLStore: ms,
	}
}

func (s *supportStore) generateCaseNumber(ctx context.Context) (string, error) {
	year := time.Now().Year()
	var maxNum int
	query := `SELECT COALESCE(MAX(CAST(SUBSTRING(case_number, 9) AS UNSIGNED)), 0) 
              FROM support_ticket 
              WHERE case_number LIKE ?`
	err := s.DB().GetContext(ctx, &maxNum, query, fmt.Sprintf("CS-%d-%%", year))
	if err != nil {
		return "", fmt.Errorf("can't get max case number: %w", err)
	}
	return fmt.Sprintf("CS-%d-%05d", year, maxNum+1), nil
}

func (s *supportStore) GetSupportTicketsPaged(ctx context.Context, limit, offset int, orderFactor entity.OrderFactor, filters entity.SupportTicketFilters) ([]entity.SupportTicket, int, error) {
	var tickets []entity.SupportTicket

	whereConditions := []string{}
	args := map[string]any{
		"limit":  limit,
		"offset": offset,
	}

	if filters.Status != nil {
		whereConditions = append(whereConditions, "status = :status")
		args["status"] = *filters.Status
	}
	if filters.Email != "" {
		whereConditions = append(whereConditions, "email LIKE :email")
		args["email"] = "%" + filters.Email + "%"
	}
	if filters.OrderReference != "" {
		whereConditions = append(whereConditions, "order_reference LIKE :order_reference")
		args["order_reference"] = "%" + filters.OrderReference + "%"
	}
	if filters.Topic != "" {
		whereConditions = append(whereConditions, "topic LIKE :topic")
		args["topic"] = "%" + filters.Topic + "%"
	}
	if filters.Category != "" {
		whereConditions = append(whereConditions, "category LIKE :category")
		args["category"] = "%" + filters.Category + "%"
	}
	if filters.Priority != nil {
		whereConditions = append(whereConditions, "priority = :priority")
		args["priority"] = *filters.Priority
	}
	if filters.DateFrom != nil {
		whereConditions = append(whereConditions, "created_at >= :date_from")
		args["date_from"] = *filters.DateFrom
	}
	if filters.DateTo != nil {
		whereConditions = append(whereConditions, "created_at <= :date_to")
		args["date_to"] = *filters.DateTo
	}

	whereClause := ""
	if len(whereConditions) > 0 {
		whereClause = "WHERE " + strings.Join(whereConditions, " AND ")
	}

	orderByClause := "created_at DESC"
	if orderFactor == entity.Ascending {
		orderByClause = "created_at ASC"
	}

	query := fmt.Sprintf(`
		SELECT id, case_number, topic, subject, civility, email, first_name, last_name,
		       order_reference, notes, category, priority, created_at, status, 
		       resolved_at, updated_at, internal_notes
		FROM support_ticket
		%s
		ORDER BY %s
		LIMIT :limit OFFSET :offset
	`, whereClause, orderByClause)

	tickets, err := QueryListNamed[entity.SupportTicket](ctx, s.DB(), query, args)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return []entity.SupportTicket{}, 0, nil
		}
		return nil, 0, fmt.Errorf("can't get support tickets: %w", err)
	}

	countQuery := fmt.Sprintf(`SELECT COUNT(*) FROM support_ticket %s`, whereClause)
	var totalCount int
	rows, err := s.DB().NamedQuery(countQuery, args)
	if err != nil {
		return nil, 0, fmt.Errorf("can't get total count: %w", err)
	}
	defer rows.Close()
	if rows.Next() {
		if err := rows.Scan(&totalCount); err != nil {
			return nil, 0, fmt.Errorf("can't scan total count: %w", err)
		}
	}

	return tickets, totalCount, nil
}

func (s *supportStore) GetSupportTicketById(ctx context.Context, id int) (entity.SupportTicket, error) {
	var ticket entity.SupportTicket
	query := `
		SELECT id, case_number, topic, subject, civility, email, first_name, last_name,
		       order_reference, notes, category, priority, created_at, status, 
		       resolved_at, updated_at, internal_notes
		FROM support_ticket
		WHERE id = ?
	`
	err := s.DB().GetContext(ctx, &ticket, query, id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return entity.SupportTicket{}, fmt.Errorf("support ticket not found")
		}
		return entity.SupportTicket{}, fmt.Errorf("can't get support ticket: %w", err)
	}
	return ticket, nil
}

func (s *supportStore) GetSupportTicketByCaseNumber(ctx context.Context, caseNumber string) (entity.SupportTicket, error) {
	var ticket entity.SupportTicket
	query := `
		SELECT id, case_number, topic, subject, civility, email, first_name, last_name,
		       order_reference, notes, category, priority, created_at, status, 
		       resolved_at, updated_at, internal_notes
		FROM support_ticket
		WHERE case_number = ?
	`
	err := s.DB().GetContext(ctx, &ticket, query, caseNumber)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return entity.SupportTicket{}, fmt.Errorf("support ticket not found")
		}
		return entity.SupportTicket{}, fmt.Errorf("can't get support ticket: %w", err)
	}
	return ticket, nil
}

func (s *supportStore) UpdateStatus(ctx context.Context, id int, status entity.SupportTicketStatus) error {
	var resolvedAt sql.NullTime
	if status == entity.SupportStatusResolved || status == entity.SupportStatusClosed {
		resolvedAt = sql.NullTime{Time: time.Now(), Valid: true}
	}

	query := `
		UPDATE support_ticket
		SET status = :status, resolved_at = :resolved_at
		WHERE id = :id
	`
	_, err := s.DB().NamedExecContext(ctx, query, map[string]any{
		"id":          id,
		"status":      status,
		"resolved_at": resolvedAt,
	})
	if err != nil {
		return fmt.Errorf("can't update status: %w", err)
	}
	return nil
}

func (s *supportStore) UpdatePriority(ctx context.Context, id int, priority entity.SupportTicketPriority) error {
	query := `UPDATE support_ticket SET priority = ? WHERE id = ?`
	_, err := s.DB().ExecContext(ctx, query, priority, id)
	if err != nil {
		return fmt.Errorf("can't update priority: %w", err)
	}
	return nil
}

func (s *supportStore) UpdateCategory(ctx context.Context, id int, category string) error {
	query := `UPDATE support_ticket SET category = ? WHERE id = ?`
	_, err := s.DB().ExecContext(ctx, query, category, id)
	if err != nil {
		return fmt.Errorf("can't update category: %w", err)
	}
	return nil
}

func (s *supportStore) UpdateInternalNotes(ctx context.Context, id int, notes string) error {
	query := `UPDATE support_ticket SET internal_notes = ? WHERE id = ?`
	_, err := s.DB().ExecContext(ctx, query, notes, id)
	if err != nil {
		return fmt.Errorf("can't update internal notes: %w", err)
	}
	return nil
}

func (s *supportStore) SubmitTicket(ctx context.Context, ticket entity.SupportTicketInsert) (string, error) {
	caseNumber, err := s.generateCaseNumber(ctx)
	if err != nil {
		return "", fmt.Errorf("can't generate case number: %w", err)
	}

	if ticket.Priority == "" {
		ticket.Priority = entity.PriorityMedium
	}

	query := `
		INSERT INTO support_ticket (
			case_number, topic, subject, civility, email, first_name, last_name, 
			order_reference, notes, category, priority, status
		)
		VALUES (
			:case_number, :topic, :subject, :civility, :email, :first_name, :last_name,
			:order_reference, :notes, :category, :priority, :status
		)
	`
	_, err = s.DB().NamedExecContext(ctx, query, map[string]any{
		"case_number":     caseNumber,
		"topic":           ticket.Topic,
		"subject":         ticket.Subject,
		"civility":        ticket.Civility,
		"email":           ticket.Email,
		"first_name":      ticket.FirstName,
		"last_name":       ticket.LastName,
		"order_reference": ticket.OrderReference,
		"notes":           ticket.Notes,
		"category":        ticket.Category,
		"priority":        ticket.Priority,
		"status":          entity.SupportStatusSubmitted,
	})
	if err != nil {
		return "", fmt.Errorf("can't submit ticket: %w", err)
	}
	return caseNumber, nil
}
