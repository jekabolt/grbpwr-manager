package platformstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/jmoiron/sqlx"
)

// Identity errors (B3). Callers map every one of them to a generic answer; they never reach a client
// as text.
var (
	// ErrTokenInvalid: the link or code is unknown, wrong, used, expired, dead or locked.
	ErrTokenInvalid = errors.New("platformstore: invalid or expired token")
	// ErrSlugTaken: the workspace address is held by another team.
	ErrSlugTaken = errors.New("platformstore: address taken")
	// ErrHandleTaken: the chosen handle belongs to another user.
	ErrHandleTaken = errors.New("platformstore: handle taken")
	// ErrWrongPassword: an existing account's password did not match.
	ErrWrongPassword = errors.New("platformstore: wrong password")
	// ErrTeamNotReady: the team is still being provisioned.
	ErrTeamNotReady = errors.New("platformstore: team not ready")
	// ErrTeamReadOnly: the team is suspended.
	ErrTeamReadOnly = errors.New("platformstore: team read-only")
	// ErrTeamAwaitingApproval: the team is confirmed and waits for an operator's approval.
	ErrTeamAwaitingApproval = errors.New("platformstore: team awaiting approval")
	// ErrTermsRequired: an invite would create an account and the terms were not accepted.
	ErrTermsRequired = errors.New("platformstore: terms not accepted")
	// ErrAlreadyMember: the invite is for a person who is already an active member of the team.
	ErrAlreadyMember = errors.New("platformstore: already a member")
)

// User statuses (platform_user.status).
const (
	UserPending  = "pending"
	UserActive   = "active"
	UserDisabled = "disabled"
)

// maxHandleTries bounds the -2, -3 … search for a derived handle.
const maxHandleTries = 50

// User is one platform_user row.
type User struct {
	ID              int64          `db:"id"`
	Email           sql.NullString `db:"email"`
	Handle          string         `db:"handle"`
	PasswordHash    string         `db:"password_hash"`
	EmailVerifiedAt sql.NullTime   `db:"email_verified_at"`
	IsOperator      bool           `db:"is_operator"`
	Status          string         `db:"status"`
	CodeFailures    int            `db:"code_failures"`
	FailuresSince   sql.NullTime   `db:"code_failures_since"`
	LockedUntil     sql.NullTime   `db:"code_locked_until"`
}

const userColumns = `id, email, handle, password_hash, email_verified_at, is_operator, status, code_failures,
code_failures_since, code_locked_until`

// Counters is a user's code-failure state as the code path reads and writes it.
func (u User) Counters() CodeCounters {
	c := CodeCounters{Failures: u.CodeFailures}
	if u.FailuresSince.Valid {
		t := u.FailuresSince.Time
		c.Since = &t
	}
	if u.LockedUntil.Valid {
		t := u.LockedUntil.Time
		c.LockedUntil = &t
	}
	return c
}

// CodeCounters mirrors platform_user.code_failures, code_failures_since, code_locked_until.
type CodeCounters struct {
	Failures    int
	Since       *time.Time
	LockedUntil *time.Time
}

// NewToken is an email_token row to insert: the caller hashed its secrets already.
type NewToken struct {
	TeamID    int64 // 0 = none
	Purpose   string
	TokenHash string
	CodeHash  string // "" = no code (hand-off)
	ExpiresAt time.Time
}

// TeamBrief is what a letter needs to know about the team it is about.
type TeamBrief struct {
	ID        int64  `db:"id"`
	Slug      string `db:"slug"`
	Name      string `db:"name"`
	Status    string `db:"status"`
	CreatedBy int64  `db:"created_by"`
}

// MakeLetter builds the token row and the outbox letter for a user once its id is known (the code
// hash binds the user id). team is nil for a letter about no team (reset).
type MakeLetter func(userID int64, team *TeamBrief) (NewToken, Letter, error)

// UserByEmail returns the user with this (lowercase) email.
func (s *Store) UserByEmail(ctx context.Context, email string) (User, error) {
	return s.userWhere(ctx, s.db, `email = ?`, email)
}

// UserByHandle returns the user with this handle.
func (s *Store) UserByHandle(ctx context.Context, handle string) (User, error) {
	return s.userWhere(ctx, s.db, `handle = ?`, handle)
}

// UserByID returns the user with this id.
func (s *Store) UserByID(ctx context.Context, id int64) (User, error) {
	return s.userWhere(ctx, s.db, `id = ?`, id)
}

func (s *Store) userWhere(ctx context.Context, q sqlx.QueryerContext, where string, arg any) (User, error) {
	var u User
	if err := sqlx.GetContext(ctx, q, &u, `SELECT `+userColumns+` FROM platform_user WHERE `+where, arg); err != nil {
		if isNoRows(err) {
			return User{}, ErrUserNotFound
		}
		return User{}, fmt.Errorf("platformstore: read user: %w", err)
	}
	return u, nil
}

func lockUserByEmail(ctx context.Context, tx *sqlx.Tx, email string) (User, error) {
	var u User
	if err := tx.GetContext(ctx, &u, `SELECT `+userColumns+` FROM platform_user WHERE email = ? FOR UPDATE`, email); err != nil {
		if isNoRows(err) {
			return User{}, ErrUserNotFound
		}
		return User{}, fmt.Errorf("platformstore: lock user: %w", err)
	}
	return u, nil
}

func lockUserByID(ctx context.Context, tx *sqlx.Tx, id int64) (User, error) {
	var u User
	if err := tx.GetContext(ctx, &u, `SELECT `+userColumns+` FROM platform_user WHERE id = ? FOR UPDATE`, id); err != nil {
		if isNoRows(err) {
			return User{}, ErrUserNotFound
		}
		return User{}, fmt.Errorf("platformstore: lock user: %w", err)
	}
	return u, nil
}

// inTx runs fn in a transaction, committing on nil and rolling back otherwise.
func (s *Store) inTx(ctx context.Context, fn func(tx *sqlx.Tx) error) error {
	tx, err := s.db.BeginTxx(ctx, nil)
	if err != nil {
		return fmt.Errorf("platformstore: begin: %w", err)
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("platformstore: commit: %w", err)
	}
	return nil
}

func dupKey(err error, key string) bool {
	var me *mysql.MySQLError
	return errors.As(err, &me) && me.Number == 1062 && strings.Contains(me.Message, key)
}

// insertUserTx creates a user. With fixed, handle is used as is (ErrHandleTaken when held);
// otherwise handle is a base tried as base, base-2, base-3 …
// insertUserTx creates an account. terms is the terms version the person accepted now ("" = none:
// operators and team #1 accounts carry NULLs).
func insertUserTx(ctx context.Context, tx *sqlx.Tx, email, hash, status string, verifiedAt *time.Time, operator bool,
	handle string, fixed bool, terms string, now time.Time) (int64, string, error) {
	termsVersion, termsAt := termsColumns(terms, now)
	tries := maxHandleTries
	if fixed {
		tries = 1
	}
	for n := 1; n <= tries; n++ {
		cand := handle
		if n > 1 {
			cand = handle + "-" + strconv.Itoa(n)
		}
		res, err := tx.ExecContext(ctx, `INSERT INTO platform_user
(email, handle, password_hash, email_verified_at, is_operator, status, terms_version, terms_accepted_at, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			email, cand, hash, verifiedAt, operator, status, termsVersion, termsAt, now)
		if err == nil {
			id, err := res.LastInsertId()
			if err != nil {
				return 0, "", fmt.Errorf("platformstore: user id: %w", err)
			}
			return id, cand, nil
		}
		if dupKey(err, "uq_platform_user_handle") {
			continue
		}
		if dupKey(err, "uq_platform_user_email") {
			return 0, "", ErrTokenInvalid // a concurrent sign-up of the same email won
		}
		return 0, "", fmt.Errorf("platformstore: insert user: %w", err)
	}
	if fixed {
		return 0, "", ErrHandleTaken
	}
	return 0, "", fmt.Errorf("platformstore: no free handle for %q", handle)
}

// insertTokenTx inserts a token row. A verify or reset letter retires the user's older live rows of
// the same purpose; hand-offs live side by side (one per target team).
func insertTokenTx(ctx context.Context, tx *sqlx.Tx, userID int64, t NewToken, now time.Time) error {
	if t.Purpose != "handoff" {
		if _, err := tx.ExecContext(ctx, `UPDATE email_token SET used_at = ?
WHERE user_id = ? AND purpose = ? AND used_at IS NULL`, now, userID, t.Purpose); err != nil {
			return fmt.Errorf("platformstore: retire tokens: %w", err)
		}
	}
	var team, code any
	if t.TeamID > 0 {
		team = t.TeamID
	}
	if t.CodeHash != "" {
		code = t.CodeHash
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO email_token
(user_id, team_id, purpose, token_hash, code_hash, code_attempts, expires_at, created_at) VALUES (?, ?, ?, ?, ?, 0, ?, ?)`,
		userID, team, t.Purpose, t.TokenHash, code, t.ExpiresAt, now); err != nil {
		return fmt.Errorf("platformstore: insert token: %w", err)
	}
	return nil
}

// insertLetterTx writes the outbox row inside the caller's transaction: the letter exists exactly
// when its token does.
// termsColumns is the (terms_version, terms_accepted_at) pair for an acceptance of terms at now.
func termsColumns(terms string, now time.Time) (any, any) {
	if terms == "" {
		return nil, nil
	}
	return terms, now.UTC().Truncate(time.Second)
}

// teamLetterPayload is a team_request / team_approved letter: no code, a link that needs none.
type teamLetterPayload struct {
	Link     string `json:"link"`
	TeamName string `json:"team_name"`
	Slug     string `json:"slug"`
	Email    string `json:"email,omitempty"`
}

func insertTeamLetterTx(ctx context.Context, tx *sqlx.Tx, to, template string, p teamLetterPayload, now time.Time) error {
	payload, err := json.Marshal(p)
	if err != nil {
		return fmt.Errorf("platformstore: letter payload: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO platform_email (to_email, template, payload, status, created_at)
VALUES (?, ?, ?, 'pending', ?)`, to, template, string(payload), now); err != nil {
		return fmt.Errorf("platformstore: enqueue %s letter: %w", template, err)
	}
	return nil
}

// TeamApprovedLetterTx queues the team_approved letter to the team's owner (its creator) inside the
// caller's transaction — the console's approval writes it together with review → provisioning.
// link is the team's own address, https://<slug>.<base>/.
func TeamApprovedLetterTx(ctx context.Context, tx *sqlx.Tx, teamID int64, link string, now time.Time) error {
	var row struct {
		Slug  string         `db:"slug"`
		Name  string         `db:"name"`
		Email sql.NullString `db:"email"`
	}
	if err := tx.GetContext(ctx, &row, `SELECT t.slug, t.name, u.email FROM team t
JOIN platform_user u ON u.id = t.created_by WHERE t.id = ?`, teamID); err != nil {
		if isNoRows(err) {
			return ErrTokenInvalid
		}
		return fmt.Errorf("platformstore: team owner: %w", err)
	}
	if !row.Email.Valid || row.Email.String == "" {
		return nil // an owner without an email (team #1 handles) gets no letter
	}
	return insertTeamLetterTx(ctx, tx, row.Email.String, "team_approved",
		teamLetterPayload{Link: link, TeamName: row.Name, Slug: row.Slug}, now)
}

func insertLetterTx(ctx context.Context, tx *sqlx.Tx, l Letter, now time.Time) error {
	payload, err := json.Marshal(letterPayload{Link: l.Link, Code: l.Code, TeamName: l.TeamName, Slug: l.Slug, From: l.From})
	if err != nil {
		return fmt.Errorf("platformstore: letter payload: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO platform_email (to_email, template, payload, status, created_at)
VALUES (?, ?, ?, 'pending', ?)`, l.To, l.Template, string(payload), now); err != nil {
		return fmt.Errorf("platformstore: enqueue letter: %w", err)
	}
	return nil
}

func lettersSinceTx(ctx context.Context, tx *sqlx.Tx, email string, since time.Time) (int, error) {
	var n int
	if err := tx.GetContext(ctx, &n, `SELECT COUNT(*) FROM platform_email
WHERE to_email = ? AND template IN ('verify','invite','reset') AND created_at > ?`,
		email, since); err != nil {
		return 0, fmt.Errorf("platformstore: count letters: %w", err)
	}
	return n, nil
}

func sendLetterTx(ctx context.Context, tx *sqlx.Tx, userID int64, team *TeamBrief, mk MakeLetter, now time.Time) error {
	tok, l, err := mk(userID, team)
	if err != nil {
		return err
	}
	if err := insertTokenTx(ctx, tx, userID, tok, now); err != nil {
		return err
	}
	return insertLetterTx(ctx, tx, l, now)
}

// SlugTaken reports whether a workspace address is held: by a team that is not an expired pending
// one, or by a retired slug still in its redirect window.
func (s *Store) SlugTaken(ctx context.Context, slug string, now time.Time) (bool, error) {
	var n int
	if err := s.db.GetContext(ctx, &n, `SELECT
 (SELECT COUNT(*) FROM team WHERE slug = ? AND NOT (status = 'pending' AND pending_expires_at IS NOT NULL AND pending_expires_at < ?))
 + (SELECT COUNT(*) FROM team_slug_history WHERE slug = ? AND redirect_until > ?)`, slug, now, slug, now); err != nil {
		return false, fmt.Errorf("platformstore: slug taken: %w", err)
	}
	return n > 0, nil
}

// SignUpParams is one sheet submission. The caller validated every field and hashed the password.
type SignUpParams struct {
	Email, PasswordHash, TeamName, Slug, HandleBase string
	Now                                             time.Time
	PendingTTL                                      time.Duration
	MaxOwnedTeams, MaxLettersPerHour                int
	// PasswordMatches checks the typed password against an account's stored hash. "" must cost the
	// same as a real check and answer false (the caller compares against a dummy hash).
	PasswordMatches func(hash string) bool
	Letter          MakeLetter
	// TermsVersion is the terms version the person accepted on this sheet; stored on a new or
	// re-keyed pending account.
	TermsVersion string
}

// SignUpResult says what happened; the client sees the same empty answer either way.
type SignUpResult struct {
	Created bool  // a team and a letter were created
	UserID  int64 // the user the letter went to (0 when nothing was created)
	TeamID  int64
	Reason  string // why nothing was created (audit only)
}

// SignUp registers a person and a pending team and queues ONE verify letter, in one transaction.
//
// A pending (unverified) email signing up again replaces its pending teams; its password only when
// the typed one matches or no verify letter of it is still live (a stranger cannot re-key a sign-up
// in progress). An active account gets a new team only when the typed password is its password and
// it owns fewer than MaxOwnedTeams; otherwise nothing happens. ErrSlugTaken when the address is held
// — also for the pending owner of that address unless the typed password is its password.
func (s *Store) SignUp(ctx context.Context, p SignUpParams) (SignUpResult, error) {
	var res SignUpResult
	err := s.inTx(ctx, func(tx *sqlx.Tx) error {
		res = SignUpResult{}
		// The address first, before anything that depends on the email: a taken address answers the
		// same for a known and an unknown email, so it never tells whether an account exists.
		if _, err := tx.ExecContext(ctx, `DELETE FROM team WHERE slug = ? AND status = 'pending' AND pending_expires_at < ?`,
			p.Slug, p.Now); err != nil {
			return fmt.Errorf("platformstore: drop expired pending team: %w", err)
		}
		var held int
		if err := tx.GetContext(ctx, &held, `SELECT
 (SELECT COUNT(*) FROM team WHERE slug = ?) + (SELECT COUNT(*) FROM team_slug_history WHERE slug = ? AND redirect_until > ?)`,
			p.Slug, p.Slug, p.Now); err != nil {
			return fmt.Errorf("platformstore: slug held: %w", err)
		}
		if held > 0 {
			// A pending user re-signing up for its own pending address, with its password, gets it back
			// below. Everyone else — the same email with another password included — hears "taken",
			// after the same password check, so the answer and its cost never single the email out.
			var ownHash string
			err := tx.GetContext(ctx, &ownHash, `SELECT u.password_hash FROM team t JOIN platform_user u ON u.id = t.created_by
WHERE t.slug = ? AND t.status = 'pending' AND u.email = ? AND u.status = 'pending' LIMIT 1`, p.Slug, p.Email)
			if err != nil && !isNoRows(err) {
				return fmt.Errorf("platformstore: slug owner: %w", err)
			}
			if p.PasswordMatches == nil || !p.PasswordMatches(ownHash) {
				return ErrSlugTaken
			}
		}
		n, err := lettersSinceTx(ctx, tx, p.Email, p.Now.Add(-time.Hour))
		if err != nil {
			return err
		}
		if n >= p.MaxLettersPerHour {
			res.Reason = "letter_cap"
			return nil
		}
		u, err := lockUserByEmail(ctx, tx, p.Email)
		var uid int64
		switch {
		case errors.Is(err, ErrUserNotFound):
			uid, _, err = insertUserTx(ctx, tx, p.Email, p.PasswordHash, UserPending, nil, false, p.HandleBase, false, p.TermsVersion, p.Now)
			if errors.Is(err, ErrTokenInvalid) {
				res.Reason = "race"
				return nil
			}
			if err != nil {
				return err
			}
		case err != nil:
			return err
		case u.Status == UserPending:
			uid = u.ID
			if p.PasswordMatches == nil || !p.PasswordMatches(u.PasswordHash) {
				var live int
				if err := tx.GetContext(ctx, &live, `SELECT COUNT(*) FROM email_token
WHERE user_id = ? AND purpose = 'verify' AND used_at IS NULL AND expires_at > ?`, uid, p.Now); err != nil {
					return fmt.Errorf("platformstore: live verify letters: %w", err)
				}
				if live > 0 {
					res.Reason = "pending_password_mismatch"
					return nil
				}
			}
			termsVersion, termsAt := termsColumns(p.TermsVersion, p.Now)
			if _, err := tx.ExecContext(ctx, `UPDATE platform_user SET password_hash = ?,
terms_version = COALESCE(?, terms_version), terms_accepted_at = COALESCE(?, terms_accepted_at) WHERE id = ?`,
				p.PasswordHash, termsVersion, termsAt, uid); err != nil {
				return fmt.Errorf("platformstore: replace pending password: %w", err)
			}
			if _, err := tx.ExecContext(ctx, `DELETE FROM team WHERE created_by = ? AND status = 'pending'`, uid); err != nil {
				return fmt.Errorf("platformstore: drop pending teams: %w", err)
			}
		case u.Status == UserActive:
			if p.PasswordMatches == nil || !p.PasswordMatches(u.PasswordHash) {
				res.Reason = "existing_account"
				return nil
			}
			var owned int
			if err := tx.GetContext(ctx, &owned, `SELECT
 (SELECT COUNT(*) FROM team_member m JOIN team t ON t.id = m.team_id
   WHERE m.user_id = ? AND m.role = 'owner' AND m.status = 'active' AND t.status <> 'deleted')
 + (SELECT COUNT(*) FROM team WHERE created_by = ? AND status = 'pending' AND pending_expires_at > ?)`,
				u.ID, u.ID, p.Now); err != nil {
				return fmt.Errorf("platformstore: count owned teams: %w", err)
			}
			if owned >= p.MaxOwnedTeams {
				res.Reason = "team_cap"
				return nil
			}
			uid = u.ID
		default:
			res.Reason = "disabled"
			return nil
		}

		r, err := tx.ExecContext(ctx, `INSERT INTO team (slug, name, status, pending_expires_at, created_by, created_at)
VALUES (?, ?, 'pending', ?, ?, ?)`, p.Slug, p.TeamName, p.Now.Add(p.PendingTTL), uid, p.Now)
		if err != nil {
			if dupKey(err, "uq_team_slug") {
				return ErrSlugTaken
			}
			return fmt.Errorf("platformstore: insert team: %w", err)
		}
		tid, err := r.LastInsertId()
		if err != nil {
			return fmt.Errorf("platformstore: team id: %w", err)
		}
		team := &TeamBrief{ID: tid, Slug: p.Slug, Name: p.TeamName, Status: "pending", CreatedBy: uid}
		if err := sendLetterTx(ctx, tx, uid, team, p.Letter, p.Now); err != nil {
			return err
		}
		res = SignUpResult{Created: true, UserID: uid, TeamID: tid}
		return nil
	})
	return res, err
}

// ResendVerification queues a fresh verify letter for a pending account's latest live pending team.
// It reports whether a letter was queued (false: unknown, verified, no team, or the hourly cap).
func (s *Store) ResendVerification(ctx context.Context, email string, now time.Time, maxPerHour int, mk MakeLetter) (bool, error) {
	sent := false
	err := s.inTx(ctx, func(tx *sqlx.Tx) error {
		u, err := lockUserByEmail(ctx, tx, email)
		if errors.Is(err, ErrUserNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		if u.Status == UserDisabled {
			return nil
		}
		// A pending account, or a verified one adding a team: its newest live pending team.
		var team TeamBrief
		err = tx.GetContext(ctx, &team, `SELECT id, slug, name, status, created_by FROM team
WHERE created_by = ? AND status = 'pending' AND pending_expires_at > ? ORDER BY id DESC LIMIT 1`, u.ID, now)
		if isNoRows(err) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("platformstore: pending team: %w", err)
		}
		n, err := lettersSinceTx(ctx, tx, email, now.Add(-time.Hour))
		if err != nil {
			return err
		}
		if n >= maxPerHour {
			return nil
		}
		if err := sendLetterTx(ctx, tx, u.ID, &team, mk, now); err != nil {
			return err
		}
		sent = true
		return nil
	})
	return sent, err
}

// RequestPasswordReset queues a reset letter for an active account. It reports whether one was queued.
func (s *Store) RequestPasswordReset(ctx context.Context, email string, now time.Time, maxPerHour int, mk MakeLetter) (bool, error) {
	sent := false
	err := s.inTx(ctx, func(tx *sqlx.Tx) error {
		u, err := lockUserByEmail(ctx, tx, email)
		if errors.Is(err, ErrUserNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		if u.Status != UserActive {
			return nil
		}
		n, err := lettersSinceTx(ctx, tx, email, now.Add(-time.Hour))
		if err != nil {
			return err
		}
		if n >= maxPerHour {
			return nil
		}
		if err := sendLetterTx(ctx, tx, u.ID, nil, mk, now); err != nil {
			return err
		}
		sent = true
		return nil
	})
	return sent, err
}

// SyncOperators makes is_operator match the list: a listed email without an account gets an active
// user with an unusable password (the owner sets it through reset) and no team; everyone else is 0.
// It returns how many accounts it created.
func (s *Store) SyncOperators(ctx context.Context, emails []string, now time.Time, unusable func() (string, error),
	handleBase func(email string) string) (int, error) {
	created := 0
	err := s.inTx(ctx, func(tx *sqlx.Tx) error {
		created = 0
		for _, e := range emails {
			_, err := lockUserByEmail(ctx, tx, e)
			if err == nil {
				continue
			}
			if !errors.Is(err, ErrUserNotFound) {
				return err
			}
			h, err := unusable()
			if err != nil {
				return err
			}
			if _, _, err := insertUserTx(ctx, tx, e, h, UserActive, nil, true, handleBase(e), false, "", now); err != nil {
				return fmt.Errorf("platformstore: operator account: %w", err)
			}
			created++
		}
		if len(emails) == 0 {
			if _, err := tx.ExecContext(ctx, `UPDATE platform_user SET is_operator = 0 WHERE is_operator <> 0`); err != nil {
				return fmt.Errorf("platformstore: sync operators: %w", err)
			}
			return nil
		}
		q, args, err := sqlx.In(`UPDATE platform_user
SET is_operator = CASE WHEN email IS NOT NULL AND email IN (?) THEN 1 ELSE 0 END`, emails)
		if err != nil {
			return fmt.Errorf("platformstore: sync operators: %w", err)
		}
		if _, err := tx.ExecContext(ctx, q, args...); err != nil {
			return fmt.Errorf("platformstore: sync operators: %w", err)
		}
		return nil
	})
	return created, err
}

// AuditAuth records one identity event. detail must never carry a code, a token or a password.
func (s *Store) AuditAuth(ctx context.Context, actorUserID, teamID int64, action string, detail map[string]string, ip string) error {
	var actor, team, det, ipv any
	if actorUserID > 0 {
		actor = actorUserID
	}
	if teamID > 0 {
		team = teamID
	}
	if len(detail) > 0 {
		b, err := json.Marshal(detail)
		if err != nil {
			return fmt.Errorf("platformstore: audit detail: %w", err)
		}
		det = string(b)
	}
	if ip != "" {
		if len(ip) > 45 {
			ip = ip[:45]
		}
		ipv = ip
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO platform_audit (actor_user_id, team_id, action, detail, ip, created_at)
VALUES (?, ?, ?, ?, ?, UTC_TIMESTAMP(3))`, actor, team, action, det, ipv); err != nil {
		return fmt.Errorf("platformstore: audit: %w", err)
	}
	return nil
}
