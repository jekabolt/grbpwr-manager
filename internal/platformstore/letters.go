package platformstore

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Letter is one platform email (verify / invite / reset / restore / account_deleted). The mail
// worker renders Template with the other fields and sends it to To. Until and Deleted are dates for
// the account letters: the end of the restore window and when the account was deleted (only the
// restore letter a sign-up asked for carries Deleted).
type Letter struct {
	To, Template, Link, Code, TeamName, Slug, From string
	Until, Deleted                                 time.Time
	// TeamID, InviteID: set on invite letters (X6: per-team cap, the invite's own delivery state).
	// A join_code also carries TeamID for outbox attribution, but never in its email_token row.
	TeamID, InviteID int64
}

type letterPayload struct {
	Link     string     `json:"link,omitempty"`
	Code     string     `json:"code,omitempty"`
	TeamName string     `json:"team_name,omitempty"`
	Slug     string     `json:"slug,omitempty"`
	From     string     `json:"from,omitempty"`
	Until    *time.Time `json:"until,omitempty"`
	Deleted  *time.Time `json:"deleted,omitempty"`
}

// payloadOf is the outbox payload of a letter.
func payloadOf(l Letter) letterPayload {
	p := letterPayload{Link: l.Link, Code: l.Code, TeamName: l.TeamName, Slug: l.Slug, From: l.From}
	if !l.Until.IsZero() {
		t := l.Until.UTC()
		p.Until = &t
	}
	if !l.Deleted.IsZero() {
		t := l.Deleted.UTC()
		p.Deleted = &t
	}
	return p
}

// EnqueueLetter queues the letter as pending in platform_email.
func (s *Store) EnqueueLetter(ctx context.Context, l Letter) error {
	to := strings.TrimSpace(l.To)
	if to == "" || !strings.Contains(to, "@") || len(to) > 254 {
		return fmt.Errorf("platformstore: letter: bad recipient")
	}
	switch l.Template {
	case "verify", "join_code", "invite", "reset":
	default:
		return fmt.Errorf("platformstore: letter: unknown template %q", l.Template)
	}
	payload, err := json.Marshal(payloadOf(l))
	if err != nil {
		return fmt.Errorf("platformstore: letter payload: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO platform_email (to_email, template, payload, status, created_at)
VALUES (?, ?, ?, 'pending', UTC_TIMESTAMP(3))`, to, l.Template, string(payload)); err != nil {
		return fmt.Errorf("platformstore: enqueue letter: %w", err)
	}
	return nil
}
