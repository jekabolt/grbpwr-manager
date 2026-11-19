package platformstore

import (
	"context"
	"fmt"
)

// First entry (P0 §4): team_member.entry_pending is 1 from the moment a membership starts (an invite
// accepted, a team created, a sign-up confirmed) until the member finishes or skips the welcome
// screen. Memberships that existed before it stay 0.

// EntryPending reports whether the active membership of userID in teamID still owes its first entry.
// false when there is no such membership.
func (s *Store) EntryPending(ctx context.Context, teamID, userID int64) (bool, error) {
	if teamID <= 0 || userID <= 0 {
		return false, nil
	}
	var flags []bool
	if err := s.db.SelectContext(ctx, &flags, `SELECT entry_pending FROM team_member
WHERE team_id = ? AND user_id = ? AND status = 'active'`, teamID, userID); err != nil {
		return false, fmt.Errorf("platformstore: entry pending: %w", err)
	}
	return len(flags) == 1 && flags[0], nil
}

// FinishTeamEntry clears the first-entry flag of userID's active membership in teamID. Idempotent: a
// membership already finished, or none, changes nothing.
func (s *Store) FinishTeamEntry(ctx context.Context, teamID, userID int64) error {
	if _, err := s.db.ExecContext(ctx, `UPDATE team_member SET entry_pending = 0
WHERE team_id = ? AND user_id = ? AND status = 'active' AND entry_pending = 1`, teamID, userID); err != nil {
		return fmt.Errorf("platformstore: finish team entry: %w", err)
	}
	return nil
}

// TeamSessionChain is TeamSession for the account RPCs (P0 §1.4): the team session sid of uid in tid
// is live (unrevoked, account active, membership active, any role, any team status) and its chain is
// the root session it was opened from, or the session itself when it has none.
func (s *Store) TeamSessionChain(ctx context.Context, sid string, uid, tid int64) (string, bool, error) {
	if sid == "" || uid <= 0 || tid <= 0 {
		return "", false, nil
	}
	var chains []string
	if err := s.db.SelectContext(ctx, &chains, `
SELECT COALESCE(s.parent_sid, s.sid)
FROM user_session s
JOIN platform_user u ON u.id = s.user_id
JOIN team_member m ON m.team_id = s.team_id AND m.user_id = s.user_id
WHERE s.sid = ? AND s.user_id = ? AND s.team_id = ? AND s.aud = 'team' AND s.revoked_at IS NULL
  AND u.status = 'active' AND m.status = 'active'`, sid, uid, tid); err != nil {
		return "", false, fmt.Errorf("platformstore: team session chain: %w", err)
	}
	if len(chains) == 0 {
		return "", false, nil
	}
	return chains[0], true, nil
}
