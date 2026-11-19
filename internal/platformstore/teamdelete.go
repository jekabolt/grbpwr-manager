package platformstore

import (
	"context"
	"fmt"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/tenant"
	"github.com/jmoiron/sqlx"
)

// DeletedTeam is a committed DeleteTeam: when the team's data is wiped and the team sessions it
// revoked, which the caller drops from its in-process session cache.
type DeletedTeam struct {
	PurgeAt time.Time
	SIDs    []string
}

// DeleteTeam hides a team (teams D1): in one transaction the team row is locked, the status goes
// active / suspended → deleted with deleted_at = now, purge_at = now + retention and deleted_from =
// the old status; every live session of the team is revoked, its open invites revoked and its
// unexchanged hand-off codes consumed. Membership rows stay (RestoreTeam needs them); the slug stays
// taken until the wipe. Team #1 and any other status: ErrWrongTeamStatus. The directory entry is
// dropped.
func (s *Store) DeleteTeam(ctx context.Context, teamID int64, now time.Time, retention time.Duration) (DeletedTeam, error) {
	now = now.UTC()
	res := DeletedTeam{PurgeAt: now.Add(retention)}
	err := s.inTx(ctx, func(tx *sqlx.Tx) error {
		var st string
		if err := tx.GetContext(ctx, &st, `SELECT status FROM team WHERE id = ? FOR UPDATE`, teamID); err != nil {
			if isNoRows(err) {
				return ErrTeamNotFound
			}
			return fmt.Errorf("platformstore: lock team: %w", err)
		}
		if teamID == tenant.TeamOneID {
			return ErrWrongTeamStatus
		}
		switch st {
		case "active", "suspended":
		default:
			return ErrWrongTeamStatus
		}
		if _, err := tx.ExecContext(ctx, `UPDATE team SET status = 'deleted', deleted_at = ?, purge_at = ?, deleted_from = ?
WHERE id = ?`, now, res.PurgeAt, st, teamID); err != nil {
			return fmt.Errorf("platformstore: delete team: %w", err)
		}
		sids, err := revokeAllTeamSessionsTx(ctx, tx, teamID, now)
		if err != nil {
			return err
		}
		res.SIDs = sids
		if _, err := tx.ExecContext(ctx, `UPDATE team_invite SET revoked_at = ?
WHERE team_id = ? AND accepted_at IS NULL AND revoked_at IS NULL`, now, teamID); err != nil {
			return fmt.Errorf("platformstore: revoke team invites: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE email_token SET used_at = ?
WHERE team_id = ? AND purpose = 'handoff' AND used_at IS NULL`, now, teamID); err != nil {
			return fmt.Errorf("platformstore: expire team hand-offs: %w", err)
		}
		return nil
	})
	s.dir.Invalidate(teamID)
	if err != nil {
		return DeletedTeam{}, err
	}
	return res, nil
}

// RestoreTeam brings a deleted team back to the status it had before (deleted_from) while its wipe
// is still ahead (purge_at > now); deleted_at, purge_at and deleted_from go back to NULL. Sessions
// and invites stay revoked: people sign in again. Any other status, or a team whose wipe is due:
// ErrWrongTeamStatus. The directory entry is dropped.
func (s *Store) RestoreTeam(ctx context.Context, teamID int64, now time.Time) error {
	err := s.inTx(ctx, func(tx *sqlx.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE team SET status = COALESCE(deleted_from, 'suspended'),
 deleted_at = NULL, purge_at = NULL, deleted_from = NULL
WHERE id = ? AND status = 'deleted' AND purge_at IS NOT NULL AND purge_at > ?`, teamID, now.UTC())
		if err != nil {
			return fmt.Errorf("platformstore: restore team: %w", err)
		}
		if n, _ := res.RowsAffected(); n == 1 {
			return nil
		}
		var exists int
		if err := tx.GetContext(ctx, &exists, `SELECT COUNT(*) FROM team WHERE id = ?`, teamID); err != nil {
			return fmt.Errorf("platformstore: restore team: %w", err)
		}
		if exists == 0 {
			return ErrTeamNotFound
		}
		return ErrWrongTeamStatus
	})
	s.dir.Invalidate(teamID)
	return err
}

// revokeAllTeamSessionsTx revokes every live session of the team (members and operators alike) and
// returns the sids.
func revokeAllTeamSessionsTx(ctx context.Context, tx *sqlx.Tx, teamID int64, now time.Time) ([]string, error) {
	var sids []string
	if err := tx.SelectContext(ctx, &sids, `SELECT sid FROM user_session
WHERE team_id = ? AND revoked_at IS NULL FOR UPDATE`, teamID); err != nil {
		return nil, fmt.Errorf("platformstore: list team sessions: %w", err)
	}
	if len(sids) > 0 {
		if _, err := tx.ExecContext(ctx, `UPDATE user_session SET revoked_at = ?
WHERE team_id = ? AND revoked_at IS NULL`, now.UTC(), teamID); err != nil {
			return nil, fmt.Errorf("platformstore: revoke team sessions: %w", err)
		}
	}
	return sids, nil
}
