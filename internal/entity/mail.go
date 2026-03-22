package entity

import (
	"database/sql"
	"time"
)

type SendEmailRequest struct {
	Id               int            `db:"id"`
	From             string         `db:"from_email"`
	To               string         `db:"to_email"`
	Html             string         `db:"html"`
	Subject          string         `db:"subject"`
	ReplyTo          string         `db:"reply_to"`
	Sent             bool           `db:"sent"`
	SentAt           sql.NullTime   `db:"sent_at"`
	CreatedAt        time.Time      `db:"created_at"`
	ErrMsg           sql.NullString `db:"error_msg"`
	SendAttemptCount int            `db:"send_attempt_count"`
	NextRetryAt      sql.NullTime   `db:"next_retry_at"`
}

// SuppressionReason is why an address is suppressed.
type SuppressionReason string

const (
	SuppressionReasonBounce    SuppressionReason = "bounce"
	SuppressionReasonComplaint SuppressionReason = "complaint"
)

type EmailSuppression struct {
	Id        int               `db:"id"`
	Email     string            `db:"email"`
	Reason    SuppressionReason `db:"reason"`
	CreatedAt time.Time         `db:"created_at"`
}

// EmailDailyMetrics holds aggregated email delivery counters for a single day.
type EmailDailyMetrics struct {
	Id              int       `db:"id"`
	Date            time.Time `db:"date"`
	EmailsSent      int       `db:"emails_sent"`
	EmailsDelivered int       `db:"emails_delivered"`
	EmailsBounced   int       `db:"emails_bounced"`
	EmailsOpened    int       `db:"emails_opened"`
	EmailsClicked   int       `db:"emails_clicked"`
	CreatedAt       time.Time `db:"created_at"`
	UpdatedAt       time.Time `db:"updated_at"`
}

// EmailMetricsSummary aggregates email delivery performance for a date range.
type EmailMetricsSummary struct {
	TotalSent      int
	TotalDelivered int
	TotalBounced   int
	TotalOpened    int
	TotalClicked   int
	DeliveryRate   float64
	OpenRate       float64
	ClickRate      float64
	BounceRate     float64
}
