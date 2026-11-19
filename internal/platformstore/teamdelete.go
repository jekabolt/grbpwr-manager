package platformstore

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/tenant"
	"github.com/jmoiron/sqlx"
)

// DeletedTeam is a committed DeleteTeam: the team's address and name as the transaction locked
// them (a rename that committed just before is in them), when its data is wiped, and the team
// sessions it revoked, which the caller drops from its in-process session cache.
type DeletedTeam struct {
	Slug, Name string
	PurgeAt    time.Time
	SIDs       []string
}

// Lock order of the control plane (teams D1b). Every transaction takes its locks in this order and
// never asks again for a row it has passed:
//
//  1. the team row, always first. FOR UPDATE where the team itself changes (DeleteTeam,
//     RestoreTeam, SetTeamReadOnly, RenameTeam, ApproveTeam, DeclineTeam, UpsertInvite, ClaimSpare,
//     the pending sweep, EnsureTeamOne); FOR SHARE where something is created that lives only
//     while the team is usable (CreateTeamSession, IssueHandoff — member and operator entry —,
//     ExchangeHandoff, ExchangeOperatorHandoff, both commits of AcceptInvite), so sign-ins of one
//     team do not queue behind each other, but none of them can start or finish across a status
//     change: a session, code or membership made after a delete committed sees status deleted
//     and is refused, and DeleteTeam revokes whatever committed before it;
//  2. the account (platform_user);
//  3. the membership (team_member);
//  4. sessions (user_session);
//  5. invites (team_invite);
//  6. tokens and hand-off codes (email_token).
//
// For one team the team row serialises every pair of team-locking transactions, so what follows
// it cannot close a cycle between them. Transactions that never lock the team row (member change,
// password change, invite resend / revoke, sign-out) keep 2–6. Two known exceptions, both safe:
// AcceptInvite locks its invite right after the team row, before the account and the membership
// (it learns the account from the invite; nothing that holds an account, membership or session
// asks for an invite after it); the sign-up confirm locks its token before its team, which is
// still pending then (DeleteTeam stops at the status check before it touches a token).

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
		var cur struct {
			Slug   string `db:"slug"`
			Name   string `db:"name"`
			Status string `db:"status"`
		}
		if err := tx.GetContext(ctx, &cur, `SELECT slug, name, status FROM team WHERE id = ? FOR UPDATE`, teamID); err != nil {
			if isNoRows(err) {
				return ErrTeamNotFound
			}
			return fmt.Errorf("platformstore: lock team: %w", err)
		}
		if teamID == tenant.TeamOneID {
			return ErrWrongTeamStatus
		}
		switch cur.Status {
		case "active", "suspended":
		default:
			return ErrWrongTeamStatus
		}
		res.Slug, res.Name = cur.Slug, cur.Name
		if _, err := tx.ExecContext(ctx, `UPDATE team SET status = 'deleted', deleted_at = ?, purge_at = ?, deleted_from = ?
WHERE id = ?`, now, res.PurgeAt, cur.Status, teamID); err != nil {
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

// RestoredTeam is a committed RestoreTeam: the team's address, name and status as the transaction
// locked and set them.
type RestoredTeam struct {
	Slug, Name, Status string
}

// RestoreTeam brings a deleted team back to the status it had before (deleted_from) while its wipe
// is still ahead (purge_at > now); deleted_at, purge_at and deleted_from go back to NULL. Sessions
// and invites stay revoked: people sign in again. Any other status, or a team whose wipe is due:
// ErrWrongTeamStatus. The directory entry is dropped.
func (s *Store) RestoreTeam(ctx context.Context, teamID int64, now time.Time) (RestoredTeam, error) {
	var res RestoredTeam
	err := s.inTx(ctx, func(tx *sqlx.Tx) error {
		var cur struct {
			Slug        string         `db:"slug"`
			Name        string         `db:"name"`
			Status      string         `db:"status"`
			PurgeAt     sql.NullTime   `db:"purge_at"`
			DeletedFrom sql.NullString `db:"deleted_from"`
		}
		if err := tx.GetContext(ctx, &cur, `SELECT slug, name, status, purge_at, deleted_from FROM team WHERE id = ? FOR UPDATE`,
			teamID); err != nil {
			if isNoRows(err) {
				return ErrTeamNotFound
			}
			return fmt.Errorf("platformstore: lock team: %w", err)
		}
		if cur.Status != "deleted" || !cur.PurgeAt.Valid || !cur.PurgeAt.Time.After(now.UTC()) {
			return ErrWrongTeamStatus
		}
		back := cur.DeletedFrom.String
		if back == "" {
			back = "suspended"
		}
		if _, err := tx.ExecContext(ctx, `UPDATE team SET status = ?, deleted_at = NULL, purge_at = NULL, deleted_from = NULL
WHERE id = ?`, back, teamID); err != nil {
			return fmt.Errorf("platformstore: restore team: %w", err)
		}
		res = RestoredTeam{Slug: cur.Slug, Name: cur.Name, Status: back}
		return nil
	})
	s.dir.Invalidate(teamID)
	if err != nil {
		return RestoredTeam{}, err
	}
	return res, nil
}

// shareTeamTx takes the team row FOR SHARE, the first lock of every transaction that creates a
// session, a hand-off code or a membership (lock order at DeleteTeam), and returns its status;
// "" when there is no such team.
func shareTeamTx(ctx context.Context, tx *sqlx.Tx, teamID int64) (string, error) {
	var sts []string
	if err := tx.SelectContext(ctx, &sts, `SELECT status FROM team WHERE id = ? FOR SHARE`, teamID); err != nil {
		return "", fmt.Errorf("platformstore: share team: %w", err)
	}
	if len(sts) == 0 {
		return "", nil
	}
	return sts[0], nil
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
