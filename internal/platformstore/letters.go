package platformstore

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// Letter is one platform email (verify / invite / reset). The mail worker renders Template with
// the other fields and sends it to To.
type Letter struct {
	To, Template, Link, Code, TeamName, Slug, From string
}

type letterPayload struct {
	Link     string `json:"link,omitempty"`
	Code     string `json:"code,omitempty"`
	TeamName string `json:"team_name,omitempty"`
	Slug     string `json:"slug,omitempty"`
	From     string `json:"from,omitempty"`
}

// EnqueueLetter queues the letter as pending in platform_email.
func (s *Store) EnqueueLetter(ctx context.Context, l Letter) error {
	to := strings.TrimSpace(l.To)
	if to == "" || !strings.Contains(to, "@") || len(to) > 254 {
		return fmt.Errorf("platformstore: letter: bad recipient")
	}
	switch l.Template {
	case "verify", "invite", "reset":
	default:
		return fmt.Errorf("platformstore: letter: unknown template %q", l.Template)
	}
	payload, err := json.Marshal(letterPayload{Link: l.Link, Code: l.Code, TeamName: l.TeamName, Slug: l.Slug, From: l.From})
	if err != nil {
		return fmt.Errorf("platformstore: letter payload: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO platform_email (to_email, template, payload, status, created_at)
VALUES (?, ?, ?, 'pending', UTC_TIMESTAMP(3))`, to, l.Template, string(payload)); err != nil {
		return fmt.Errorf("platformstore: enqueue letter: %w", err)
	}
	return nil
}
