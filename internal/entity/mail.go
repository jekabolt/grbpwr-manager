package entity

import (
	"database/sql"
	"errors"
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
	// Kind is what rendered the row (MailKindShopSignIn / MailKindShopOrder); NULL on storefront rows
	// and on every row written before the column existed.
	Kind sql.NullString `db:"kind"`
	// ExpiresAt is the UTC instant after which the letter is useless (a sign-in code past its TTL).
	ExpiresAt sql.NullTime `db:"expires_at"`
	// IdempotencyKey is the provider idempotency key, fixed when the row is written (AddMail fills
	// it). NULL only on rows written by an older binary.
	IdempotencyKey sql.NullString `db:"idempotency_key"`
	// FirstDispatchAt is when a worker first offered the row to the provider (set just before the
	// call, so a crash after it still leaves the mark).
	FirstDispatchAt sql.NullTime `db:"first_dispatch_at"`
}

// ErrMailClaimLost: the row is no longer leased to the caller's token (its lease ran out and another
// worker took it, or it was settled already). Nothing was written.
var ErrMailClaimLost = errors.New("outbox row is no longer leased to this worker")

// MailOutcome is how a claimed outbox row ends its turn.
type MailOutcome int

const (
	MailSent        MailOutcome = iota + 1 // the provider accepted it (or bulk mail counted as sent)
	MailRetry                              // a transient failure: one attempt counted, retried at NextRetryAt
	MailDead                               // a permanent failure or the last attempt: never retried
	MailQuarantined                        // never sent (stale, foreign kind, ambiguous dispatch)
)

// MailSettle records the outcome of a claimed row. It applies only while the row is still leased to
// Token; otherwise the store returns ErrMailClaimLost.
type MailSettle struct {
	ID      int
	Token   string
	Outcome MailOutcome
	// ErrMsg is the failure (retry, dead) or the quarantine reason.
	ErrMsg      string
	NextRetryAt time.Time // MailRetry
	MaxAttempts int       // MailDead: the attempt count is set to it
	// ProviderID is the provider's id of an accepted letter (MailSent; empty when none came back).
	ProviderID string
	Now        time.Time
}

// Outbox row kinds written by the team-shop mailer. Only these drain on a team-shop lane; anything
// else found in a team-shop outbox (legacy rows, storefront marketing) is quarantined, never sent.
const (
	MailKindShopSignIn = "shop_signin"
	MailKindShopOrder  = "shop_order"
)

// MailClaim asks for up to Limit worker-eligible outbox rows, leased to Token until Now+Lease.
// Kinds, when set, limits the claim to rows of those kinds (a NULL kind never matches). NotBefore,
// when set, leaves out rows created before it (the team-shop cutover). A row past its expires_at is
// never claimed.
type MailClaim struct {
	Limit       int
	MaxAttempts int
	Lease       time.Duration
	Token       string
	Kinds       []string
	NotBefore   time.Time
	Now         time.Time
}

// MailCutover is the team-shop lane's never-send rule: an unsent, unquarantined row is quarantined
// when its kind is not in Kinds (or NULL), it expired by Now, or it was created before OpenedAt.
type MailCutover struct {
	Kinds    []string
	OpenedAt time.Time
	Now      time.Time
	Reason   string
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
