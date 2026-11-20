package platformstore

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jmoiron/sqlx"
)

// SignupLetterStats is what the sign-up breaker reads (X6): confirm letters written in the last
// hour, and, over the last 24 hours, confirm letters that left and how many of them bounced
// permanently or drew a spam complaint. Invite letters are not counted: a team owner could feed
// the share with made-up addresses (they have their own per-team cap); soft bounces and send
// failures are not counted either.
type SignupLetterStats struct {
	VerifyLastHour int `db:"verify_hour"`
	Sent24h        int `db:"sent_day"`
	Bounced24h     int `db:"bounced_day"`
}

// SignupLetterStats counts from platform_email (idx_platform_email_template_created), so every
// replica sees the same numbers.
func (s *Store) SignupLetterStats(ctx context.Context, now time.Time) (SignupLetterStats, error) {
	var st SignupLetterStats
	hour, day := now.Add(-time.Hour), now.Add(-24*time.Hour)
	if err := s.db.GetContext(ctx, &st, `SELECT
  COALESCE(SUM(created_at >= ?), 0) AS verify_hour,
  COALESCE(SUM(status = 'sent'), 0) AS sent_day,
  COALESCE(SUM(status = 'sent' AND delivery IN ('bounced', 'complained')), 0) AS bounced_day
FROM platform_email WHERE template IN ('verify','join_code') AND created_at >= ?`, hour, day); err != nil {
		return st, fmt.Errorf("platformstore: sign-up letter stats: %w", err)
	}
	return st, nil
}

// TeamLetterCap bounds one team's invite letters (invites and re-sends together). Zero = no cap.
type TeamLetterCap struct {
	PerHour, PerDay int
}

// teamLetterCapTx refuses with ErrTeamLetterCap when the team's invite letters of the last hour or
// day reached the cap.
func teamLetterCapTx(ctx context.Context, tx *sqlx.Tx, teamID int64, now time.Time, c TeamLetterCap) error {
	if c.PerHour <= 0 && c.PerDay <= 0 {
		return nil
	}
	var n struct {
		Hour int `db:"hour"`
		Day  int `db:"day"`
	}
	if err := tx.GetContext(ctx, &n, `SELECT COALESCE(SUM(created_at > ?), 0) AS hour, COUNT(*) AS day
FROM platform_email WHERE team_id = ? AND template = 'invite' AND created_at > ?`,
		now.Add(-time.Hour), teamID, now.Add(-24*time.Hour)); err != nil {
		return fmt.Errorf("platformstore: count team letters: %w", err)
	}
	if (c.PerHour > 0 && n.Hour >= c.PerHour) || (c.PerDay > 0 && n.Day >= c.PerDay) {
		return ErrTeamLetterCap
	}
	return nil
}

// UnblockMailAddress takes an address off the suppression list (the mail-unblock command). It
// reports whether the address was on it. The address matches as typed or lowercased.
func (s *Store) UnblockMailAddress(ctx context.Context, email string) (bool, error) {
	email = strings.TrimSpace(email)
	if email == "" {
		return false, fmt.Errorf("platformstore: unblock: empty address")
	}
	r, err := s.db.ExecContext(ctx, `DELETE FROM platform_mail_suppression WHERE email IN (?, ?)`, email, strings.ToLower(email))
	if err != nil {
		return false, fmt.Errorf("platformstore: unblock: %w", err)
	}
	n, err := r.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("platformstore: unblock: %w", err)
	}
	return n > 0, nil
}
