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
	// DeliverySoftBounce: a transient or undetermined bounce (mailbox full, greylisting). Not on the
	// list, not counted by the sign-up breaker, not «bounced» on an invite.
	DeliverySoftBounce = "soft_bounce"
	// DeliveryFailed: Resend could not send (email.failed: our side, a bad key, a quota). Same.
	DeliveryFailed = "failed"
)

// hardDelivery: a final bad outcome that a later, milder event never overwrites.
func hardDelivery(d string) bool { return d == DeliveryBounced || d == DeliveryComplained }

// SuppressionApplies reports whether a letter of this kind honours the suppression list: only the
// letters that go to an address somebody typed (a sign-up's confirm letter, an invite). Letters to
// an existing person's own confirmed address (reset, restore, account deleted, team letters) are
// always sent, so nobody can be locked out of their own password reset through the list.
func SuppressionApplies(kind string) bool {
	return kind == KindVerify || kind == KindJoinCode || kind == KindInvite
}

// maxEventBody bounds a webhook body (Resend events are a few KB).
const maxEventBody = 256 << 10

// EventHandler receives Resend's delivery events for the platform letters
// (POST /api/webhooks/platform-mail). Only a Svix-signed body is read; an event is applied only to
// a letter this install sent (matched by Resend's email id) and only when its recipient is that
// letter's address, so an event of another product on a shared Resend account changes nothing.
type EventHandler struct {
	db  *sqlx.DB // nil: a shop-only endpoint (NewForeignEventHandler)
	wh  *svix.Webhook
	now func() time.Time
	// foreign, optional, takes events for letters that are not platform letters (team shops' letters
	// over the same Resend account).
	foreign ForeignLetters
}

// Event is one signed Resend delivery event as the handlers read it.
type Event struct {
	Type    string
	EmailID string
	To      []string
	Bounce  string // Permanent | Transient | Undetermined
	At      time.Time
}

// SuppressionReason is the list reason the event asks for: ReasonBounce for a permanent bounce or
// Resend's own suppression, ReasonComplaint for a complaint, "" for anything else.
func (e Event) SuppressionReason() string {
	_, reason, _ := classify(e.Type, e.Bounce)
	return reason
}

// ForeignLetters takes a signed event of a letter this install sent outside platform_email (a team
// shop's letter). It must check the letter and its recipient itself; an error makes Resend retry.
type ForeignLetters func(ctx context.Context, ev Event) error

// SetForeignLetters routes events of letters that are not platform letters to f.
func (h *EventHandler) SetForeignLetters(f ForeignLetters) { h.foreign = f }

// NewForeignEventHandler builds a handler for an endpoint that carries only foreign letters (the team
// shops' own Resend account, POST /api/webhooks/shop-mail). Signed like the platform one.
func NewForeignEventHandler(secret string, f ForeignLetters) (*EventHandler, error) {
	if f == nil {
		return nil, errors.New("platformmail: events: no letter route")
	}
	wh, err := webhookVerifier(secret)
	if err != nil {
		return nil, err
	}
	return &EventHandler{wh: wh, now: time.Now, foreign: f}, nil
}

// NewEventHandler builds the handler. An empty secret is an error: the route must not be mounted
// unsigned (anyone could suppress a person's address).
func NewEventHandler(db *sqlx.DB, secret string) (*EventHandler, error) {
	if db == nil {
		return nil, errors.New("platformmail: events: db is required")
	}
	wh, err := webhookVerifier(secret)
	if err != nil {
		return nil, err
	}
	return &EventHandler{db: db, wh: wh, now: time.Now}, nil
}

func webhookVerifier(secret string) (*svix.Webhook, error) {
	secret = strings.TrimSpace(secret)
	if secret == "" {
		return nil, errors.New("platformmail: events: no webhook secret")
	}
	wh, err := svix.NewWebhook(secret)
	if err != nil {
		return nil, fmt.Errorf("platformmail: events: bad webhook secret: %w", err)
	}
	return wh, nil
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

// classify maps an event to a delivery outcome and a suppression reason; ok=false for a type this
// handler does not read.
func classify(typ, bounce string) (delivery, reason string, ok bool) {
	switch typ {
	case "email.delivered":
		return DeliveryDelivered, "", true
	case "email.delivery_delayed":
		return DeliveryDelayed, "", true
	case "email.bounced":
		if strings.EqualFold(bounce, "Permanent") {
			return DeliveryBounced, ReasonBounce, true
		}
		return DeliverySoftBounce, "", true
	case "email.suppressed":
		// Resend refused the send itself: the address is on its own list after an earlier bounce or
		// complaint (possibly from another product of the account).
		return DeliveryBounced, ReasonBounce, true
	case "email.failed":
		return DeliveryFailed, "", true
	case "email.complained":
		return DeliveryComplained, ReasonComplaint, true
	}
	return "", "", false
}

// Apply records one event. Unknown types and events for letters this install did not send are
// ignored (nil). Repeating an event writes the same state. An event of a letter that is not a
// platform letter goes to the foreign route when one is set.
func (h *EventHandler) Apply(ctx context.Context, typ, emailID string, to []string, bounce string, at time.Time) error {
	delivery, reason, ok := classify(typ, bounce)
	if !ok {
		return nil
	}
	emailID = strings.TrimSpace(emailID)
	if emailID == "" || len(emailID) > 64 {
		return nil
	}
	foreign := func() error {
		if h.foreign == nil {
			return nil
		}
		return h.foreign(ctx, Event{Type: typ, EmailID: emailID, To: to, Bounce: bounce, At: at})
	}
	if h.db == nil {
		return foreign()
	}
	var row struct {
		ID       int64   `db:"id"`
		To       string  `db:"to_email"`
		Delivery *string `db:"delivery"`
	}
	err := h.db.GetContext(ctx, &row, `SELECT id, to_email, delivery FROM platform_email WHERE provider_id = ? ORDER BY id LIMIT 1`, emailID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return foreign() // not a platform letter
		}
		return fmt.Errorf("find letter: %w", err)
	}
	if !recipientMatches(row.To, to) {
		return nil
	}
	// A late milder event (delivered, delayed, a soft bounce, failed) never overwrites a permanent
	// bounce or complaint already recorded.
	if row.Delivery != nil && hardDelivery(*row.Delivery) && !hardDelivery(delivery) {
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
