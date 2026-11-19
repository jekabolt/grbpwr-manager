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
//     the pending sweep, EnsureTeamOne, SetTeamLogo, SetTeamBrand) or where a status read must hold
//     until commit (RevokeMemberSessions: a member's rights changed); FOR SHARE where something is created that lives only
//     while the team is usable (CreateTeamSession, IssueHandoff — member and operator entry —,
//     ExchangeHandoff, ExchangeOperatorHandoff, both commits of AcceptInvite), so sign-ins of one
//     team do not queue behind each other, but none of them can start or finish across a status
//     change: a session, code or membership made after a delete committed sees status deleted
//     and is refused, and DeleteTeam revokes whatever committed before it; CreateTeam (root
//     session) and SignUp lock only an expired pending team that holds the address they want (FOR
//     UPDATE, first, to delete it), then insert a new row, which is theirs alone until they commit;
//     1a. the team's logo (team_logo), right after its own team row and only by a transaction that
//     holds that row: SetTeamLogo (FOR UPDATE), the team-creating transactions (the row they
//     inserted), every delete of a team row (the pending sweep, SignUp's pending replace,
//     DeclineTeam, the provider claim of a pending account) deletes the logo first;
//     1b. the team's brand (team_brand), right after the logo, by the same rule: SetTeamBrand (FOR
//     UPDATE), EnsureTeamOne (the team #1 seed, a no-op upsert), the team-creating transactions
//     (SignUp and CreateTeam write the brand of the row they inserted, right after its logo), and
//     every delete of a team row deletes it beside the logo (DeleteTeam is soft and keeps it;
//     removing a logo never touches it);
//  2. the account (platform_user);
//     2a. its provider identities (platform_identity), right after the account: the provider
//     completion holds the account before it writes an identity (FOR UPDATE before it inserts one,
//     FOR SHARE before it stamps a known one); it reads an identity without a lock only to learn
//     which account to take, and a concurrent first insert of the same identity starts it over;
//  3. the membership (team_member);
//  4. sessions (user_session);
//  5. invites (team_invite);
//  6. tokens and hand-off codes (email_token).
//
// oauth_state stands outside the order: it is created and consumed each in its own single-statement
// transaction, never while any other lock is held, and nothing else ever locks it.
//
// For one team the team row serialises every pair of team-locking transactions, so what follows
// it cannot close a cycle between them. Transactions that never lock the team row (member change,
// password change, invite resend / revoke, sign-out, the provider completion of an active or new
// account, the sign-in code exchange) keep 2–6. The provider claim of a pending account locks the
// account and then that account's pending teams (2 before 1), as SignUp's pending replace already
// does, and deletes them before it touches any session or token of the account, so it never holds
// a token the pending sweep (team, then that team's tokens) waits for. A pending team has no
// member, session, invite or hand-off: the sweep never reaches an account through it, and the only
// other path from a pending team row to an account is SignUp's address check — the shape two
// sign-ups of one email already have today (InnoDB picks a victim; the claim answers try_again).
// Two known exceptions, both safe:
// AcceptInvite locks its invite right after the team row, before the account and the membership
// (it learns the account from the invite; nothing that holds an account, membership or session
// asks for an invite after it); the sign-up confirm locks its token before its team, which is
// still pending then (DeleteTeam stops at the status check before it touches a token). After the
// membership the confirm inserts its new root session (a row no one else can hold yet), as the
// sign-in code exchange inserts one after its token.

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
