package platformstore

import (
	"context"
	"fmt"
)

// SessionActive reports whether the session exists and is not revoked. An unknown sid is not
// active (a token whose session row is gone is refused).
func (s *Store) SessionActive(ctx context.Context, sid string) (bool, error) {
	if sid == "" {
		return false, nil
	}
	var n int
	if err := s.db.GetContext(ctx, &n,
		`SELECT COUNT(*) FROM user_session WHERE sid = ? AND revoked_at IS NULL`, sid); err != nil {
		return false, fmt.Errorf("platformstore: session: %w", err)
	}
	return n > 0, nil
}
