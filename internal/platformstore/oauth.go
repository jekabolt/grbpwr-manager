package platformstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jmoiron/sqlx"
)

// Provider sign-in (teams O §1, §2, §12). Nothing here talks to a provider: the callback (lane O3)
// verifies the id_token and hands the result to CompleteProviderSignIn.

// OAuthStateTTL is how long a started provider flow may take before its row is dead.
const OAuthStateTTL = 10 * time.Minute

// Providers and state targets (platform_identity.provider, oauth_state.provider / target).
const (
	ProviderGoogle = "google"
	ProviderApple  = "apple"

	OAuthTargetSignIn = "signin"
	OAuthTargetInvite = "invite"
	// OAuthTargetLink connects Google to the signed-in account a link ticket names (P0 §3.7).
	OAuthTargetLink = "link"
)

// Provider sign-in refusals. The caller maps each to its fixed key; nothing reaches a client as text.
var (
	// ErrOAuthStateInvalid: the state row is unknown, used or expired — one answer for all three.
	ErrOAuthStateInvalid = errors.New("platformstore: oauth state invalid")
	// ErrProviderUnverified: no account is linked yet and the provider's email cannot be trusted for
	// one (empty, not verified, or not authoritative). Key unverified.
	ErrProviderUnverified = errors.New("platformstore: provider email not usable")
	// ErrProviderDisabled: the account is disabled. Key disabled.
	ErrProviderDisabled = errors.New("platformstore: account disabled")
	// ErrProviderOperator: the account is an operator's, whose way in is the password only. Key
	// use_password. Nothing was written.
	ErrProviderOperator = errors.New("platformstore: operator account, password only")

	errIdentityRace = errors.New("platformstore: identity inserted concurrently")
)

// OAuthState is one provider flow in flight. Hashes are hex sha256 of the secrets the caller keeps
// (the state value in the URL, the HttpOnly cookie); the verifier and the nonce are stored as they
// are because the callback needs them back.
type OAuthState struct {
	StateHash    string
	BinderHash   string
	Provider     string // ProviderGoogle | ProviderApple
	Target       string // OAuthTargetSignIn | OAuthTargetInvite | OAuthTargetLink
	PKCEVerifier string
	Nonce        string
	InviteHash   string // the invite token's hash, target invite only
	TermsVersion string // the terms version accepted on the invite sheet, "" = none
	// LinkUserID is the account a Google connect goes to and ReturnSlug the team host it returns
	// to, target link only.
	LinkUserID int64
	ReturnSlug string
	ExpiresAt  time.Time
}

type oauthStateRow struct {
	StateHash    string         `db:"state_hash"`
	BinderHash   string         `db:"binder_hash"`
	Provider     string         `db:"provider"`
	Target       string         `db:"target"`
	PKCEVerifier string         `db:"pkce_verifier"`
	Nonce        string         `db:"nonce"`
	InviteHash   sql.NullString `db:"invite_hash"`
	TermsVersion sql.NullString `db:"terms_version"`
	LinkUserID   sql.NullInt64  `db:"link_user_id"`
	ReturnSlug   sql.NullString `db:"return_slug"`
	ExpiresAt    time.Time      `db:"expires_at"`
}

// CreateOAuthState stores a started flow, live for OAuthStateTTL from now. Rows that died more than
// an hour ago are cleared first (a bounded delete in its own statement).
func (s *Store) CreateOAuthState(ctx context.Context, st OAuthState, now time.Time) error {
	now = now.UTC()
	switch {
	case len(st.StateHash) != 64 || len(st.BinderHash) != 64:
		return errors.New("platformstore: oauth state needs state and binder hashes")
	case st.Provider != ProviderGoogle && st.Provider != ProviderApple:
		return fmt.Errorf("platformstore: unknown provider %q", st.Provider)
	case st.Target != OAuthTargetSignIn && st.Target != OAuthTargetInvite && st.Target != OAuthTargetLink:
		return fmt.Errorf("platformstore: unknown oauth target %q", st.Target)
	case (st.Target == OAuthTargetInvite) != (st.InviteHash != ""):
		return errors.New("platformstore: an invite hash goes with the invite target only")
	case (st.Target == OAuthTargetLink) != (st.LinkUserID > 0) || (st.Target != OAuthTargetLink && st.ReturnSlug != ""):
		return errors.New("platformstore: an account and its return go with the link target only")
	case st.Target == OAuthTargetLink && st.Provider != ProviderGoogle:
		return errors.New("platformstore: only google connects to an account")
	case len(st.ReturnSlug) > 63:
		return errors.New("platformstore: return slug too long")
	case st.PKCEVerifier == "" || len(st.PKCEVerifier) > 128 || st.Nonce == "" || len(st.Nonce) > 64:
		return errors.New("platformstore: oauth state needs a verifier and a nonce")
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM oauth_state WHERE expires_at < ? LIMIT 500`, now.Add(-time.Hour)); err != nil {
		return fmt.Errorf("platformstore: clear old oauth states: %w", err)
	}
	var invite, terms, linkUser, returnSlug any
	if st.InviteHash != "" {
		invite = st.InviteHash
	}
	if st.TermsVersion != "" {
		terms = st.TermsVersion
	}
	if st.LinkUserID > 0 {
		linkUser, returnSlug = st.LinkUserID, st.ReturnSlug
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO oauth_state
(state_hash, binder_hash, provider, target, pkce_verifier, nonce, invite_hash, terms_version, link_user_id, return_slug,
created_at, expires_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, st.StateHash, st.BinderHash, st.Provider, st.Target, st.PKCEVerifier, st.Nonce,
		invite, terms, linkUser, returnSlug, now, now.Add(OAuthStateTTL)); err != nil {
		return fmt.Errorf("platformstore: create oauth state: %w", err)
	}
	return nil
}

// ConsumeOAuthState spends a flow exactly once: the single UPDATE that marks it used succeeds for
// one caller only, while the row is unused and unexpired. A second consume, an expired row and an
// unknown state all answer ErrOAuthStateInvalid. The caller compares BinderHash with its cookie's
// hash (constant time) after this returns.
func (s *Store) ConsumeOAuthState(ctx context.Context, stateHash string, now time.Time) (OAuthState, error) {
	now = now.UTC()
	if len(stateHash) != 64 {
		return OAuthState{}, ErrOAuthStateInvalid
	}
	r, err := s.db.ExecContext(ctx, `UPDATE oauth_state SET used_at = ?
WHERE state_hash = ? AND used_at IS NULL AND expires_at > ?`, now, stateHash, now)
	if err != nil {
		return OAuthState{}, fmt.Errorf("platformstore: consume oauth state: %w", err)
	}
	if n, err := r.RowsAffected(); err != nil {
		return OAuthState{}, fmt.Errorf("platformstore: consume oauth state: %w", err)
	} else if n != 1 {
		return OAuthState{}, ErrOAuthStateInvalid
	}
	var row oauthStateRow
	if err := s.db.GetContext(ctx, &row, `SELECT state_hash, binder_hash, provider, target, pkce_verifier, nonce,
invite_hash, terms_version, link_user_id, return_slug, expires_at FROM oauth_state WHERE state_hash = ?`, stateHash); err != nil {
		if isNoRows(err) {
			return OAuthState{}, ErrOAuthStateInvalid
		}
		return OAuthState{}, fmt.Errorf("platformstore: read oauth state: %w", err)
	}
	return OAuthState{
		StateHash: row.StateHash, BinderHash: row.BinderHash, Provider: row.Provider, Target: row.Target,
		PKCEVerifier: row.PKCEVerifier, Nonce: row.Nonce, InviteHash: row.InviteHash.String,
		TermsVersion: row.TermsVersion.String, LinkUserID: row.LinkUserID.Int64, ReturnSlug: row.ReturnSlug.String,
		ExpiresAt: row.ExpiresAt,
	}, nil
}

// ProviderSignIn is what a verified id_token says, as the callback hands it over. It is this
// package's own type: the provider package is never imported here.
type ProviderSignIn struct {
	Provider string // ProviderGoogle | ProviderApple
	Subject  string // the provider's stable user id (sub)
	Email    string // as the provider gave it; "" = none
	// EmailVerified: the provider says the address was verified.
	EmailVerified bool
	// EmailAuthoritative: the provider vouches the address belongs to this person now (Google: a
	// Gmail address or a Workspace hd claim; Apple: any verified address).
	EmailAuthoritative bool
	// PrivateEmail: an Apple relay address.
	PrivateEmail bool
}

// ProviderParams is what the completion needs besides the provider's word.
type ProviderParams struct {
	Now time.Time
	// Email is ProviderSignIn.Email normalised by the caller (lowercase, validated); "" when it is
	// absent or not a usable address.
	Email string
	// HandleBase is the handle a new account starts from (derived from Email by the caller).
	HandleBase string
	// UnusableHash makes a password hash nobody can match: a new account's, and the claimed pending
	// account's.
	UnusableHash func() (string, error)
	// Code is the sign-in code row (purpose signin, hashed, 60 s) inserted when the person gets in.
	Code NewToken
}

// ProviderResult is a completed provider sign-in: the account and what happened to it.
type ProviderResult struct {
	UserID int64
	// Created: a new account was made; Linked: the identity was added to an existing account
	// (Claimed: that account was pending and is now active, its pending teams gone).
	Created, Linked, Claimed bool
	// SIDs are sessions the claim revoked (the caller drops them from the edge cache).
	SIDs []string
}

// CompleteProviderSignIn is the one completion of a provider callback (teams O §2, §12, and the
// coordinator's EmailAuthoritative rule), in one transaction:
//
//   - a known identity (provider, subject) → its account, whatever the email says now; an operator's
//     account → ErrProviderOperator, a disabled one → ErrProviderDisabled (anything else that is not
//     active: ErrTokenInvalid);
//   - no identity yet: an empty email, or one not verified or not authoritative → ErrProviderUnverified
//     before any lookup; else the account by email: none → a new one (active, verified now,
//     unusable hash, no team, no terms) with the identity; an operator's → ErrProviderOperator;
//     disabled → ErrProviderDisabled; active → the identity is added, the password untouched;
//     pending → the pre-hijack claim: its pending teams deleted with their logos, the password
//     made unusable through changeCredentialsTx (sessions revoked, hand-off and sign-in codes
//     expired), live verify / reset letters expired, status active, email verified now, the
//     identity added, audit oauth_claimed_pending.
//
// On success the sign-in code p.Code is inserted for the account (it opens a root session once,
// within its 60 s). Every refusal writes nothing. A concurrent first sign-in of the same identity
// is retried once and then lands on the known-identity path.
func (s *Store) CompleteProviderSignIn(ctx context.Context, in ProviderSignIn, p ProviderParams) (ProviderResult, error) {
	if (in.Provider != ProviderGoogle && in.Provider != ProviderApple) || in.Subject == "" || len(in.Subject) > 255 {
		return ProviderResult{}, fmt.Errorf("platformstore: provider sign-in needs a provider and a subject")
	}
	p.Now = p.Now.UTC()
	p.Code.Purpose = "signin"
	p.Code.CodeHash = ""
	p.Code.ParentSID = ""
	if p.Code.TokenHash == "" || !p.Code.ExpiresAt.After(p.Now) {
		return ProviderResult{}, errors.New("platformstore: provider sign-in needs a live code")
	}
	res, err := s.completeProvider(ctx, in, p)
	if errors.Is(err, errIdentityRace) {
		res, err = s.completeProvider(ctx, in, p)
	}
	if errors.Is(err, errIdentityRace) {
		return ProviderResult{}, fmt.Errorf("platformstore: identity race did not settle: %w", err)
	}
	return res, err
}

func (s *Store) completeProvider(ctx context.Context, in ProviderSignIn, p ProviderParams) (ProviderResult, error) {
	var known []struct {
		UserID  int64  `db:"user_id"`
		Subject string `db:"subject"`
	}
	if err := s.db.SelectContext(ctx, &known, `SELECT user_id, subject FROM platform_identity WHERE provider = ? AND subject = ?`,
		in.Provider, in.Subject); err != nil {
		return ProviderResult{}, fmt.Errorf("platformstore: find identity: %w", err)
	}
	for _, identity := range known {
		if identity.Subject == in.Subject {
			return s.signInKnownIdentity(ctx, in, identity.UserID, p)
		}
	}
	if p.Email == "" || strings.TrimSpace(in.Email) == "" || !in.EmailVerified || !in.EmailAuthoritative {
		return ProviderResult{}, ErrProviderUnverified
	}
	var res ProviderResult
	var swept []int64
	err := s.inTx(ctx, func(tx *sqlx.Tx) error {
		res, swept = ProviderResult{}, nil
		u, err := lockUserByEmail(ctx, tx, p.Email)
		switch {
		case errors.Is(err, ErrUserNotFound):
			hash, err := p.UnusableHash()
			if err != nil {
				return err
			}
			now := p.Now
			uid, _, err := insertUserTx(ctx, tx, p.Email, hash, UserActive, &now, false, p.HandleBase, false, "", "", "", p.Now)
			if errors.Is(err, ErrTokenInvalid) {
				return errIdentityRace // the same email was created a moment ago: start over
			}
			if err != nil {
				return err
			}
			res = ProviderResult{UserID: uid, Created: true}
		case err != nil:
			return err
		case u.IsOperator:
			return ErrProviderOperator
		case u.Status == UserDisabled:
			return ErrProviderDisabled
		case u.Status == UserDeleted:
			return deletedAccountErr(u, p.Now)
		case u.Status == UserActive:
			res = ProviderResult{UserID: u.ID, Linked: true}
		case u.Status == UserPending:
			var cerr error
			res.SIDs, swept, cerr = claimPendingTx(ctx, tx, u.ID, p)
			if cerr != nil {
				return cerr
			}
			res.UserID, res.Linked, res.Claimed = u.ID, true, true
		default:
			return ErrProviderDisabled
		}
		if err := insertIdentityTx(ctx, tx, res.UserID, in, p.Now); err != nil {
			return err
		}
		return insertTokenTx(ctx, tx, res.UserID, p.Code, p.Now)
	})
	for _, id := range swept {
		s.dir.Invalidate(id)
	}
	if err != nil {
		return ProviderResult{}, err
	}
	return res, nil
}

// signInKnownIdentity is a returning identity: its account is read FOR SHARE (a password change,
// which locks it FOR UPDATE, then expires the new code), the identity's last sign-in is stamped and
// the code inserted.
func (s *Store) signInKnownIdentity(ctx context.Context, in ProviderSignIn, uid int64, p ProviderParams) (ProviderResult, error) {
	err := s.inTx(ctx, func(tx *sqlx.Tx) error {
		u, err := shareUserByID(ctx, tx, uid)
		if errors.Is(err, ErrUserNotFound) {
			return ErrTokenInvalid
		}
		if err != nil {
			return err
		}
		switch {
		case u.IsOperator:
			return ErrProviderOperator
		case u.Status == UserDisabled:
			return ErrProviderDisabled
		case u.Status == UserDeleted:
			return deletedAccountErr(u, p.Now)
		case u.Status != UserActive:
			return ErrTokenInvalid
		}
		if _, err := tx.ExecContext(ctx, `UPDATE platform_identity SET last_login_at = ? WHERE provider = ? AND subject = ? AND user_id = ?`,
			p.Now, in.Provider, in.Subject, uid); err != nil {
			return fmt.Errorf("platformstore: stamp identity: %w", err)
		}
		return insertTokenTx(ctx, tx, uid, p.Code, p.Now)
	})
	if err != nil {
		return ProviderResult{}, err
	}
	return ProviderResult{UserID: uid}, nil
}

// insertIdentityTx links (provider, subject) to the account the caller locked. A duplicate key is a
// concurrent first sign-in of the same identity: the whole transaction starts over.
func insertIdentityTx(ctx context.Context, tx *sqlx.Tx, userID int64, in ProviderSignIn, now time.Time) error {
	var email any
	if e := strings.TrimSpace(in.Email); e != "" && len(e) <= 254 {
		email = strings.ToLower(e)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO platform_identity (user_id, provider, subject, email, private_email, created_at, last_login_at)
VALUES (?, ?, ?, ?, ?, ?, ?)`, userID, in.Provider, in.Subject, email, in.PrivateEmail, now, now); err != nil {
		if dupKey(err, "uq_platform_identity_subject") {
			return errIdentityRace
		}
		return fmt.Errorf("platformstore: insert identity: %w", err)
	}
	return nil
}

// claimPendingTx is the pre-hijack claim of a pending account whose email the provider vouches
// for, inside the caller's transaction that holds the account FOR UPDATE. Its pending teams go
// first (with their logos; 2 before 1, see the lock order at DeleteTeam), then the credentials:
// whoever typed the password of an unconfirmed sign-up loses it.
func claimPendingTx(ctx context.Context, tx *sqlx.Tx, userID int64, p ProviderParams) ([]string, []int64, error) {
	teams, err := deletePendingTeamsTx(ctx, tx, `created_by = ? AND status = 'pending'`, userID)
	if err != nil {
		return nil, nil, fmt.Errorf("platformstore: claim: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM platform_join_intent WHERE user_id = ?`, userID); err != nil {
		return nil, nil, fmt.Errorf("platformstore: claim: clear join intent: %w", err)
	}
	hash, err := p.UnusableHash()
	if err != nil {
		return nil, nil, err
	}
	c, err := changeCredentialsTx(ctx, tx, userID, hash, p.Now)
	if err != nil {
		return nil, nil, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE email_token SET used_at = ?
WHERE user_id = ? AND purpose IN ('verify', 'reset') AND used_at IS NULL`, p.Now, userID); err != nil {
		return nil, nil, fmt.Errorf("platformstore: claim: expire letters: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE platform_user SET status = 'active', email_verified_at = ?,
code_failures = 0, code_failures_since = NULL, code_locked_until = NULL WHERE id = ? AND status = 'pending'`, p.Now, userID); err != nil {
		return nil, nil, fmt.Errorf("platformstore: claim: activate: %w", err)
	}
	detail, err := json.Marshal(map[string]any{"pending_teams_removed": len(teams)})
	if err != nil {
		return nil, nil, fmt.Errorf("platformstore: claim: audit detail: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO platform_audit (actor_user_id, team_id, action, detail, created_at)
VALUES (?, NULL, 'oauth_claimed_pending', ?, UTC_TIMESTAMP(3))`, userID, string(detail)); err != nil {
		return nil, nil, fmt.Errorf("platformstore: claim: audit: %w", err)
	}
	return c.SIDs, teams, nil
}

// IssueSignInCode inserts a sign-in code for an active account outside a provider completion (the
// invite path of lane O3, after it accepted the invite): enterTeamID > 0 makes the exchange answer
// that team's address as enter_team_slug. ErrTokenInvalid when the account is not active.
func (s *Store) IssueSignInCode(ctx context.Context, userID, enterTeamID int64, t NewToken, now time.Time) error {
	t.Purpose, t.CodeHash, t.ParentSID, t.TeamID = "signin", "", "", enterTeamID
	now = now.UTC()
	if t.TokenHash == "" || !t.ExpiresAt.After(now) {
		return errors.New("platformstore: a sign-in code needs a live hash")
	}
	return s.inTx(ctx, func(tx *sqlx.Tx) error {
		if enterTeamID > 0 {
			if _, err := shareTeamTx(ctx, tx, enterTeamID); err != nil {
				return err
			}
		}
		u, err := shareUserByID(ctx, tx, userID)
		if errors.Is(err, ErrUserNotFound) {
			return ErrTokenInvalid
		}
		if err != nil {
			return err
		}
		if u.Status != UserActive || u.IsOperator {
			return ErrTokenInvalid
		}
		return insertTokenTx(ctx, tx, userID, t, now)
	})
}

// SignInExchange is an exchanged sign-in code: the account (its root session is open) and, when the
// code was issued after an invite, the team to enter (0 = none).
type SignInExchange struct {
	User        User
	EnterTeamID int64
}

// ExchangeSignInCode spends a sign-in code once, within its lifetime, and opens the root session
// sid in the same transaction: the account (FOR SHARE) before the code (FOR UPDATE), the session
// only on the credential generation read there. Unknown, used, expired, an account that is not
// active or is an operator's: ErrTokenInvalid.
func (s *Store) ExchangeSignInCode(ctx context.Context, tokenHash, sid string, now time.Time) (SignInExchange, error) {
	now = now.UTC()
	var uids []int64
	if err := s.db.SelectContext(ctx, &uids, `SELECT user_id FROM email_token WHERE token_hash = ? AND purpose = 'signin'`,
		tokenHash); err != nil {
		return SignInExchange{}, fmt.Errorf("platformstore: find sign-in code: %w", err)
	}
	if len(uids) == 0 {
		return SignInExchange{}, ErrTokenInvalid
	}
	var res SignInExchange
	err := s.inTx(ctx, func(tx *sqlx.Tx) error {
		u, err := shareUserByID(ctx, tx, uids[0])
		if errors.Is(err, ErrUserNotFound) {
			return ErrTokenInvalid
		}
		if err != nil {
			return err
		}
		var t tokenRow
		if err := tx.GetContext(ctx, &t, `SELECT `+tokenColumns+` FROM email_token
WHERE token_hash = ? AND purpose = 'signin' FOR UPDATE`, tokenHash); err != nil {
			if isNoRows(err) {
				return ErrTokenInvalid
			}
			return fmt.Errorf("platformstore: lock sign-in code: %w", err)
		}
		if t.UsedAt.Valid || !t.ExpiresAt.After(now) || t.UserID != u.ID || u.Status != UserActive || u.IsOperator {
			return ErrTokenInvalid
		}
		if err := consumeTx(ctx, tx, t.ID, now); err != nil {
			return err
		}
		if err := createSessionTx(ctx, tx, sid, u.ID, 0, SessionRoot, "", u.CredGen, now); err != nil {
			if errors.Is(err, ErrCredentialsChanged) {
				return ErrTokenInvalid
			}
			return err
		}
		res = SignInExchange{User: u, EnterTeamID: t.TeamID.Int64}
		return nil
	})
	if err != nil {
		return SignInExchange{}, err
	}
	return res, nil
}

// RootSessionLive reports whether sid is a live root session of the user opened after notBefore
// (now minus the root session's absolute lifetime): a root session is never renewed.
func (s *Store) RootSessionLive(ctx context.Context, sid string, userID int64, notBefore time.Time) (bool, error) {
	if sid == "" || userID <= 0 {
		return false, nil
	}
	var n int
	if err := s.db.GetContext(ctx, &n, `SELECT COUNT(*) FROM user_session
WHERE sid = ? AND user_id = ? AND aud = 'root' AND revoked_at IS NULL AND created_at > ?`, sid, userID, notBefore.UTC()); err != nil {
		return false, fmt.Errorf("platformstore: root session: %w", err)
	}
	return n > 0, nil
}

// SignOutRoot ends a root session and everything opened from it, in one transaction: the account
// FOR UPDATE first (so a hand-off or sign-in exchange of the same account, which holds it FOR
// SHARE, either commits before and its session is found here, or starts after and finds its code
// spent), then the root session and every session whose parent_sid is it (revoked), then its unused
// hand-off and sign-in codes (expired). Returns the revoked sids for the edge cache.
func (s *Store) SignOutRoot(ctx context.Context, sid string, userID int64, now time.Time) ([]string, error) {
	now = now.UTC()
	if sid == "" || userID <= 0 {
		return nil, nil
	}
	var sids []string
	err := s.inTx(ctx, func(tx *sqlx.Tx) error {
		if _, err := lockUserByID(ctx, tx, userID); err != nil {
			if errors.Is(err, ErrUserNotFound) {
				return nil
			}
			return err
		}
		sids = nil
		if err := tx.SelectContext(ctx, &sids, `SELECT sid FROM user_session
WHERE user_id = ? AND (sid = ? OR parent_sid = ?) AND revoked_at IS NULL FOR UPDATE`, userID, sid, sid); err != nil {
			return fmt.Errorf("platformstore: root sign-out: sessions: %w", err)
		}
		if len(sids) > 0 {
			if _, err := tx.ExecContext(ctx, `UPDATE user_session SET revoked_at = ?
WHERE user_id = ? AND (sid = ? OR parent_sid = ?) AND revoked_at IS NULL`, now, userID, sid, sid); err != nil {
				return fmt.Errorf("platformstore: root sign-out: revoke: %w", err)
			}
		}
		if _, err := tx.ExecContext(ctx, `UPDATE email_token SET used_at = ?
WHERE user_id = ? AND parent_sid = ? AND purpose IN ('handoff', 'signin') AND used_at IS NULL`, now, userID, sid); err != nil {
			return fmt.Errorf("platformstore: root sign-out: codes: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return sids, nil
}
