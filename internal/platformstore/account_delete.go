package platformstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/tenant"
	"github.com/jmoiron/sqlx"
)

// Errors of the account delete and restore (P0 §3.5).
var (
	// ErrBadConfirm: the typed @username is not the account's effective @username. Key bad_confirm.
	ErrBadConfirm = errors.New("platformstore: confirm username does not match")
	// ErrSoleOwner: the person is the only active owner of a team that has other active members.
	// Key sole_owner.
	ErrSoleOwner = errors.New("platformstore: sole owner of a team with members")
	// ErrOperatorAccount: an operator account is never deleted by its holder. Key operator.
	ErrOperatorAccount = errors.New("platformstore: operator account")
	// ErrAccountDeleted: the account is deleted and its restore window still runs (the right
	// password on an invite, a provider sign-in, a provider invite). Key account_deleted.
	ErrAccountDeleted = errors.New("platformstore: account deleted")
)

// errTeamsMoved: the person's memberships changed between DeleteAccount's unlocked read of their
// teams and its locks; the transaction is rolled back and run again.
var errTeamsMoved = errors.New("platformstore: memberships moved")

// maxDeleteTries bounds DeleteAccount's re-runs after errTeamsMoved.
const maxDeleteTries = 3

// liveTeamStatus reports whether a team in this status is seen by its members (not pending, not
// deleted): the statuses the sole-owner and closing rules look at.
func liveTeamStatus(st string) bool {
	switch st {
	case "review", "provisioning", "active", "suspended":
		return true
	}
	return false
}

// classifyTeam is the one rule behind the delete preview and the delete itself (P0 §3.5): sole =
// the person is the only active owner of a live team that has other active members (the delete is
// refused); closing = the person is the only active member of a live team other than team #1 (it
// closes with the account).
func classifyTeam(teamID int64, status string, owner bool, others, otherOwners int) (sole, closing bool) {
	if !liveTeamStatus(status) {
		return false, false
	}
	sole = owner && otherOwners == 0 && others > 0
	closing = others == 0 && teamID != tenant.TeamOneID
	return sole, closing
}

// confirmMatches compares the typed confirmation with the effective @username: spaces around it
// and one leading @ are ignored, and so is case (usernames are lowercase).
func confirmMatches(typed, username string) bool {
	typed = strings.TrimPrefix(strings.TrimSpace(typed), "@")
	return typed != "" && strings.EqualFold(typed, username)
}

// AccountDeletePreview is what the delete modal shows before the person confirms.
type AccountDeletePreview struct {
	// SoleOwner: teams where the person is the only active owner and others are members; the
	// delete is refused while it is not empty.
	SoleOwner []MemberTeam
	// Closing: teams where the person is the only active member; they close with the account.
	Closing []MemberTeam
}

// AccountDeletePreview reads the teams that block a delete and the teams that would close with it,
// by the rule DeleteAccount applies under its locks (a read without locks: the delete re-checks).
func (s *Store) AccountDeletePreview(ctx context.Context, userID int64) (AccountDeletePreview, error) {
	teams, err := s.MemberTeams(ctx, userID)
	if err != nil || len(teams) == 0 {
		return AccountDeletePreview{}, err
	}
	var counts []struct {
		TeamID      int64 `db:"team_id"`
		Others      int   `db:"others"`
		OtherOwners int   `db:"other_owners"`
	}
	if err := s.db.SelectContext(ctx, &counts, `SELECT m.team_id,
  COUNT(CASE WHEN m.user_id <> ? THEN 1 END) AS others,
  COUNT(CASE WHEN m.user_id <> ? AND m.role = 'owner' THEN 1 END) AS other_owners
FROM team_member m
WHERE m.status = 'active' AND m.team_id IN (SELECT o.team_id FROM team_member o WHERE o.user_id = ? AND o.status = 'active')
GROUP BY m.team_id`, userID, userID, userID); err != nil {
		return AccountDeletePreview{}, fmt.Errorf("platformstore: delete preview: %w", err)
	}
	type count struct{ others, owners int }
	byTeam := make(map[int64]count, len(counts))
	for _, c := range counts {
		byTeam[c.TeamID] = count{c.Others, c.OtherOwners}
	}
	var out AccountDeletePreview
	for _, t := range teams {
		c := byTeam[t.TeamID]
		sole, closing := classifyTeam(t.TeamID, t.Status, t.Role == "owner", c.others, c.owners)
		switch {
		case sole:
			out.SoleOwner = append(out.SoleOwner, t)
		case closing:
			out.Closing = append(out.Closing, t)
		}
	}
	return out, nil
}

// DeleteAccountParams is a person deleting their own account (P0 §3.5).
type DeleteAccountParams struct {
	UserID int64
	// Confirm is the @username the person typed (ErrBadConfirm unless it is the effective one).
	Confirm   string
	Now       time.Time
	Retention time.Duration
	// RestoreLink is the account_deleted letter's link (the root site's /restore page, no token).
	RestoreLink string
}

// ClosedTeam is a team that closed with the account.
type ClosedTeam struct {
	ID         int64
	Slug, Name string
}

// AccountDeletion is a committed DeleteAccount.
type AccountDeletion struct {
	Handle  string
	PurgeAt time.Time
	// Frozen are the teams where the membership froze and the team stays open: the caller disables
	// the person's admins row there (the closing teams are closed, and keep the row as it is).
	Frozen []int64
	// Closed are the teams that closed with the account.
	Closed []ClosedTeam
	// SIDs are every session the delete revoked (the person's, and any other in a closing team),
	// which the caller drops from its session caches.
	SIDs []string
}

// DeleteAccount deletes an active account for RestoreAccount's window (P0 §3.5). One transaction
// in lock order: every team the person is an active member of (FOR UPDATE, id order) → the account
// → per team its owner rows then its active member rows (FOR UPDATE, the member change's own first
// statement, so a role change or removal of the same team runs wholly before or after) → sessions →
// invites → tokens. Refused, with nothing written: ErrUserNotFound (not an active account),
// ErrOperatorAccount, ErrBadConfirm, ErrSoleOwner, ErrTryAgain (a closing team is provisioning, or
// a lock race).
//
// Written: the account deleted (deleted_at now, purge_at = now + retention, credential_gen bumped);
// every active membership frozen (role kept); each closing team deleted as DeleteTeam deletes it,
// with the account's purge_at and closed_with_user_id = the person; every session of the person
// and of the closing teams revoked; the closing teams' invites revoked and hand-offs consumed; every
// unused token of the person consumed; Google-connect flows ended; letter account_deleted. Nothing
// is erased: email, handle, names, password, identities, avatar and authored data stay until the
// purge, and the email and @username stay reserved.
func (s *Store) DeleteAccount(ctx context.Context, p DeleteAccountParams) (AccountDeletion, error) {
	now := p.Now.UTC()
	purgeAt := now.Add(p.Retention)
	for try := 1; ; try++ {
		res, err := s.deleteAccountOnce(ctx, p.UserID, p.Confirm, p.RestoreLink, now, purgeAt)
		if errors.Is(err, errTeamsMoved) {
			if try < maxDeleteTries {
				continue
			}
			return AccountDeletion{}, ErrTryAgain
		}
		if err != nil {
			return AccountDeletion{}, lockConflict(err)
		}
		for _, t := range res.Closed {
			s.dir.Invalidate(t.ID)
		}
		return res, nil
	}
}

func (s *Store) deleteAccountOnce(ctx context.Context, userID int64, confirm, link string, now, purgeAt time.Time) (AccountDeletion, error) {
	// The teams to lock, read without a lock (lock order: team rows before the account); the
	// transaction re-reads them after the account lock and starts over when they moved.
	var teamIDs []int64
	if err := s.db.SelectContext(ctx, &teamIDs, `SELECT team_id FROM team_member
WHERE user_id = ? AND status = 'active' ORDER BY team_id`, userID); err != nil {
		return AccountDeletion{}, fmt.Errorf("platformstore: delete account: teams: %w", err)
	}
	var res AccountDeletion
	err := s.inTx(ctx, func(tx *sqlx.Tx) error {
		res = AccountDeletion{PurgeAt: purgeAt}
		type lockedTeam struct {
			ID     int64  `db:"id"`
			Slug   string `db:"slug"`
			Name   string `db:"name"`
			Status string `db:"status"`
		}
		locked := make(map[int64]lockedTeam, len(teamIDs))
		for _, id := range teamIDs {
			var t lockedTeam
			if err := tx.GetContext(ctx, &t, `SELECT id, slug, name, status FROM team WHERE id = ? FOR UPDATE`, id); err != nil {
				if isNoRows(err) {
					continue
				}
				return fmt.Errorf("platformstore: delete account: lock team: %w", err)
			}
			locked[id] = t
		}
		u, err := lockUserByID(ctx, tx, userID)
		if err != nil {
			return err
		}
		if u.IsOperator {
			return ErrOperatorAccount
		}
		if u.Status != UserActive {
			return ErrUserNotFound
		}
		var public sql.NullString
		if err := tx.GetContext(ctx, &public, `SELECT public_username FROM platform_user WHERE id = ?`, userID); err != nil {
			return fmt.Errorf("platformstore: delete account: username: %w", err)
		}
		if !confirmMatches(confirm, effectiveUsername(u.Handle, public)) {
			return ErrBadConfirm
		}
		res.Handle = u.Handle
		// With the account held, no membership of the person can turn active (every path that makes
		// one holds the account), so this read is the whole set; one outside the locked teams means
		// the unlocked read is stale.
		var active []int64
		if err := tx.SelectContext(ctx, &active, `SELECT team_id FROM team_member
WHERE user_id = ? AND status = 'active' ORDER BY team_id`, userID); err != nil {
			return fmt.Errorf("platformstore: delete account: memberships: %w", err)
		}
		for _, id := range active {
			if _, ok := locked[id]; !ok {
				return errTeamsMoved
			}
		}
		var closing []lockedTeam
		for _, id := range active {
			t := locked[id]
			var owners, members []int64
			if err := tx.SelectContext(ctx, &owners, `SELECT user_id FROM team_member
WHERE team_id = ? AND role = 'owner' AND status = 'active' FOR UPDATE`, id); err != nil {
				return fmt.Errorf("platformstore: delete account: lock owners: %w", err)
			}
			if err := tx.SelectContext(ctx, &members, `SELECT user_id FROM team_member
WHERE team_id = ? AND status = 'active' FOR UPDATE`, id); err != nil {
				return fmt.Errorf("platformstore: delete account: lock members: %w", err)
			}
			if !slices.Contains(members, userID) {
				continue // removed meanwhile: nothing to freeze here
			}
			owner := slices.Contains(owners, userID)
			otherOwners := len(owners)
			if owner {
				otherOwners--
			}
			sole, closes := classifyTeam(id, t.Status, owner, len(members)-1, otherOwners)
			if sole {
				return ErrSoleOwner
			}
			if closes {
				if t.Status == "provisioning" {
					return ErrTryAgain
				}
				closing = append(closing, t)
			} else {
				res.Frozen = append(res.Frozen, id)
			}
		}
		if _, err := tx.ExecContext(ctx, `UPDATE platform_user SET status = 'deleted', deleted_at = ?, purge_at = ?,
credential_gen = credential_gen + 1 WHERE id = ?`, now, purgeAt, userID); err != nil {
			return fmt.Errorf("platformstore: delete account: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE team_join_request SET status = 'closed', decided_at = ?
WHERE user_id = ? AND status = 'pending'`, now, userID); err != nil {
			return fmt.Errorf("platformstore: close account join requests: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM platform_join_intent WHERE user_id = ?`, userID); err != nil {
			return fmt.Errorf("platformstore: delete account join intent: %w", err)
		}
		// Google-connect flows started by the account end with it (as a credential change ends them).
		if err := expireLinkFlowsTx(ctx, tx, userID, now); err != nil {
			return err
		}
		frozen := append([]int64(nil), res.Frozen...)
		for _, t := range closing {
			frozen = append(frozen, t.ID)
		}
		if len(frozen) > 0 {
			q, args, err := sqlx.In(`UPDATE team_member SET status = 'frozen'
WHERE user_id = ? AND status = 'active' AND team_id IN (?)`, userID, frozen)
			if err != nil {
				return fmt.Errorf("platformstore: delete account: freeze: %w", err)
			}
			if _, err := tx.ExecContext(ctx, q, args...); err != nil {
				return fmt.Errorf("platformstore: delete account: freeze memberships: %w", err)
			}
		}
		for _, t := range closing {
			if err := markTeamDeletedTx(ctx, tx, t.ID, t.Status, userID, now, purgeAt); err != nil {
				return err
			}
			res.Closed = append(res.Closed, ClosedTeam{ID: t.ID, Slug: t.Slug, Name: t.Name})
		}
		// Sessions: every live one of the person, then whatever else lives in a closing team (an
		// operator's entry).
		if err := tx.SelectContext(ctx, &res.SIDs,
			`SELECT sid FROM user_session WHERE user_id = ? AND revoked_at IS NULL FOR UPDATE`, userID); err != nil {
			return fmt.Errorf("platformstore: delete account: live sessions: %w", err)
		}
		if len(res.SIDs) > 0 {
			if _, err := tx.ExecContext(ctx,
				`UPDATE user_session SET revoked_at = ? WHERE user_id = ? AND revoked_at IS NULL`, now, userID); err != nil {
				return fmt.Errorf("platformstore: delete account: revoke sessions: %w", err)
			}
		}
		for _, t := range closing {
			sids, err := revokeAllTeamSessionsTx(ctx, tx, t.ID, now)
			if err != nil {
				return err
			}
			res.SIDs = append(res.SIDs, sids...)
		}
		for _, t := range closing {
			if err := revokeTeamInvitesTx(ctx, tx, t.ID, now); err != nil {
				return err
			}
		}
		// Tokens: every unused letter, hand-off and sign-in code of the person, then the operator
		// hand-offs into a closing team.
		if _, err := tx.ExecContext(ctx, `UPDATE email_token SET used_at = ? WHERE user_id = ? AND used_at IS NULL`,
			now, userID); err != nil {
			return fmt.Errorf("platformstore: delete account: expire tokens: %w", err)
		}
		for _, t := range closing {
			if err := expireTeamHandoffsTx(ctx, tx, t.ID, now); err != nil {
				return err
			}
		}
		if u.Email.Valid && u.Email.String != "" {
			if err := insertLetterTx(ctx, tx, Letter{To: u.Email.String, Template: "account_deleted", Link: link, Until: purgeAt},
				now); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return AccountDeletion{}, err
	}
	return res, nil
}
