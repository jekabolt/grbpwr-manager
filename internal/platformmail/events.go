package platformmail

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	svix "github.com/svix/svix-webhooks/go"

	"github.com/jmoiron/sqlx"
)

// SuppressedError prefixes platform_email.last_error of a letter the worker refused because its
// recipient is on platform_mail_suppression ("suppressed: bounce" / "suppressed: complaint").
const SuppressedError = "suppressed: "

// Suppression reasons (platform_mail_suppression.reason).
const (
	ReasonBounce    = "bounce"
	ReasonComplaint = "complaint"
)

// Delivery outcomes (platform_email.delivery).
const (
	DeliveryDelivered  = "delivered"
	DeliveryBounced    = "bounced"
	DeliveryComplained = "complained"
	DeliveryDelayed    = "delayed"
)

// SuppressionApplies reports whether a letter of this kind honours the suppression list: only the
// letters that go to an address somebody typed (a sign-up's confirm letter, an invite). Letters to
// an existing person's own confirmed address (reset, restore, account deleted, team letters) are
// always sent, so nobody can be locked out of their own password reset through the list.
func SuppressionApplies(kind string) bool { return kind == KindVerify || kind == KindInvite }

// maxEventBody bounds a webhook body (Resend events are a few KB).
const maxEventBody = 256 << 10

// EventHandler receives Resend's delivery events for the platform letters
// (POST /api/webhooks/platform-mail). Only a Svix-signed body is read; an event is applied only to
// a letter this install sent (matched by Resend's email id) and only when its recipient is that
// letter's address, so an event of another product on a shared Resend account changes nothing.
type EventHandler struct {
	db  *sqlx.DB
	wh  *svix.Webhook
	now func() time.Time
}

// NewEventHandler builds the handler. An empty secret is an error: the route must not be mounted
// unsigned (anyone could suppress a person's address).
func NewEventHandler(db *sqlx.DB, secret string) (*EventHandler, error) {
	if db == nil {
		return nil, errors.New("platformmail: events: db is required")
	}
	secret = strings.TrimSpace(secret)
	if secret == "" {
		return nil, errors.New("platformmail: events: no webhook secret")
	}
	wh, err := svix.NewWebhook(secret)
	if err != nil {
		return nil, fmt.Errorf("platformmail: events: bad webhook secret: %w", err)
	}
	return &EventHandler{db: db, wh: wh, now: time.Now}, nil
}

// event is the part of a Resend webhook body this handler reads.
type event struct {
	Type      string `json:"type"`
	CreatedAt string `json:"created_at"`
	Data      struct {
		EmailID string   `json:"email_id"`
		To      []string `json:"to"`
		Bounce  *struct {
			Type string `json:"type"` // Permanent | Transient | Undetermined
		} `json:"bounce,omitempty"`
	} `json:"data"`
}

func (h *EventHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	ctx := r.Context()
	body, err := io.ReadAll(io.LimitReader(r.Body, maxEventBody+1))
	if err != nil || len(body) > maxEventBody {
		http.Error(w, "bad body", http.StatusBadRequest)
		return
	}
	// Svix checks the HMAC over id.timestamp.body and refuses a timestamp more than 5 minutes off:
	// a captured event cannot be replayed later.
	if err := h.wh.Verify(body, r.Header); err != nil {
		slog.Default().WarnContext(ctx, "platformmail: mail event signature refused")
		http.Error(w, "invalid signature", http.StatusUnauthorized)
		return
	}
	var ev event
	if err := json.Unmarshal(body, &ev); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	if err := h.Apply(ctx, ev.Type, ev.Data.EmailID, ev.Data.To, bounceType(ev), h.eventTime(ev.CreatedAt)); err != nil {
		slog.Default().ErrorContext(ctx, "platformmail: apply mail event", slog.String("type", ev.Type), slog.String("err", err.Error()))
		// 500: Resend retries the event later.
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func bounceType(ev event) string {
	if ev.Data.Bounce == nil {
		return ""
	}
	return ev.Data.Bounce.Type
}

// eventTime is the event's own time, or now when it is missing, unreadable or in the future.
func (h *EventHandler) eventTime(s string) time.Time {
	now := h.now().UTC().Truncate(time.Millisecond)
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil || t.After(now) {
		return now
	}
	return t.UTC().Truncate(time.Millisecond)
}

// Apply records one event. Unknown types and events for letters this install did not send are
// ignored (nil). Repeating an event writes the same state.
func (h *EventHandler) Apply(ctx context.Context, typ, emailID string, to []string, bounce string, at time.Time) error {
	var delivery, reason string
	switch typ {
	case "email.delivered":
		delivery = DeliveryDelivered
	case "email.delivery_delayed":
		delivery = DeliveryDelayed
	case "email.bounced":
		delivery = DeliveryBounced
		if strings.EqualFold(bounce, "Permanent") {
			reason = ReasonBounce
		}
	case "email.suppressed":
		// Resend refused the send itself: the address is on its own list after an earlier bounce or
		// complaint (possibly from another product of the account).
		delivery, reason = DeliveryBounced, ReasonBounce
	case "email.failed":
		delivery = DeliveryBounced
	case "email.complained":
		delivery, reason = DeliveryComplained, ReasonComplaint
	default:
		return nil
	}
	emailID = strings.TrimSpace(emailID)
	if emailID == "" || len(emailID) > 64 {
		return nil
	}
	var row struct {
		ID       int64   `db:"id"`
		To       string  `db:"to_email"`
		Delivery *string `db:"delivery"`
	}
	err := h.db.GetContext(ctx, &row, `SELECT id, to_email, delivery FROM platform_email WHERE provider_id = ? ORDER BY id LIMIT 1`, emailID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil // not our letter
		}
		return fmt.Errorf("find letter: %w", err)
	}
	if !recipientMatches(row.To, to) {
		return nil
	}
	// A late "delivered" or "delayed" never overwrites a bounce or complaint already recorded.
	if row.Delivery != nil && (delivery == DeliveryDelivered || delivery == DeliveryDelayed) &&
		(*row.Delivery == DeliveryBounced || *row.Delivery == DeliveryComplained) {
		delivery = *row.Delivery
	}
	if row.Delivery != nil && *row.Delivery == DeliveryComplained && delivery == DeliveryBounced {
		delivery = DeliveryComplained
	}
	if _, err := h.db.ExecContext(ctx, `UPDATE platform_email SET delivery = ?, delivery_at = ? WHERE id = ?`, delivery, at, row.ID); err != nil {
		return fmt.Errorf("record delivery: %w", err)
	}
	if reason == "" {
		return nil
	}
	// A complaint outranks a bounce; the first time stays.
	if _, err := h.db.ExecContext(ctx, `INSERT INTO platform_mail_suppression (email, reason, first_at, last_at, events)
VALUES (?, ?, ?, ?, 1)
ON DUPLICATE KEY UPDATE events = events + 1, last_at = GREATEST(last_at, VALUES(last_at)),
  reason = IF(VALUES(reason) = 'complaint', 'complaint', reason)`, row.To, reason, at, at); err != nil {
		return fmt.Errorf("suppress: %w", err)
	}
	slog.Default().InfoContext(ctx, "platformmail: address suppressed", slog.String("reason", reason), slog.Int64("letter", row.ID))
	return nil
}

// recipientMatches: the event names the letter's own address (case-insensitively; letters go to
// one recipient).
func recipientMatches(letterTo string, to []string) bool {
	for _, t := range to {
		if strings.EqualFold(strings.TrimSpace(t), letterTo) {
			return true
		}
	}
	return false
}
