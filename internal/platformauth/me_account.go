package platformauth

import (
	"context"
	"errors"
	"log/slog"
	"strconv"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/auth/jwt"
	"github.com/jekabolt/grbpwr-manager/internal/middleware"
	"github.com/jekabolt/grbpwr-manager/internal/platformstore"
	"github.com/jekabolt/grbpwr-manager/proto/gen/auth"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Account lifecycle RPCs of the calling person (P0 design §3.5, §4).

// MaxAccountDeletesPerHr bounds DeleteMyAccount tries per account (a wrong confirmation included).
const MaxAccountDeletesPerHr = 10

// Delete answers. Built per call: the gate counts package-level values.
func errBadConfirm() error     { return status.Error(codes.InvalidArgument, "bad_confirm") }
func errSoleOwner() error      { return status.Error(codes.FailedPrecondition, "sole_owner") }
func errOperatorDelete() error { return status.Error(codes.FailedPrecondition, "operator") }
func errAccountDeleted() error { return status.Error(codes.FailedPrecondition, "account_deleted") }

// FinishTeamEntry ends the first-entry screen of the calling team (P0 §4): finished and skipped are
// the same, and it sticks. A team token only (a root token has no team to enter); idempotent.
func (s *Service) FinishTeamEntry(ctx context.Context, token string, _ *auth.FinishTeamEntryRequest) (*auth.FinishTeamEntryResponse, error) {
	c, err := s.me(ctx, token)
	if err != nil {
		return nil, err
	}
	if c.TeamID == 0 {
		return nil, errNoAccess()
	}
	if !s.profileWrite.Allow(c.key()) {
		return nil, errBusy()
	}
	if err := s.d.Store.FinishTeamEntry(ctx, c.TeamID, c.UserID); err != nil {
		s.log(ctx, "finish team entry", err)
		return nil, errInternal()
	}
	s.audit(ctx, c.UserID, c.TeamID, "team_entry_finish", nil)
	return &auth.FinishTeamEntryResponse{}, nil
}

// DeleteMyAccount deletes the calling person's account (P0 §3.5): every session ends at once, the
// memberships freeze with their roles, the teams where the person is the only member close with
// it, and the account_deleted letter names the last day of RestoreAccount's 30 days. Refused:
// bad_confirm (the typed @username), sole_owner (the only owner of a team with members), operator,
// try_again (a closing team is still provisioning, or a lock race).
func (s *Service) DeleteMyAccount(ctx context.Context, token string, req *auth.DeleteMyAccountRequest) (*auth.DeleteMyAccountResponse, error) {
	c, err := s.me(ctx, token)
	if err != nil {
		return nil, err
	}
	if !s.accountDelete.Allow(c.key()) {
		return nil, errBusy()
	}
	now := s.now()
	del, err := s.d.Store.DeleteAccount(ctx, platformstore.DeleteAccountParams{
		UserID:      c.UserID,
		Confirm:     req.GetConfirmUsername(),
		Now:         now,
		Retention:   platformstore.DeleteRetention,
		RestoreLink: "https://" + s.base + "/restore",
	})
	switch {
	case errors.Is(err, platformstore.ErrBadConfirm):
		return nil, errBadConfirm()
	case errors.Is(err, platformstore.ErrSoleOwner):
		return nil, errSoleOwner()
	case errors.Is(err, platformstore.ErrOperatorAccount):
		return nil, errOperatorDelete()
	case errors.Is(err, platformstore.ErrTryAgain):
		return nil, errTryAgain()
	case errors.Is(err, platformstore.ErrUserNotFound):
		return nil, errAuth()
	case err != nil:
		s.log(ctx, "delete account", err)
		return nil, errInternal()
	}
	// Committed: the edge drops the revoked sessions and the handle's legacy «not before» (the
	// credential generation moved), then each open team disables the person's admins row.
	if s.d.Hooks.PasswordChanged != nil {
		s.d.Hooks.PasswordChanged(ctx, del.Handle, del.SIDs)
	}
	s.freezeRows(ctx, del.Handle, del.Frozen)
	s.audit(ctx, c.UserID, c.TeamID, "account_delete", map[string]string{
		"closed": strconv.Itoa(len(del.Closed)), "frozen": strconv.Itoa(len(del.Frozen))})
	return &auth.DeleteMyAccountResponse{PurgeAt: timestamppb.New(del.PurgeAt)}, nil
}

// freezeRows disables the person's admins row in each team whose membership froze (best effort,
// after the commit: the frozen membership and the revoked sessions already shut every platform
// path; the row only backs the legacy team #1 Login and the edge's cached rights).
func (s *Service) freezeRows(ctx context.Context, handle string, teams []int64) {
	if s.d.Hooks.FreezeMember == nil {
		return
	}
	for _, id := range teams {
		team, err := s.d.Store.Directory().ByID(ctx, id)
		if err != nil || team.DBName == "" {
			continue
		}
		if _, err := s.d.Hooks.FreezeMember(ctx, team, handle); err != nil {
			slog.Default().ErrorContext(ctx, "platformauth: freeze team admins row",
				slog.Int64("team", id), slog.String("err", err.Error()))
		}
	}
}

// enableRows finishes the admins rows a delete froze, in each team whose membership came back
// (best effort: a row still frozen is finished on the member's next sign-in to the team).
func (s *Service) enableRows(ctx context.Context, handle string, teams []int64) {
	if s.d.Hooks.EnableMember == nil {
		return
	}
	for _, id := range teams {
		team, err := s.d.Store.Directory().ByID(ctx, id)
		if err != nil || team.DBName == "" {
			continue
		}
		if _, err := s.d.Hooks.EnableMember(ctx, team, handle); err != nil {
			slog.Default().ErrorContext(ctx, "platformauth: enable team admins row",
				slog.Int64("team", id), slog.String("err", err.Error()))
		}
	}
}

// loginUser finds the account a typed login names: an email, else a handle, else an @username
// (one leading @ ignored), as SignIn resolves it. ok=false when none.
func (s *Service) loginUser(ctx context.Context, raw string) (platformstore.User, bool, error) {
	login := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(raw)), "@")
	if login == "" || len(login) > 320 {
		return platformstore.User{}, false, nil
	}
	var (
		u   platformstore.User
		err error
	)
	if strings.Contains(login, "@") {
		email, nerr := NormalizeEmail(login)
		if nerr != nil {
			return platformstore.User{}, false, nil
		}
		u, err = s.d.Store.UserByEmail(ctx, email)
	} else {
		u, err = s.d.Store.UserByHandle(ctx, login)
		if errors.Is(err, platformstore.ErrUserNotFound) {
			u, err = s.d.Store.UserByPublicUsername(ctx, login)
		}
	}
	if errors.Is(err, platformstore.ErrUserNotFound) {
		return platformstore.User{}, false, nil
	}
	if err != nil {
		return platformstore.User{}, false, err
	}
	return u, true, nil
}

// RequestAccountRestore emails a restore letter (code + link, 1 hour) to the stored address of the
// account login names, when it is deleted and its 30 days still run (public). Always {}: the
// answer never tells whether the login names an account, or which kind.
func (s *Service) RequestAccountRestore(ctx context.Context, req *auth.RequestAccountRestoreRequest) (*auth.RequestAccountRestoreResponse, error) {
	if err := s.usable(); err != nil {
		return nil, err
	}
	if !s.restoreIP.Allow(middleware.GetClientIP(ctx)) {
		return &auth.RequestAccountRestoreResponse{}, nil
	}
	u, ok, err := s.loginUser(ctx, req.GetLogin())
	if err != nil {
		s.log(ctx, "restore lookup", err)
		return &auth.RequestAccountRestoreResponse{}, nil
	}
	if !ok || !u.Email.Valid {
		return &auth.RequestAccountRestoreResponse{}, nil
	}
	now := s.now()
	sent, err := s.d.Store.RequestAccountRestore(ctx, u.ID, now, MaxLettersPerEmailHr,
		s.letter(PurposeRestore, u.Email.String, RestoreTTL, now))
	if err != nil {
		s.log(ctx, "request restore", err)
	} else if sent {
		s.audit(ctx, u.ID, 0, "restore_requested", nil)
	}
	return &auth.RequestAccountRestoreResponse{}, nil
}

// RestoreAccount brings a deleted account back while its 30 days run, by the restore letter's link
// token or by (login, code) (public): the account active again, back in every team with the role it
// had, the teams that closed with it reopened; the person is signed in on the root site. A wrong
// code counts against the letter and the account as every code does. Refused: invalid or expired
// code (also a login that names nothing, an account that is not deleted, a window that is over).
func (s *Service) RestoreAccount(ctx context.Context, req *auth.RestoreAccountRequest) (*auth.RestoreAccountResponse, error) {
	if err := s.usable(); err != nil {
		return nil, err
	}
	now := s.now()
	sid, err := NewSessionID()
	if err != nil {
		s.log(ctx, "session id", err)
		return nil, errInternal()
	}
	ctx = withSessionClient(ctx)
	var res platformstore.AccountRestore
	if tok := strings.TrimSpace(req.GetToken()); tok != "" {
		if !s.restoreIP.Allow(middleware.GetClientIP(ctx)) {
			return nil, errBusy()
		}
		res, err = s.d.Store.RestoreByLink(ctx, HashToken(tok), now, sid)
	} else {
		u, found, lerr := s.loginUser(ctx, req.GetLogin())
		if lerr != nil {
			s.log(ctx, "restore lookup", lerr)
			return nil, errInternal()
		}
		email := ""
		if found && u.Email.Valid {
			email = u.Email.String
		}
		// The IP bucket and the code's shape first; an unknown login costs the same HMAC and answers
		// the same.
		_, c, ierr := s.codeInput(ctx, email, req.GetCode())
		if ierr != nil {
			return nil, ierr
		}
		res, err = s.d.Store.RestoreByCode(ctx, s.attempt(PurposeRestore, email, c, now), u.ID, sid)
	}
	switch {
	case errors.Is(err, platformstore.ErrTokenInvalid):
		return nil, errCode()
	case errors.Is(err, platformstore.ErrTryAgain):
		return nil, errTryAgain()
	case err != nil:
		s.log(ctx, "restore account", err)
		return nil, errInternal()
	}
	s.enableRows(ctx, res.User.Handle, res.Unfrozen)
	// The restore has committed: a mint failure answers without the token (the person signs in with
	// the password), never as an error over a spent letter.
	tok, err := s.mint(ctx, res.User, sid, 0, jwt.AudRoot, RootSessionTTL, false, nil, now)
	if err != nil {
		s.log(ctx, "restore root token", err)
		tok = ""
	}
	teams, err := s.d.Store.MemberTeams(ctx, res.User.ID)
	if err != nil {
		s.log(ctx, "member teams", err)
	}
	s.audit(ctx, res.User.ID, 0, "account_restore", map[string]string{
		"teams": strconv.Itoa(len(res.Unfrozen)), "reopened": strconv.Itoa(len(res.Reopened))})
	return &auth.RestoreAccountResponse{RootToken: tok, Teams: s.teamRefs(teams)}, nil
}
