package platformstore

import (
	"context"
	"fmt"
	"time"
)

// SignupLetterStats is what the sign-up breaker reads (X6): confirm letters written in the last
// hour, and, over the last 24 hours, letters to typed addresses (verify, invite) that left and how
// many of them bounced or drew a spam complaint.
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
  COALESCE(SUM(template = 'verify' AND created_at >= ?), 0) AS verify_hour,
  COALESCE(SUM(status = 'sent'), 0) AS sent_day,
  COALESCE(SUM(status = 'sent' AND delivery IN ('bounced', 'complained')), 0) AS bounced_day
FROM platform_email WHERE template IN ('verify', 'invite') AND created_at >= ?`, hour, day); err != nil {
		return st, fmt.Errorf("platformstore: sign-up letter stats: %w", err)
	}
	return st, nil
}
