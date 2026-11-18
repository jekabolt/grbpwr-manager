package platformstore

import (
	"context"
	"fmt"
	"time"
)

// TeamSession reads the live tuple behind a team token in one query: the session sid belongs to
// user uid in team tid with aud team and is not revoked, the platform user is active, and the
// membership (tid, uid) is active. ok=false when any part is missing; role is the membership role.
func (s *Store) TeamSession(ctx context.Context, sid string, uid, tid int64) (string, bool, error) {
	if sid == "" || uid <= 0 || tid <= 0 {
		return "", false, nil
	}
	var roles []string
	err := s.db.SelectContext(ctx, &roles, `
SELECT m.role
FROM user_session s
JOIN platform_user u ON u.id = s.user_id
JOIN team_member m ON m.team_id = s.team_id AND m.user_id = s.user_id
WHERE s.sid = ? AND s.user_id = ? AND s.team_id = ? AND s.aud = 'team' AND s.revoked_at IS NULL
  AND u.status = 'active' AND m.status = 'active'`, sid, uid, tid)
	if err != nil {
		return "", false, fmt.Errorf("platformstore: team session: %w", err)
	}
	if len(roles) == 0 {
		return "", false, nil
	}
	return roles[0], true, nil
}

// RevokeTeamSessions revokes every live team session of user uid in team tid and returns their
// sids, so the caller can drop them from the edge cache (tenantbind.Binder.InvalidateSession).
// Member removal and demotion call it in the same transaction as the membership change.
func (s *Store) RevokeTeamSessions(ctx context.Context, uid, tid int64, now time.Time) ([]string, error) {
	var sids []string
	if err := s.db.SelectContext(ctx, &sids,
		`SELECT sid FROM user_session WHERE user_id = ? AND team_id = ? AND revoked_at IS NULL`, uid, tid); err != nil {
		return nil, fmt.Errorf("platformstore: list team sessions: %w", err)
	}
	if len(sids) == 0 {
		return nil, nil
	}
	if _, err := s.db.ExecContext(ctx,
		`UPDATE user_session SET revoked_at = ? WHERE user_id = ? AND team_id = ? AND revoked_at IS NULL`,
		now.UTC(), uid, tid); err != nil {
		return nil, fmt.Errorf("platformstore: revoke team sessions: %w", err)
	}
	return sids, nil
}
