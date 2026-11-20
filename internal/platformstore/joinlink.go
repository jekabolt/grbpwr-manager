package platformstore

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jmoiron/sqlx"
)

const (
	joinRefLen          = 22
	joinNonceBytes      = 16
	joinRequestLifetime = 14 * 24 * time.Hour
	joinDeclineCooldown = 7 * 24 * time.Hour
	joinRotateHourCap   = 10
	joinQueueCap        = 50
	joinTeamHourCap     = 20
	joinUserOpenCap     = 5
)

var (
	ErrJoinLinkInvalid     = errors.New("platformstore: join link invalid")
	ErrRotateCap           = errors.New("platformstore: join link rotate cap reached")
	ErrJoinQueueFull       = errors.New("platformstore: join request queue full")
	ErrJoinRateLimited     = errors.New("platformstore: join request rate limited")
	ErrJoinRequestNotFound = errors.New("platformstore: join request not found")
	ErrJoinConfirmEmail    = errors.New("platformstore: join account email is not verified")
	ErrJoinOperator        = errors.New("platformstore: operators cannot join teams")
	ErrJoinDisabled        = errors.New("platformstore: disabled account cannot join teams")
)

// JoinLinkToken derives the stable public token for one stored generation and nonce.
func JoinLinkToken(pepper string, teamID, gen int64, nonce []byte) string {
	m := hmac.New(sha256.New, []byte(pepper))
	m.Write([]byte("join-link|"))
	m.Write([]byte(strconv.FormatInt(teamID, 10)))
	m.Write([]byte{'|'})
	m.Write([]byte(strconv.FormatInt(gen, 10)))
	m.Write([]byte{'|'})
	m.Write([]byte(hex.EncodeToString(nonce)))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))[:joinRefLen]
}

// JoinLinkMatches compares a presented token in constant time.
func JoinLinkMatches(pepper string, teamID, gen int64, nonce []byte, presented string) bool {
	return hmac.Equal([]byte(JoinLinkToken(pepper, teamID, gen, nonce)), []byte(strings.TrimSpace(presented)))
}

// JoinRequestRef is the opaque public handle of a team_join_request row.
func JoinRequestRef(pepper string, id int64) string {
	m := hmac.New(sha256.New, []byte(pepper))
	m.Write([]byte("join-request|"))
	m.Write([]byte(strconv.FormatInt(id, 10)))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))[:joinRefLen]
}

type TeamJoinLink struct {
	TeamID int64  `db:"team_id"`
	Gen    int64  `db:"gen"`
	Nonce  []byte `db:"nonce"`
}

type JoinLinkChange struct {
	TeamID int64
	Actor  Actor
	IP     string
	Now    time.Time
}

func randomJoinNonce() ([]byte, error) {
	b := make([]byte, joinNonceBytes)
	if _, err := rand.Read(b); err != nil {
		return nil, fmt.Errorf("platformstore: join nonce: %w", err)
	}
	return b, nil
}

// EnsureJoinLink creates generation one once, then always returns the same stored link material.
func (s *Store) EnsureJoinLink(ctx context.Context, p JoinLinkChange) (TeamJoinLink, error) {
	nonce, err := randomJoinNonce()
	if err != nil {
		return TeamJoinLink{}, err
	}
	var out TeamJoinLink
	err = s.inTx(ctx, func(tx *sqlx.Tx) error {
		st, err := shareTeamTx(ctx, tx, p.TeamID)
		if err != nil {
			return err
		}
		if st == "" || st == "deleted" {
			return ErrTeamNotFound
		}
		if st == "suspended" {
			return ErrTeamReadOnly
		}
		if st != "active" {
			return ErrWrongTeamStatus
		}
		if _, err := tx.ExecContext(ctx, `INSERT IGNORE INTO team_join_link
(team_id, gen, nonce, rotated_at, rotated_by) VALUES (?, 1, ?, ?, ?)`, p.TeamID, nonce, p.Now, p.Actor.UserID); err != nil {
			return fmt.Errorf("platformstore: ensure join link: %w", err)
		}
		if err := tx.GetContext(ctx, &out, `SELECT team_id, gen, nonce FROM team_join_link WHERE team_id = ? FOR SHARE`, p.TeamID); err != nil {
			return fmt.Errorf("platformstore: read join link: %w", err)
		}
		if err := checkActorTx(ctx, tx, p.TeamID, p.Actor); err != nil {
			return err
		}
		return insertJoinAuditTx(ctx, tx, p.Actor.UserID, p.TeamID, "join_link_copy", "", p.IP, p.Now)
	})
	return out, err
}

// RotateJoinLink moves to a fresh generation, closes pending requests of older generations, and
// records the rotation atomically. Ten rotations per team per hour are allowed.
func (s *Store) RotateJoinLink(ctx context.Context, p JoinLinkChange) (TeamJoinLink, error) {
	nonce, err := randomJoinNonce()
	if err != nil {
		return TeamJoinLink{}, err
	}
	var out TeamJoinLink
	err = s.inTx(ctx, func(tx *sqlx.Tx) error {
		st, err := shareTeamTx(ctx, tx, p.TeamID)
		if err != nil {
			return err
		}
		if st == "" || st == "deleted" {
			return ErrTeamNotFound
		}
		if st == "suspended" {
			return ErrTeamReadOnly
		}
		if st != "active" {
			return ErrWrongTeamStatus
		}
		if _, err := tx.ExecContext(ctx, `INSERT IGNORE INTO team_join_link
(team_id, gen, nonce, rotated_at, rotated_by) VALUES (?, 1, ?, ?, ?)`, p.TeamID, nonce, p.Now, p.Actor.UserID); err != nil {
			return fmt.Errorf("platformstore: initialise join link: %w", err)
		}
		var cur TeamJoinLink
		if err := tx.GetContext(ctx, &cur, `SELECT team_id, gen, nonce FROM team_join_link WHERE team_id = ? FOR UPDATE`, p.TeamID); err != nil {
			return fmt.Errorf("platformstore: lock join link: %w", err)
		}
		if err := checkActorTx(ctx, tx, p.TeamID, p.Actor); err != nil {
			return err
		}
		var rotations int
		if err := tx.GetContext(ctx, &rotations, `SELECT COUNT(*) FROM platform_audit
WHERE team_id = ? AND action = 'join_link_rotate' AND created_at > ?`, p.TeamID, p.Now.Add(-time.Hour)); err != nil {
			return fmt.Errorf("platformstore: count join link rotations: %w", err)
		}
		if rotations >= joinRotateHourCap {
			return ErrRotateCap
		}
		if _, err := tx.ExecContext(ctx, `UPDATE team_join_link SET gen = gen + 1, nonce = ?, rotated_at = ?, rotated_by = ?
WHERE team_id = ?`, nonce, p.Now, p.Actor.UserID, p.TeamID); err != nil {
			return fmt.Errorf("platformstore: rotate join link: %w", err)
		}
		out = TeamJoinLink{TeamID: p.TeamID, Gen: cur.Gen + 1, Nonce: nonce}
		if _, err := tx.ExecContext(ctx, `UPDATE team_join_request SET status = 'closed', decided_at = ?, decided_by = ?
WHERE team_id = ? AND status = 'pending' AND gen < ?`, p.Now, p.Actor.UserID, p.TeamID, out.Gen); err != nil {
			return fmt.Errorf("platformstore: close old join requests: %w", err)
		}
		return insertJoinAuditTx(ctx, tx, p.Actor.UserID, p.TeamID, "join_link_rotate", "", p.IP, p.Now)
	})
	return out, err
}

// JoinLinkInfo validates the current link and returns its team's name. Suspended teams remain
// identifiable on the join page, but JoinTeam refuses them as read-only.
func (s *Store) JoinLinkInfo(ctx context.Context, teamID int64, token, pepper string) (string, error) {
	var row struct {
		Name   string `db:"name"`
		Status string `db:"status"`
		Gen    int64  `db:"gen"`
		Nonce  []byte `db:"nonce"`
	}
	if err := s.db.GetContext(ctx, &row, `SELECT t.name, t.status, l.gen, l.nonce
FROM team t JOIN team_join_link l ON l.team_id = t.id WHERE t.id = ?`, teamID); err != nil {
		if isNoRows(err) {
			return "", ErrJoinLinkInvalid
		}
		return "", fmt.Errorf("platformstore: join link info: %w", err)
	}
	if row.Status == "deleted" || !JoinLinkMatches(pepper, teamID, row.Gen, row.Nonce, token) {
		return "", ErrJoinLinkInvalid
	}
	return row.Name, nil
}

type JoinState string

const (
	JoinAlreadyMember JoinState = "already_member"
	JoinJoined        JoinState = "joined"
	JoinPending       JoinState = "pending"
)

// JoinMember is the staged membership shared by link joins and approvals.
type JoinMember struct {
	TeamID      int64
	UserID      int64
	Handle      string
	Role        string
	Permissions []byte
	InviteID    int64
	RequestID   int64
	Gen         int64
}

type JoinDecisionParams struct {
	TeamID, UserID int64
	Token, Pepper  string
	IP             string
	Now            time.Time
	RequestLink    string
	Stage          func(context.Context, JoinMember) error
	Enable         func(context.Context, JoinMember) (bool, error)
}

type JoinDecisionResult struct {
	State         JoinState
	EnablePending bool
}

type joinReservation struct {
	state  JoinState
	member JoinMember
	email  string
}

// JoinTeam decides an explicit request by a verified active account.
func (s *Store) JoinTeam(ctx context.Context, p JoinDecisionParams) (JoinDecisionResult, error) {
	var r joinReservation
	err := s.inTx(ctx, func(tx *sqlx.Tx) error {
		link, st, err := lockCurrentJoinLinkTx(ctx, tx, p.TeamID, p.Token, p.Pepper)
		if err != nil {
			return err
		}
		if st == "suspended" {
			return ErrTeamReadOnly
		}
		// The account lock serialises this user's cross-team open-request cap. It follows the common
		// team -> account order and also excludes a concurrent account delete.
		u, err := lockUserByID(ctx, tx, p.UserID)
		if err != nil {
			return err
		}
		if err := joinableUser(u); err != nil {
			return err
		}
		r, err = s.decideJoinTx(ctx, tx, p.TeamID, link.Gen, u, p.IP, p.Now, p.RequestLink)
		return err
	})
	if err != nil {
		return JoinDecisionResult{}, err
	}
	return s.finishJoinReservation(ctx, p, r)
}

func joinableUser(u User) error {
	if u.IsOperator {
		return ErrJoinOperator
	}
	if u.Status == UserDisabled || u.Status == UserDeleted {
		return ErrJoinDisabled
	}
	if u.Status != UserActive || !u.EmailVerifiedAt.Valid || !u.Email.Valid {
		return ErrJoinConfirmEmail
	}
	return nil
}

func lockCurrentJoinLinkTx(ctx context.Context, tx *sqlx.Tx, teamID int64, token, pepper string) (TeamJoinLink, string, error) {
	st, err := shareTeamTx(ctx, tx, teamID)
	if err != nil {
		return TeamJoinLink{}, "", err
	}
	if st == "" || st == "deleted" || st == "pending" || st == "review" || st == "provisioning" {
		return TeamJoinLink{}, st, ErrJoinLinkInvalid
	}
	var link TeamJoinLink
	if err := tx.GetContext(ctx, &link, `SELECT team_id, gen, nonce FROM team_join_link WHERE team_id = ? FOR SHARE`, teamID); err != nil {
		if isNoRows(err) {
			return TeamJoinLink{}, st, ErrJoinLinkInvalid
		}
		return TeamJoinLink{}, st, fmt.Errorf("platformstore: lock join link: %w", err)
	}
	if !JoinLinkMatches(pepper, teamID, link.Gen, link.Nonce, token) {
		return TeamJoinLink{}, st, ErrJoinLinkInvalid
	}
	return link, st, nil
}

func lockJoinGenerationTx(ctx context.Context, tx *sqlx.Tx, teamID, gen int64) (string, error) {
	st, err := shareTeamTx(ctx, tx, teamID)
	if err != nil {
		return "", err
	}
	if st == "" || st == "deleted" || st == "pending" || st == "review" || st == "provisioning" {
		return st, ErrJoinLinkInvalid
	}
	var current int64
	if err := tx.GetContext(ctx, &current, `SELECT gen FROM team_join_link WHERE team_id = ? FOR SHARE`, teamID); err != nil {
		if isNoRows(err) {
			return st, ErrJoinLinkInvalid
		}
		return st, fmt.Errorf("platformstore: lock join generation: %w", err)
	}
	if current != gen {
		return st, ErrJoinLinkInvalid
	}
	return st, nil
}

func (s *Store) decideJoinTx(ctx context.Context, tx *sqlx.Tx, teamID, gen int64, u User, ip string, now time.Time,
	requestLink string) (joinReservation, error) {
	var memberStatus []string
	if err := tx.SelectContext(ctx, &memberStatus, `SELECT status FROM team_member
WHERE team_id = ? AND user_id = ? FOR UPDATE`, teamID, u.ID); err != nil {
		return joinReservation{}, fmt.Errorf("platformstore: join membership: %w", err)
	}
	if len(memberStatus) > 0 {
		switch memberStatus[0] {
		case "active":
			return joinReservation{state: JoinAlreadyMember}, nil
		case "joining":
			// A join already in its four steps (approved request or accepted invite): another press
			// must not open a second request that a later approve would re-stage over the first.
			return joinReservation{state: JoinPending}, nil
		}
	}

	var invites []Invite
	if err := tx.SelectContext(ctx, &invites, `SELECT i.id, i.team_id, t.slug, t.name, t.status, i.email, i.role,
i.permissions, i.invited_by, i.expires_at, i.name AS invite_name
FROM team_invite i JOIN team t ON t.id = i.team_id
WHERE i.team_id = ? AND i.email = ? AND i.accepted_at IS NULL AND i.revoked_at IS NULL AND i.expires_at > ?
ORDER BY i.created_at DESC, i.id DESC FOR UPDATE OF i`, teamID, u.Email.String, now); err != nil {
		return joinReservation{}, fmt.Errorf("platformstore: lock join invites: %w", err)
	}
	if len(invites) > 0 {
		inv := invites[0]
		if err := reserveMembershipTx(ctx, tx, inv, u.ID, now); err != nil {
			return joinReservation{}, err
		}
		return joinReservation{state: JoinJoined, email: u.Email.String, member: JoinMember{
			TeamID: teamID, UserID: u.ID, Handle: u.Handle, Role: inv.Role, Permissions: inv.Permissions,
			InviteID: inv.ID, Gen: gen,
		}}, nil
	}

	if _, err := tx.ExecContext(ctx, `UPDATE team_join_request SET status = 'expired', decided_at = ?
WHERE team_id = ? AND status = 'pending' AND created_at <= ?`, now, teamID, now.Add(-joinRequestLifetime)); err != nil {
		return joinReservation{}, fmt.Errorf("platformstore: expire join requests: %w", err)
	}
	var open []int64
	if err := tx.SelectContext(ctx, &open, `SELECT id FROM team_join_request
WHERE team_id = ? AND user_id = ? AND status = 'pending' FOR UPDATE`, teamID, u.ID); err != nil {
		return joinReservation{}, fmt.Errorf("platformstore: lock open join request: %w", err)
	}
	if len(open) > 0 {
		return joinReservation{state: JoinPending}, nil
	}
	var declined []time.Time
	if err := tx.SelectContext(ctx, &declined, `SELECT decided_at FROM team_join_request
WHERE team_id = ? AND user_id = ? AND status = 'declined' AND decided_at IS NOT NULL
ORDER BY decided_at DESC LIMIT 1 FOR UPDATE`, teamID, u.ID); err != nil {
		return joinReservation{}, fmt.Errorf("platformstore: last declined join request: %w", err)
	}
	if len(declined) > 0 && declined[0].After(now.Add(-joinDeclineCooldown)) {
		return joinReservation{state: JoinPending}, nil
	}
	// Lock the three cap ranges so concurrent requesters cannot both observe the last free slot.
	var teamOpen []int64
	if err := tx.SelectContext(ctx, &teamOpen, `SELECT id FROM team_join_request
WHERE team_id = ? AND gen = ? AND status = 'pending' FOR UPDATE`, teamID, gen); err != nil {
		return joinReservation{}, fmt.Errorf("platformstore: join queue size: %w", err)
	}
	if len(teamOpen) >= joinQueueCap {
		return joinReservation{}, ErrJoinQueueFull
	}
	var teamRecent []int64
	if err := tx.SelectContext(ctx, &teamRecent, `SELECT id FROM team_join_request
WHERE team_id = ? AND created_at > ? FOR UPDATE`, teamID, now.Add(-time.Hour)); err != nil {
		return joinReservation{}, fmt.Errorf("platformstore: recent join requests: %w", err)
	}
	if len(teamRecent) >= joinTeamHourCap {
		return joinReservation{}, ErrJoinRateLimited
	}
	var userOpen []int64
	if err := tx.SelectContext(ctx, &userOpen, `SELECT id FROM team_join_request
WHERE user_id = ? AND status = 'pending' FOR UPDATE`, u.ID); err != nil {
		return joinReservation{}, fmt.Errorf("platformstore: user join requests: %w", err)
	}
	if len(userOpen) >= joinUserOpenCap {
		return joinReservation{}, ErrJoinRateLimited
	}
	r, err := tx.ExecContext(ctx, `INSERT INTO team_join_request
(team_id, user_id, gen, status, ip, created_at) VALUES (?, ?, ?, 'pending', ?, ?)`,
		teamID, u.ID, gen, nullableText(truncateIP(ip)), now)
	if err != nil {
		if dupKey(err, "uq_team_join_request_open") {
			return joinReservation{state: JoinPending}, nil
		}
		return joinReservation{}, fmt.Errorf("platformstore: insert join request: %w", err)
	}
	id, err := r.LastInsertId()
	if err != nil {
		return joinReservation{}, fmt.Errorf("platformstore: join request id: %w", err)
	}
	if err := insertJoinAuditTx(ctx, tx, u.ID, teamID, "join_request", u.Email.String, ip, now); err != nil {
		return joinReservation{}, err
	}
	if err := notifyJoinRequestTx(ctx, tx, teamID, u.Email.String, requestLink, now); err != nil {
		return joinReservation{}, err
	}
	return joinReservation{state: JoinPending, email: u.Email.String, member: JoinMember{RequestID: id}}, nil
}

func (s *Store) finishJoinReservation(ctx context.Context, p JoinDecisionParams, r joinReservation) (JoinDecisionResult, error) {
	if r.state != JoinJoined {
		return JoinDecisionResult{State: r.state}, nil
	}
	if p.Stage != nil {
		if err := p.Stage(ctx, r.member); err != nil {
			return JoinDecisionResult{}, fmt.Errorf("platformstore: stage join member: %w", err)
		}
	}
	if err := s.activateJoinReservation(ctx, p, r); err != nil {
		return JoinDecisionResult{}, err
	}
	out := JoinDecisionResult{State: JoinJoined}
	if p.Enable != nil {
		if ok, err := p.Enable(ctx, r.member); err != nil || !ok {
			out.EnablePending = true
		}
	}
	return out, nil
}

func (s *Store) activateJoinReservation(ctx context.Context, p JoinDecisionParams, r joinReservation) error {
	return s.inTx(ctx, func(tx *sqlx.Tx) error {
		// Step one already made the link decision while holding its generation FOR SHARE. Once a
		// request or invite is reserved, a later link rotation must not strand that four-step join.
		st, err := shareTeamTx(ctx, tx, r.member.TeamID)
		if err != nil {
			return err
		}
		if st != "active" && st != "suspended" {
			return ErrJoinLinkInvalid
		}
		if st == "suspended" {
			return ErrTeamReadOnly
		}
		u, err := shareUserByID(ctx, tx, r.member.UserID)
		if err != nil {
			return err
		}
		if err := joinableUser(u); err != nil {
			return err
		}
		var prev string
		if err := tx.GetContext(ctx, &prev, `SELECT status FROM team_member
WHERE team_id = ? AND user_id = ? FOR UPDATE`, r.member.TeamID, r.member.UserID); err != nil {
			if isNoRows(err) {
				return ErrJoinLinkInvalid
			}
			return fmt.Errorf("platformstore: lock joined membership: %w", err)
		}
		if prev == "joining" {
			if _, err := revokeTeamSessionsTx(ctx, tx, r.member.UserID, r.member.TeamID, p.Now); err != nil {
				return err
			}
		}
		rr, err := tx.ExecContext(ctx, `UPDATE team_member SET role = ?,
entry_pending = IF(status = 'joining', 1, entry_pending), status = 'active'
WHERE team_id = ? AND user_id = ? AND status IN ('joining','active')`,
			r.member.Role, r.member.TeamID, r.member.UserID)
		if err != nil {
			return fmt.Errorf("platformstore: activate joined member: %w", err)
		}
		if n, _ := rr.RowsAffected(); n == 0 {
			return ErrJoinLinkInvalid
		}
		switch {
		case r.member.InviteID > 0:
			rr, err = tx.ExecContext(ctx, `UPDATE team_invite SET accepted_at = ?
WHERE id = ? AND team_id = ? AND accepted_at IS NULL AND revoked_at IS NULL AND expires_at > ?`,
				p.Now, r.member.InviteID, r.member.TeamID, p.Now)
			if err != nil {
				return fmt.Errorf("platformstore: accept join-link invite: %w", err)
			}
			if n, _ := rr.RowsAffected(); n != 1 {
				return ErrJoinLinkInvalid
			}
			if err := closePendingJoinRequestsTx(ctx, tx, r.member.TeamID, r.member.UserID, p.Now); err != nil {
				return err
			}
			return insertJoinAuditTx(ctx, tx, r.member.UserID, r.member.TeamID, "join_by_link", r.email, p.IP, p.Now)
		case r.member.RequestID > 0:
			var status string
			if err := tx.GetContext(ctx, &status, `SELECT status FROM team_join_request
WHERE id = ? AND team_id = ? AND user_id = ? AND gen = ? FOR UPDATE`,
				r.member.RequestID, r.member.TeamID, r.member.UserID, r.member.Gen); err != nil || status != "approved" {
				if err != nil && !isNoRows(err) {
					return fmt.Errorf("platformstore: lock approved join request: %w", err)
				}
				return ErrJoinLinkInvalid
			}
			if err := closePendingJoinRequestsTx(ctx, tx, r.member.TeamID, r.member.UserID, p.Now); err != nil {
				return err
			}
			if err := revokeLiveInvitesTx(ctx, tx, r.member.TeamID, u.Email.String, p.Now); err != nil {
				return err
			}
			if prev == "joining" {
				return insertJoinApprovedLetterTx(ctx, tx, u.Email.String, r.member.TeamID, p.RequestLink, p.Now)
			}
		}
		return nil
	})
}

// revokeLiveInvitesTx revokes the team's open invites for an address: once a person joined through a
// request or was removed, an older invite must not (re)admit them with its own role later.
func revokeLiveInvitesTx(ctx context.Context, tx *sqlx.Tx, teamID int64, email string, now time.Time) error {
	if email == "" {
		return nil
	}
	if _, err := tx.ExecContext(ctx, `UPDATE team_invite SET revoked_at = ?
WHERE team_id = ? AND email = ? AND accepted_at IS NULL AND revoked_at IS NULL`, now, teamID, email); err != nil {
		return fmt.Errorf("platformstore: revoke live invites: %w", err)
	}
	return nil
}

func closePendingJoinRequestsTx(ctx context.Context, tx *sqlx.Tx, teamID, userID int64, now time.Time) error {
	if _, err := tx.ExecContext(ctx, `UPDATE team_join_request
SET status = 'closed', decided_at = ?, decided_by = ?
WHERE team_id = ? AND user_id = ? AND status = 'pending'`, now, userID, teamID, userID); err != nil {
		return fmt.Errorf("platformstore: close joined requests: %w", err)
	}
	return nil
}

// JoinSignUpParams creates the pending account and its verify_join intent without creating a team.
type JoinSignUpParams struct {
	TeamID                            int64
	Link, Pepper                      string
	Email, PasswordHash, HandleBase   string
	FirstName, LastName, TermsVersion string
	Now                               time.Time
	MaxLettersPerHour                 int
	PasswordMatches                   func(string) bool
	Letter                            MakeLetter
}

func (s *Store) JoinSignUp(ctx context.Context, p JoinSignUpParams) (SignUpResult, error) {
	var res SignUpResult
	err := s.inTx(ctx, func(tx *sqlx.Tx) error {
		link, st, err := lockCurrentJoinLinkTx(ctx, tx, p.TeamID, p.Link, p.Pepper)
		if err != nil {
			return err
		}
		if st == "suspended" {
			return ErrTeamReadOnly
		}
		if n, err := lettersSinceTx(ctx, tx, p.Email, p.Now.Add(-time.Hour)); err != nil {
			return err
		} else if n >= p.MaxLettersPerHour {
			res.Reason = "letter_cap"
			return nil
		}
		u, err := lockUserByEmail(ctx, tx, p.Email)
		var uid int64
		switch {
		case errors.Is(err, ErrUserNotFound):
			uid, _, err = insertUserTx(ctx, tx, p.Email, p.PasswordHash, UserPending, nil, false,
				p.HandleBase, false, p.TermsVersion, p.FirstName, p.LastName, p.Now)
			if errors.Is(err, ErrTokenInvalid) {
				res.Reason = "race"
				return nil
			}
			if err != nil {
				return err
			}
		case err != nil:
			return err
		case u.IsOperator:
			res.Reason = "operator"
			return nil
		case u.Status == UserPending:
			uid = u.ID
			if p.PasswordMatches == nil || !p.PasswordMatches(u.PasswordHash) {
				var live int
				if err := tx.GetContext(ctx, &live, `SELECT COUNT(*) FROM email_token
WHERE user_id = ? AND purpose IN ('verify','verify_join') AND used_at IS NULL AND expires_at > ?`, uid, p.Now); err != nil {
					return fmt.Errorf("platformstore: live join verify letters: %w", err)
				}
				if live > 0 {
					res.Reason = "pending_password_mismatch"
					return nil
				}
			}
			termsVersion, termsAt := termsColumns(p.TermsVersion, p.Now)
			if _, err := tx.ExecContext(ctx, `UPDATE platform_user SET password_hash = ?, first_name = ?, last_name = ?,
terms_version = COALESCE(?, terms_version), terms_accepted_at = COALESCE(?, terms_accepted_at) WHERE id = ?`,
				p.PasswordHash, p.FirstName, p.LastName, termsVersion, termsAt, uid); err != nil {
				return fmt.Errorf("platformstore: update pending join account: %w", err)
			}
			if _, err := deletePendingTeamsTx(ctx, tx, `created_by = ? AND status = 'pending'`, uid); err != nil {
				return fmt.Errorf("platformstore: drop pending teams for join: %w", err)
			}
		case u.Status == UserActive:
			res.Reason = "existing_account"
			return nil
		default:
			res.Reason = "disabled"
			return nil
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO platform_join_intent (user_id, team_id, gen, created_at)
VALUES (?, ?, ?, ?) ON DUPLICATE KEY UPDATE team_id = VALUES(team_id), gen = VALUES(gen), created_at = VALUES(created_at)`,
			uid, p.TeamID, link.Gen, p.Now); err != nil {
			return fmt.Errorf("platformstore: save join intent: %w", err)
		}
		var team TeamBrief
		if err := tx.GetContext(ctx, &team, `SELECT id, slug, name, status, COALESCE(created_by, 0) AS created_by
FROM team WHERE id = ?`, p.TeamID); err != nil {
			return fmt.Errorf("platformstore: join team letter data: %w", err)
		}
		if err := sendLetterTx(ctx, tx, uid, &team, p.Email, p.Letter, p.Now); err != nil {
			return err
		}
		res = SignUpResult{Created: true, UserID: uid, TeamID: p.TeamID}
		return nil
	})
	return res, err
}

// ResendJoinVerification queues a fresh join code for the pending intent. It reports false for any
// unknown, stale, rotated, verified, disabled, or capped account.
func (s *Store) ResendJoinVerification(ctx context.Context, email string, now time.Time, maxPerHour int,
	mk MakeLetter) (bool, error) {
	var pre struct {
		UserID int64 `db:"user_id"`
		TeamID int64 `db:"team_id"`
		Gen    int64 `db:"gen"`
	}
	if err := s.db.GetContext(ctx, &pre, `SELECT i.user_id, i.team_id, i.gen FROM platform_join_intent i
JOIN platform_user u ON u.id = i.user_id WHERE u.email = ?`, email); err != nil {
		if isNoRows(err) {
			return false, nil
		}
		return false, fmt.Errorf("platformstore: find join intent: %w", err)
	}
	sent := false
	err := s.inTx(ctx, func(tx *sqlx.Tx) error {
		st, err := lockJoinGenerationTx(ctx, tx, pre.TeamID, pre.Gen)
		if errors.Is(err, ErrJoinLinkInvalid) || st == "suspended" {
			return nil
		}
		if err != nil {
			return err
		}
		u, err := lockUserByID(ctx, tx, pre.UserID)
		if err != nil || u.Status != UserPending || !u.Email.Valid || u.Email.String != email {
			if err != nil && !errors.Is(err, ErrUserNotFound) {
				return err
			}
			return nil
		}
		var intent int
		if err := tx.GetContext(ctx, &intent, `SELECT COUNT(*) FROM platform_join_intent
WHERE user_id = ? AND team_id = ? AND gen = ?`, u.ID, pre.TeamID, pre.Gen); err != nil {
			return fmt.Errorf("platformstore: check join intent: %w", err)
		}
		if intent != 1 {
			return nil
		}
		if n, err := lettersSinceTx(ctx, tx, email, now.Add(-time.Hour)); err != nil {
			return err
		} else if n >= maxPerHour {
			return nil
		}
		var team TeamBrief
		if err := tx.GetContext(ctx, &team, `SELECT id, slug, name, status, COALESCE(created_by, 0) AS created_by
FROM team WHERE id = ?`, pre.TeamID); err != nil {
			return fmt.Errorf("platformstore: join team letter data: %w", err)
		}
		if err := sendLetterTx(ctx, tx, u.ID, &team, u.Email.String, mk, now); err != nil {
			return err
		}
		sent = true
		return nil
	})
	return sent, err
}

type ConfirmJoinParams struct {
	Attempt CodeAttempt
	// PasswordMatches checks the password typed with the code against the pending account: the code
	// only works for whoever set the password, so a sign-up made on someone else's address with an
	// attacker's password cannot be activated by the inbox owner typing the code (takeover).
	PasswordMatches func(stored string) bool
	TeamID          int64
	IP              string
	Now             time.Time
	RequestLink     string
	Stage           func(context.Context, JoinMember) error
	Enable          func(context.Context, JoinMember) (bool, error)
}

// ConfirmJoinCode verifies the pending account's email and immediately runs the join decision.
func (s *Store) ConfirmJoinCode(ctx context.Context, p ConfirmJoinParams) (JoinDecisionResult, error) {
	p.Attempt.Purpose = "verify_join"
	var pre struct {
		TeamID int64 `db:"team_id"`
		Gen    int64 `db:"gen"`
	}
	if err := s.db.GetContext(ctx, &pre, `SELECT i.team_id, i.gen FROM platform_join_intent i
JOIN platform_user u ON u.id = i.user_id WHERE u.email = ?`, p.Attempt.Email); err != nil {
		p.Attempt.Match(0, "")
		if isNoRows(err) {
			return JoinDecisionResult{}, ErrTokenInvalid
		}
		return JoinDecisionResult{}, fmt.Errorf("platformstore: confirm join intent: %w", err)
	}
	if p.TeamID <= 0 || pre.TeamID != p.TeamID {
		p.Attempt.Match(0, "")
		return JoinDecisionResult{}, ErrJoinLinkInvalid
	}
	var r joinReservation
	err := s.withCodeAfter(ctx, p.Attempt, func(tx *sqlx.Tx) error {
		st, err := lockJoinGenerationTx(ctx, tx, pre.TeamID, pre.Gen)
		if err != nil {
			return err
		}
		if st == "suspended" {
			return ErrTeamReadOnly
		}
		return nil
	}, func(tx *sqlx.Tx, t tokenRow, u User) error {
		if t.Purpose != "verify_join" || u.IsOperator || u.Status == UserDisabled || u.Status == UserDeleted {
			return ErrTokenInvalid
		}
		if p.PasswordMatches == nil || !p.PasswordMatches(u.PasswordHash) {
			return ErrWrongPassword
		}
		var intent int
		if err := tx.GetContext(ctx, &intent, `SELECT COUNT(*) FROM platform_join_intent
WHERE user_id = ? AND team_id = ? AND gen = ?`, u.ID, pre.TeamID, pre.Gen); err != nil {
			return fmt.Errorf("platformstore: lock confirmed join intent: %w", err)
		}
		if intent != 1 {
			return ErrJoinLinkInvalid
		}
		if _, err := tx.ExecContext(ctx, `UPDATE platform_user SET status = 'active',
email_verified_at = COALESCE(email_verified_at, ?) WHERE id = ?`, p.Now, u.ID); err != nil {
			return fmt.Errorf("platformstore: activate join account: %w", err)
		}
		u.Status, u.EmailVerifiedAt = UserActive, sql.NullTime{Time: p.Now, Valid: true}
		var err error
		r, err = s.decideJoinTx(ctx, tx, pre.TeamID, pre.Gen, u, p.IP, p.Now, p.RequestLink)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM platform_join_intent WHERE user_id = ?`, u.ID); err != nil {
			return fmt.Errorf("platformstore: consume join intent: %w", err)
		}
		return nil
	})
	if err != nil {
		return JoinDecisionResult{}, err
	}
	return s.finishJoinReservation(ctx, JoinDecisionParams{
		TeamID: pre.TeamID, UserID: r.member.UserID, IP: p.IP, Now: p.Now, RequestLink: p.RequestLink,
		Stage: p.Stage, Enable: p.Enable,
	}, r)
}

type PendingJoinRequest struct {
	ID        int64     `db:"id"`
	UserID    int64     `db:"user_id"`
	Email     string    `db:"email"`
	CreatedAt time.Time `db:"created_at"`
}

func (s *Store) PendingJoinRequests(ctx context.Context, teamID int64, now time.Time) ([]PendingJoinRequest, error) {
	var out []PendingJoinRequest
	if err := s.db.SelectContext(ctx, &out, `SELECT r.id, r.user_id, u.email, r.created_at
FROM team_join_request r JOIN platform_user u ON u.id = r.user_id
WHERE r.team_id = ? AND r.status = 'pending' AND r.created_at > ?
ORDER BY r.created_at, r.id`, teamID, now.Add(-joinRequestLifetime)); err != nil {
		return nil, fmt.Errorf("platformstore: pending join requests: %w", err)
	}
	return out, nil
}

// JoinRequestsForApproval also returns an approved request while its membership is still in the
// joining stage. That keeps the opaque request reference usable to resume a failed stage step,
// without putting approved requests back on the people screen.
func (s *Store) JoinRequestsForApproval(ctx context.Context, teamID int64, now time.Time) ([]PendingJoinRequest, error) {
	var out []PendingJoinRequest
	if err := s.db.SelectContext(ctx, &out, `SELECT r.id, r.user_id, u.email, r.created_at
FROM team_join_request r
JOIN platform_user u ON u.id = r.user_id
LEFT JOIN team_member m ON m.team_id = r.team_id AND m.user_id = r.user_id
WHERE r.team_id = ? AND (
  (r.status = 'pending' AND r.created_at > ?)
  OR (r.status = 'approved' AND m.status = 'joining')
)
ORDER BY r.created_at, r.id`, teamID, now.Add(-joinRequestLifetime)); err != nil {
		return nil, fmt.Errorf("platformstore: join requests for approval: %w", err)
	}
	return out, nil
}

type JoinRequestDecisionParams struct {
	TeamID, RequestID int64
	Actor             Actor
	IP                string
	Now               time.Time
	Permissions       []byte
	Role              string // member (default) or owner
	ApprovedLink      string
	Stage             func(context.Context, JoinMember) error
	Enable            func(context.Context, JoinMember) (bool, error)
}

// ApproveJoinRequest reserves a member, stages the team row, activates the membership, then enables
// the row. A retry of an approved unfinished request resumes the same four-step path.
func (s *Store) ApproveJoinRequest(ctx context.Context, p JoinRequestDecisionParams) (JoinDecisionResult, error) {
	var r joinReservation
	err := s.inTx(ctx, func(tx *sqlx.Tx) error {
		st, err := shareTeamTx(ctx, tx, p.TeamID)
		if err != nil {
			return err
		}
		if st != "active" {
			if st == "suspended" {
				return ErrTeamReadOnly
			}
			return ErrJoinLinkInvalid
		}
		var currentGen int64
		if err := tx.GetContext(ctx, &currentGen, `SELECT gen FROM team_join_link WHERE team_id = ? FOR SHARE`, p.TeamID); err != nil {
			if isNoRows(err) {
				return ErrJoinLinkInvalid
			}
			return fmt.Errorf("platformstore: lock join link: %w", err)
		}
		if err := checkActorTx(ctx, tx, p.TeamID, p.Actor); err != nil {
			return err
		}
		var row struct {
			UserID int64  `db:"user_id"`
			Gen    int64  `db:"gen"`
			Status string `db:"status"`
		}
		if err := tx.GetContext(ctx, &row, `SELECT user_id, gen, status FROM team_join_request
WHERE id = ? AND team_id = ? FOR UPDATE`, p.RequestID, p.TeamID); err != nil {
			if isNoRows(err) {
				return ErrJoinRequestNotFound
			}
			return fmt.Errorf("platformstore: lock join request: %w", err)
		}
		if row.Status != "pending" && row.Status != "approved" {
			return ErrJoinRequestNotFound
		}
		if row.Status == "pending" && currentGen != row.Gen {
			return ErrJoinLinkInvalid
		}
		u, err := shareUserByID(ctx, tx, row.UserID)
		if err != nil {
			return err
		}
		if err := joinableUser(u); err != nil {
			return err
		}
		var memberStatus []string
		if err := tx.SelectContext(ctx, &memberStatus, `SELECT status FROM team_member
WHERE team_id = ? AND user_id = ? FOR UPDATE`, p.TeamID, row.UserID); err != nil {
			return fmt.Errorf("platformstore: approve membership: %w", err)
		}
		if len(memberStatus) > 0 && memberStatus[0] == "active" {
			// Already in the team (another request or an invite won): never re-stage, which would
			// reset the admins row's rights or demote an owner. Close a stale pending row and stop.
			if row.Status == "pending" {
				if _, err := tx.ExecContext(ctx, `UPDATE team_join_request SET status = 'closed', decided_at = ?, decided_by = ?
WHERE id = ?`, p.Now, p.Actor.UserID, p.RequestID); err != nil {
					return fmt.Errorf("platformstore: close stale join request: %w", err)
				}
			}
			r = joinReservation{state: JoinAlreadyMember}
			return nil
		}
		if len(memberStatus) > 0 && memberStatus[0] == "joining" && row.Status == "pending" {
			// Another join of this person is mid-way; this row is a duplicate.
			return ErrJoinRequestNotFound
		}
		if row.Status == "pending" {
			if _, err := tx.ExecContext(ctx, `UPDATE team_join_request SET status = 'approved', decided_at = ?, decided_by = ?
WHERE id = ?`, p.Now, p.Actor.UserID, p.RequestID); err != nil {
				return fmt.Errorf("platformstore: approve join request: %w", err)
			}
			if err := insertJoinAuditTx(ctx, tx, p.Actor.UserID, p.TeamID, "join_approve", u.Email.String, p.IP, p.Now); err != nil {
				return err
			}
		}
		role := p.Role
		if role != "owner" {
			role = "member"
		}
		inv := Invite{TeamID: p.TeamID, Role: role}
		if err := reserveMembershipTx(ctx, tx, inv, u.ID, p.Now); err != nil {
			return err
		}
		r = joinReservation{state: JoinJoined, email: u.Email.String, member: JoinMember{
			TeamID: p.TeamID, UserID: u.ID, Handle: u.Handle, Role: role, Permissions: p.Permissions,
			RequestID: p.RequestID, Gen: row.Gen,
		}}
		return nil
	})
	if err != nil {
		return JoinDecisionResult{}, err
	}
	return s.finishJoinReservation(ctx, JoinDecisionParams{
		TeamID: p.TeamID, UserID: r.member.UserID, IP: p.IP, Now: p.Now, RequestLink: p.ApprovedLink,
		Stage: p.Stage, Enable: p.Enable,
	}, r)
}

func (s *Store) DeclineJoinRequest(ctx context.Context, p JoinRequestDecisionParams) error {
	return s.inTx(ctx, func(tx *sqlx.Tx) error {
		st, err := shareTeamTx(ctx, tx, p.TeamID)
		if err != nil {
			return err
		}
		if st != "active" {
			if st == "suspended" {
				return ErrTeamReadOnly
			}
			return ErrJoinLinkInvalid
		}
		if err := checkActorTx(ctx, tx, p.TeamID, p.Actor); err != nil {
			return err
		}
		var email string
		if err := tx.GetContext(ctx, &email, `SELECT u.email FROM team_join_request r
JOIN platform_user u ON u.id = r.user_id
WHERE r.id = ? AND r.team_id = ? AND r.status = 'pending' FOR UPDATE`, p.RequestID, p.TeamID); err != nil {
			if isNoRows(err) {
				return ErrJoinRequestNotFound
			}
			return fmt.Errorf("platformstore: lock join request: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE team_join_request SET status = 'declined', decided_at = ?, decided_by = ?
WHERE id = ?`, p.Now, p.Actor.UserID, p.RequestID); err != nil {
			return fmt.Errorf("platformstore: decline join request: %w", err)
		}
		return insertJoinAuditTx(ctx, tx, p.Actor.UserID, p.TeamID, "join_decline", email, p.IP, p.Now)
	})
}

func notifyJoinRequestTx(ctx context.Context, tx *sqlx.Tx, teamID int64, email, link string, now time.Time) error {
	var recent int
	if err := tx.GetContext(ctx, &recent, `SELECT COUNT(*) FROM platform_email
WHERE team_id = ? AND template = 'join_request' AND created_at > ?`, teamID, now.Add(-time.Hour)); err != nil {
		return fmt.Errorf("platformstore: count join request letters: %w", err)
	}
	if recent > 0 {
		return nil
	}
	var team TeamBrief
	if err := tx.GetContext(ctx, &team, `SELECT id, slug, name, status, COALESCE(created_by, 0) AS created_by
FROM team WHERE id = ?`, teamID); err != nil {
		return fmt.Errorf("platformstore: join request team: %w", err)
	}
	var owners []string
	if err := tx.SelectContext(ctx, &owners, `SELECT u.email FROM team_member m
JOIN platform_user u ON u.id = m.user_id
WHERE m.team_id = ? AND m.role = 'owner' AND m.status = 'active'
  AND u.status = 'active' AND u.email IS NOT NULL ORDER BY u.id`, teamID); err != nil {
		return fmt.Errorf("platformstore: join request owners: %w", err)
	}
	payload, err := json.Marshal(teamLetterPayload{Link: link, TeamName: team.Name, Slug: team.Slug, Email: email})
	if err != nil {
		return fmt.Errorf("platformstore: join request letter payload: %w", err)
	}
	for _, to := range owners {
		if _, err := tx.ExecContext(ctx, `INSERT INTO platform_email
(to_email, template, payload, status, created_at, team_id) VALUES (?, 'join_request', ?, 'pending', ?, ?)`,
			to, string(payload), now, teamID); err != nil {
			return fmt.Errorf("platformstore: enqueue join request letter: %w", err)
		}
	}
	return nil
}

func insertJoinApprovedLetterTx(ctx context.Context, tx *sqlx.Tx, to string, teamID int64, link string, now time.Time) error {
	var team TeamBrief
	if err := tx.GetContext(ctx, &team, `SELECT id, slug, name, status, COALESCE(created_by, 0) AS created_by
FROM team WHERE id = ?`, teamID); err != nil {
		return fmt.Errorf("platformstore: approved join team: %w", err)
	}
	payload, err := json.Marshal(teamLetterPayload{Link: link, TeamName: team.Name, Slug: team.Slug})
	if err != nil {
		return fmt.Errorf("platformstore: approved join letter payload: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO platform_email
(to_email, template, payload, status, created_at, team_id) VALUES (?, 'join_approved', ?, 'pending', ?, ?)`,
		to, string(payload), now, teamID); err != nil {
		return fmt.Errorf("platformstore: enqueue approved join letter: %w", err)
	}
	return nil
}

func insertJoinAuditTx(ctx context.Context, tx *sqlx.Tx, actor, teamID int64, action, email, ip string, now time.Time) error {
	var detail any
	if email != "" {
		b, err := json.Marshal(map[string]string{"email": email})
		if err != nil {
			return fmt.Errorf("platformstore: join audit detail: %w", err)
		}
		detail = string(b)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO platform_audit
(actor_user_id, team_id, action, detail, ip, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
		nullID(actor), nullID(teamID), action, detail, nullableText(truncateIP(ip)), now); err != nil {
		return fmt.Errorf("platformstore: join audit: %w", err)
	}
	return nil
}

func truncateIP(ip string) string {
	ip = strings.TrimSpace(ip)
	if len(ip) > 45 {
		return ip[:45]
	}
	return ip
}

func nullableText(s string) any {
	if s == "" {
		return nil
	}
	return s
}
