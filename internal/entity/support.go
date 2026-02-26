package entity

import (
	"database/sql"
	"fmt"
	"regexp"
	"strings"
	"time"
)

type SupportTicketStatus string

const (
	SupportStatusSubmitted       SupportTicketStatus = "submitted"
	SupportStatusInProgress      SupportTicketStatus = "in_progress"
	SupportStatusWaitingCustomer SupportTicketStatus = "waiting_customer"
	SupportStatusResolved        SupportTicketStatus = "resolved"
	SupportStatusClosed          SupportTicketStatus = "closed"
)

type SupportTicketPriority string

const (
	PriorityLow    SupportTicketPriority = "low"
	PriorityMedium SupportTicketPriority = "medium"
	PriorityHigh   SupportTicketPriority = "high"
	PriorityUrgent SupportTicketPriority = "urgent"
)

var emailRegex = regexp.MustCompile(`^[a-zA-Z0-9._%+\-]+@[a-zA-Z0-9.\-]+\.[a-zA-Z]{2,}$`)

type SupportTicket struct {
	Id            int                   `db:"id"`
	CaseNumber    string                `db:"case_number"`
	CreatedAt     time.Time             `db:"created_at"`
	UpdatedAt     time.Time             `db:"updated_at"`
	Status        SupportTicketStatus   `db:"status"`
	Priority      SupportTicketPriority `db:"priority"`
	Category      string                `db:"category"`
	ResolvedAt    sql.NullTime          `db:"resolved_at"`
	InternalNotes string                `db:"internal_notes"`
	SupportTicketInsert
}

type SupportTicketInsert struct {
	Topic          string                `db:"topic"`
	Subject        string                `db:"subject"`
	Civility       string                `db:"civility"`
	Email          string                `db:"email"`
	FirstName      string                `db:"first_name"`
	LastName       string                `db:"last_name"`
	OrderReference string                `db:"order_reference"`
	Notes          string                `db:"notes"`
	Category       string                `db:"category"`
	Priority       SupportTicketPriority `db:"priority"`
}

type SupportTicketFilters struct {
	Status         *SupportTicketStatus
	Email          string
	OrderReference string
	Topic          string
	Category       string
	Priority       *SupportTicketPriority
	DateFrom       *time.Time
	DateTo         *time.Time
}

func ValidateSupportTicketInsert(ticket *SupportTicketInsert) error {
	ticket.Email = strings.TrimSpace(ticket.Email)
	ticket.FirstName = strings.TrimSpace(ticket.FirstName)
	ticket.LastName = strings.TrimSpace(ticket.LastName)
	ticket.Subject = strings.TrimSpace(ticket.Subject)
	ticket.Notes = strings.TrimSpace(ticket.Notes)
	ticket.Topic = strings.TrimSpace(ticket.Topic)
	ticket.Category = strings.TrimSpace(ticket.Category)

	if ticket.Email == "" {
		return &ValidationError{Message: "email is required"}
	}
	if !emailRegex.MatchString(ticket.Email) {
		return &ValidationError{Message: "invalid email format"}
	}
	if ticket.FirstName == "" {
		return &ValidationError{Message: "first name is required"}
	}
	if ticket.LastName == "" {
		return &ValidationError{Message: "last name is required"}
	}
	if ticket.Subject == "" {
		return &ValidationError{Message: "subject is required"}
	}
	if ticket.Notes == "" {
		return &ValidationError{Message: "notes are required"}
	}

	if len(ticket.Subject) > 200 {
		return &ValidationError{Message: "subject must not exceed 200 characters"}
	}
	if len(ticket.Notes) > 5000 {
		return &ValidationError{Message: "notes must not exceed 5000 characters"}
	}
	if len(ticket.Topic) > 100 {
		return &ValidationError{Message: "topic must not exceed 100 characters"}
	}
	if len(ticket.Category) > 100 {
		return &ValidationError{Message: "category must not exceed 100 characters"}
	}

	if ticket.Priority == "" {
		ticket.Priority = PriorityMedium
	}

	validPriorities := map[SupportTicketPriority]bool{
		PriorityLow:    true,
		PriorityMedium: true,
		PriorityHigh:   true,
		PriorityUrgent: true,
	}
	if !validPriorities[ticket.Priority] {
		return &ValidationError{Message: fmt.Sprintf("invalid priority: %s", ticket.Priority)}
	}

	return nil
}
