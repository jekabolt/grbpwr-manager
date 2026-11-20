package platformstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/jmoiron/sqlx"
)

// ErrUsernameTaken: another account holds this @username, as its handle or as its public username
// (one shared namespace, P0 §3.1).
var ErrUsernameTaken = errors.New("platformstore: username taken")

// maxProfileTries bounds UpdateProfile's retries after InnoDB picked it as a deadlock victim.
const maxProfileTries = 3

// Profile is the account as its owner sees it in the profile (P0 §1.2).
type Profile struct {
	UserID    int64
	Handle    string
	FirstName string
	LastName  string
	// PublicUsername is the effective @username: the handle when none is set.
	PublicUsername    string
	Email             string
	PasswordHash      string
	PasswordChangedAt sql.NullTime
	// GoogleEmail is the connected Google identity's email ("" = not connected).
	GoogleEmail string
	// AvatarSHA is the avatar's sha256 ("" = no avatar).
	AvatarSHA string
}

type profileRow struct {
	ID                int64          `db:"id"`
	Handle            string         `db:"handle"`
	FirstName         string         `db:"first_name"`
	LastName          string         `db:"last_name"`
	PublicUsername    sql.NullString `db:"public_username"`
	Email             sql.NullString `db:"email"`
	PasswordHash      string         `db:"password_hash"`
	PasswordChangedAt sql.NullTime   `db:"password_changed_at"`
	Status            string         `db:"status"`
	AvatarSHA         sql.NullString `db:"avatar_sha"`
}

const profileSelect = `SELECT u.id, u.handle, u.first_name, u.last_name, u.public_username, u.email, u.password_hash,
u.password_changed_at, u.status, a.sha256 AS avatar_sha
FROM platform_user u LEFT JOIN account_avatar a ON a.user_id = u.id
WHERE u.id = ?`

// effectiveUsername is the @username a person shows: the public one, else the handle.
func effectiveUsername(handle string, public sql.NullString) string {
	if public.Valid && public.String != "" {
		return public.String
	}
	return handle
}

// Profile reads an active account's profile. ErrUserNotFound when the account is missing or not active.
func (s *Store) Profile(ctx context.Context, userID int64) (Profile, error) {
	var r profileRow
	if err := s.db.GetContext(ctx, &r, profileSelect, userID); err != nil {
		if isNoRows(err) {
			return Profile{}, ErrUserNotFound
		}
		return Profile{}, fmt.Errorf("platformstore: read profile: %w", err)
	}
	if r.Status != UserActive {
		return Profile{}, ErrUserNotFound
	}
	var google []sql.NullString
	if err := s.db.SelectContext(ctx, &google, `SELECT email FROM platform_identity
WHERE user_id = ? AND provider = 'google' ORDER BY id LIMIT 1`, userID); err != nil {
		return Profile{}, fmt.Errorf("platformstore: read google identity: %w", err)
	}
	p := Profile{
		UserID:            r.ID,
		Handle:            r.Handle,
		FirstName:         r.FirstName,
		LastName:          r.LastName,
		PublicUsername:    effectiveUsername(r.Handle, r.PublicUsername),
		Email:             r.Email.String,
		PasswordHash:      r.PasswordHash,
		PasswordChangedAt: r.PasswordChangedAt,
		AvatarSHA:         r.AvatarSHA.String,
	}
	if len(google) > 0 {
		p.GoogleEmail = google[0].String
	}
	return p, nil
}

// UpdateProfileParams is an UpdateMyProfile the caller already validated: names trimmed and within
// their limits, the username normalised and allowed.
type UpdateProfileParams struct {
	UserID              int64
	FirstName, LastName string
	// PublicUsername is the new @username; "" keeps the current one.
	PublicUsername string
}

// UpdateProfile sets an active account's names and @username in one transaction that locks both
// namespaces (P0 §8): the account's own row, then the handle and the public username equal to the
// new @username, each FOR UPDATE. Another account holding it either way → ErrUsernameTaken; the
// account's own handle stores NULL (the @username is the handle again). The unique key settles two
// renames racing to the same value. A lost lock race is retried, then ErrTryAgain.
// ErrUserNotFound when the account is missing or not active.
func (s *Store) UpdateProfile(ctx context.Context, p UpdateProfileParams) error {
	var err error
	for range maxProfileTries {
		err = lockConflict(s.inTx(ctx, func(tx *sqlx.Tx) error { return updateProfileTx(ctx, tx, p) }))
		if !errors.Is(err, ErrTryAgain) {
			return err
		}
	}
	return err
}

func updateProfileTx(ctx context.Context, tx *sqlx.Tx, p UpdateProfileParams) error {
	u, err := lockUserByID(ctx, tx, p.UserID)
	if err != nil {
		return err
	}
	if u.Status != UserActive {
		return ErrUserNotFound
	}
	if p.PublicUsername == "" {
		if _, err := tx.ExecContext(ctx, `UPDATE platform_user SET first_name = ?, last_name = ? WHERE id = ?`,
			p.FirstName, p.LastName, p.UserID); err != nil {
			return fmt.Errorf("platformstore: update names: %w", err)
		}
		return nil
	}
	var public any // NULL = the handle
	if p.PublicUsername != u.Handle {
		var byHandle []int64
		if err := tx.SelectContext(ctx, &byHandle, `SELECT id FROM platform_user WHERE handle = ? FOR UPDATE`,
			p.PublicUsername); err != nil {
			return fmt.Errorf("platformstore: lock handle namespace: %w", err)
		}
		var byPublic []int64
		if err := tx.SelectContext(ctx, &byPublic, `SELECT id FROM platform_user WHERE public_username = ? FOR UPDATE`,
			p.PublicUsername); err != nil {
			return fmt.Errorf("platformstore: lock username namespace: %w", err)
		}
		for _, id := range append(byHandle, byPublic...) {
			if id != p.UserID {
				return ErrUsernameTaken
			}
		}
		public = p.PublicUsername
	}
	if _, err := tx.ExecContext(ctx, `UPDATE platform_user SET first_name = ?, last_name = ?, public_username = ?
WHERE id = ?`, p.FirstName, p.LastName, public, p.UserID); err != nil {
		if dupKey(err, "uq_platform_user_public_username") {
			return ErrUsernameTaken
		}
		return fmt.Errorf("platformstore: update profile: %w", err)
	}
	return nil
}

// publicUsernameHeldTx reports whether some account holds name as its public username, with a
// locking read: it sees a rename committed a moment ago, which a consistent read in an older
// snapshot would miss. The handle allocators call it right after inserting the handle, so their
// lock order (handle, then public username) is UpdateProfile's.
func publicUsernameHeldTx(ctx context.Context, tx *sqlx.Tx, name string) (bool, error) {
	var n int
	if err := tx.GetContext(ctx, &n, `SELECT COUNT(*) FROM platform_user WHERE public_username = ? FOR SHARE`, name); err != nil {
		return false, fmt.Errorf("platformstore: read username namespace: %w", err)
	}
	return n > 0, nil
}

// UserByPublicUsername returns the user whose @username is name (sign in with @username, P0 §3.1).
// The column's collation equates accents and case, so a match must also be byte-exact.
func (s *Store) UserByPublicUsername(ctx context.Context, name string) (User, error) {
	if name == "" {
		return User{}, ErrUserNotFound
	}
	var rows []struct {
		ID     int64  `db:"id"`
		Public string `db:"public_username"`
	}
	if err := s.db.SelectContext(ctx, &rows, `SELECT id, public_username FROM platform_user WHERE public_username = ?`, name); err != nil {
		return User{}, fmt.Errorf("platformstore: user by username: %w", err)
	}
	for _, r := range rows {
		if r.Public == name {
			return s.UserByID(ctx, r.ID)
		}
	}
	return User{}, ErrUserNotFound
}

// Person is how one account shows in every person row (name, @username, photo; P0 §3.1).
type Person struct {
	UserID         int64
	Handle         string
	FirstName      string
	LastName       string
	PublicUsername string // effective: the handle when none is set
	AvatarSHA      string // "" = no avatar
}

type personRow struct {
	ID             int64          `db:"id"`
	Handle         string         `db:"handle"`
	FirstName      string         `db:"first_name"`
	LastName       string         `db:"last_name"`
	PublicUsername sql.NullString `db:"public_username"`
	AvatarSHA      sql.NullString `db:"avatar_sha"`
}

func (r personRow) person() Person {
	return Person{
		UserID:         r.ID,
		Handle:         r.Handle,
		FirstName:      r.FirstName,
		LastName:       r.LastName,
		PublicUsername: effectiveUsername(r.Handle, r.PublicUsername),
		AvatarSHA:      r.AvatarSHA.String,
	}
}

// maxPeopleBatch bounds one IN list of PeopleByHandles.
const maxPeopleBatch = 500

// PeopleByHandles resolves handles to their display (handle → Person); unknown handles are absent.
func (s *Store) PeopleByHandles(ctx context.Context, handles []string) (map[string]Person, error) {
	out := make(map[string]Person, len(handles))
	seen := make(map[string]bool, len(handles))
	uniq := make([]string, 0, len(handles))
	for _, h := range handles {
		if h != "" && !seen[h] {
			seen[h] = true
			uniq = append(uniq, h)
		}
	}
	for start := 0; start < len(uniq); start += maxPeopleBatch {
		batch := uniq[start:min(start+maxPeopleBatch, len(uniq))]
		q, args, err := sqlx.In(`SELECT u.id, u.handle, u.first_name, u.last_name, u.public_username, a.sha256 AS avatar_sha
FROM platform_user u LEFT JOIN account_avatar a ON a.user_id = u.id
WHERE u.handle IN (?)`, batch)
		if err != nil {
			return nil, fmt.Errorf("platformstore: people: %w", err)
		}
		var rows []personRow
		if err := s.db.SelectContext(ctx, &rows, s.db.Rebind(q), args...); err != nil {
			return nil, fmt.Errorf("platformstore: people: %w", err)
		}
		for _, r := range rows {
			if seen[r.Handle] { // the collation equates case and accents; keep exact keys only
				out[r.Handle] = r.person()
			}
		}
	}
	return out, nil
}

// PeopleByIDs is PeopleByHandles keyed by account id.
func (s *Store) PeopleByIDs(ctx context.Context, ids []int64) (map[int64]Person, error) {
	out := make(map[int64]Person, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	q, args, err := sqlx.In(`SELECT u.id, u.handle, u.first_name, u.last_name, u.public_username, a.sha256 AS avatar_sha
FROM platform_user u LEFT JOIN account_avatar a ON a.user_id = u.id
WHERE u.id IN (?)`, ids)
	if err != nil {
		return nil, fmt.Errorf("platformstore: people by id: %w", err)
	}
	var rows []personRow
	if err := s.db.SelectContext(ctx, &rows, s.db.Rebind(q), args...); err != nil {
		return nil, fmt.Errorf("platformstore: people by id: %w", err)
	}
	for _, r := range rows {
		out[r.ID] = r.person()
	}
	return out, nil
}

// setNamesIfEmptyTx seeds an account's names (an invite or a sign-up form typed them) unless the
// account already has a name: a name its owner chose is never overwritten by someone else's guess.
func setNamesIfEmptyTx(ctx context.Context, tx *sqlx.Tx, userID int64, first, last string) error {
	if first == "" && last == "" {
		return nil
	}
	if _, err := tx.ExecContext(ctx, `UPDATE platform_user SET first_name = ?, last_name = ?
WHERE id = ? AND first_name = '' AND last_name = ''`, first, last, userID); err != nil {
		return fmt.Errorf("platformstore: seed names: %w", err)
	}
	return nil
}

// DisplayName is how a person's name shows (P0 §1.1): trim(first + ' ' + last), "" when both are empty.
func DisplayName(first, last string) string {
	return strings.TrimSpace(strings.TrimSpace(first) + " " + strings.TrimSpace(last))
}
