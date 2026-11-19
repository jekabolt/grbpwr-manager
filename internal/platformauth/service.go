package platformauth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/go-chi/jwtauth/v5"
	"github.com/jekabolt/grbpwr-manager/internal/auth/jwt"
	"github.com/jekabolt/grbpwr-manager/internal/auth/pwhash"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/middleware"
	"github.com/jekabolt/grbpwr-manager/internal/platformstore"
	"github.com/jekabolt/grbpwr-manager/internal/ratelimit"
	"github.com/jekabolt/grbpwr-manager/internal/rbac"
	"github.com/jekabolt/grbpwr-manager/internal/tenant"
	"github.com/jekabolt/grbpwr-manager/proto/gen/auth"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Hooks reach team databases; the auth server builds them from its admin repository.
type Hooks struct {
	// TeamAccount reads the member's admins row in the team's database. found=false when there is none.
	TeamAccount func(ctx context.Context, team tenant.Team, handle string) (acc TeamAccount, found bool, err error)
	// StageMember creates or updates the member's admins row in the team's database DISABLED, with
	// this super flag and these permissions (an invite's step 2: unusable until the control plane
	// makes the member active).
	StageMember func(ctx context.Context, team tenant.Team, handle string, super bool, perms []entity.AdminPermission) error
	// EnableMember finishes a staged admins row with the team-side compare-and-set (an invite's
	// last step, or the finish on sign-in). false = nothing was enabled: the row is not staged any
	// more (finished, disabled or re-decided since).
	EnableMember func(ctx context.Context, team tenant.Team, handle string) (bool, error)
	// TeamOnePassword writes a new password hash onto team #1's admins row of the handle.
	TeamOnePassword func(ctx context.Context, handle, hash string) error
	// PasswordChanged runs after a password reset committed: sids are the sessions it revoked.
	// The edge drops them and the handle's cached «not before» so old platform sessions, team
	// tokens and legacy team #1 tokens fail on their next call.
	PasswordChanged func(ctx context.Context, handle string, sids []string)
	// SessionsRevoked runs after a transaction revoked these sessions without a password change (a
	// root sign-out with its children, a provider claim of a pending account): the edge drops them
	// from its cache. nil = the edge's TTL bounds a stale entry.
	SessionsRevoked func(ctx context.Context, sids []string)
}

// TeamAccount is what a team token needs from the team's admins row.
type TeamAccount struct {
	Super    bool
	Perms    []entity.AdminPermission
	Disabled bool
	// JoinPending: the row was staged by an invite that has not been finished (team-side marker).
	JoinPending bool
}

// Deps is what the service is built from.
type Deps struct {
	Store   *platformstore.Store
	Config  platformstore.Config
	JWT     *jwtauth.JWTAuth
	Hasher  *pwhash.PasswordHasher
	TeamTTL time.Duration // lifetime of team and console tokens (the admin token TTL)
	Now     func() time.Time
	Hooks   Hooks
	// SignupApproval (PLATFORM_SIGNUP_APPROVAL): a confirmed sign-up waits in review for an
	// operator instead of going straight to provisioning.
	SignupApproval bool
	// TermsVersion (PLATFORM_TERMS_VERSION) is stored on every account created by sign-up or invite.
	TermsVersion string
	// SchemaBehind is the edge's boot-time predicate «team t's database is behind the binary»
	// (tenantbind.Binder.SchemaBehind). A sign-in, hand-off or invite into such a team is refused
	// before any code is spent, session opened or team database touched. nil = every team current.
	SchemaBehind func(tenant.Team) bool
	// SignInOptions says which provider buttons GetSignInOptions offers. nil = OptionsFromConfig over
	// Config.OAuth (lane O3 passes its provider registry).
	SignInOptions SignInOptions
}

// Service is the identity logic behind the AuthService platform RPCs.
type Service struct {
	d         Deps
	base      string // PLATFORM_BASE_DOMAIN, lowercase, no scheme
	pepper    string
	ready     error // non-nil: the platform config is unusable, every RPC answers UNAVAILABLE
	dummyHash string

	options SignInOptions

	codeIP, signUpIP, signInIP, signInLogin, slugIP, resetIP *ratelimit.Limiter
	// createTeam: `new team` per account (MaxCreateTeamsPerHr); logoSet: logo changes per account.
	createTeam, logoSet *ratelimit.Limiter
	// logoSlots bounds the logos decoded at once (memory: a 4096-px image is tens of MB decoded).
	logoSlots chan struct{}
}

// Error answers. Built per call: the gate counts package-level values.
func errCode() error     { return status.Error(codes.InvalidArgument, "invalid or expired code") }
func errAuth() error     { return status.Error(codes.Unauthenticated, "not authenticated") }
func errNotReady() error { return status.Error(codes.Unavailable, "workspace is being prepared") }
func errUpdating() error { return status.Error(codes.Unavailable, "team is being updated") }
func errInternal() error { return status.Error(codes.Internal, "internal error") }
func errBusy() error {
	return status.Error(codes.ResourceExhausted, "too many attempts, please try again later")
}
func errBad(msg string) error { return status.Error(codes.InvalidArgument, msg) }
func errNoAccess() error      { return status.Error(codes.PermissionDenied, "no access") }
func errTerms() error         { return status.Error(codes.InvalidArgument, "terms_required") }
func errAwaiting() error {
	return status.Error(codes.FailedPrecondition, "team_awaiting_approval")
}

// New builds the service. A missing pepper or base domain does not fail: the service answers
// UNAVAILABLE and Ready reports why (boot logs it).
func New(d Deps) (*Service, error) {
	if d.Store == nil || d.JWT == nil || d.Hasher == nil {
		return nil, errors.New("platformauth: store, jwt and hasher are required")
	}
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.TeamTTL <= 0 {
		d.TeamTTL = time.Hour
	}
	if d.TermsVersion = strings.TrimSpace(d.TermsVersion); d.TermsVersion == "" {
		d.TermsVersion = DefaultTermsVersion
	}
	s := &Service{
		d:           d,
		base:        strings.ToLower(strings.Trim(strings.TrimSpace(d.Config.BaseDomain), "./")),
		pepper:      d.Config.TokenPepper,
		codeIP:      ratelimit.NewLimiter(time.Hour, MaxCodeTriesPerIPHr),
		signUpIP:    ratelimit.NewLimiter(time.Hour, MaxSignUpsPerIPHr),
		signInIP:    ratelimit.NewLimiter(time.Minute, 10),
		signInLogin: ratelimit.NewLimiter(time.Minute, 5),
		slugIP:      ratelimit.NewLimiter(time.Minute, 60),
		resetIP:     ratelimit.NewLimiter(time.Minute, 10),
		createTeam:  ratelimit.NewLimiter(time.Hour, MaxCreateTeamsPerHr),
		logoSet:     ratelimit.NewLimiter(time.Hour, MaxLogoSetsPerHr),
		logoSlots:   make(chan struct{}, MaxLogoDecodes),
		options:     d.SignInOptions,
	}
	if s.options == nil {
		s.options = OptionsFromConfig(d.Config.OAuth)
	}
	if err := CheckPepper(s.pepper); err != nil {
		s.ready = err
	} else if s.base == "" || strings.Contains(s.base, "/") {
		s.ready = errors.New("platformauth: PLATFORM_BASE_DOMAIN is empty or not a bare host")
	}
	h, err := d.Hasher.HashPassword("platformauth-timing-dummy")
	if err != nil {
		s.Close()
		return nil, fmt.Errorf("platformauth: dummy hash: %w", err)
	}
	s.dummyHash = h
	return s, nil
}

// Ready is nil when the platform config allows the RPCs to run.
func (s *Service) Ready() error { return s.ready }

// Close stops the limiters' cleanup goroutines.
func (s *Service) Close() {
	for _, l := range []*ratelimit.Limiter{s.codeIP, s.signUpIP, s.signInIP, s.signInLogin, s.slugIP, s.resetIP, s.createTeam, s.logoSet} {
		l.Stop()
	}
}

func (s *Service) usable() error {
	if s.ready != nil {
		return status.Error(codes.Unavailable, "sign-up is not available")
	}
	return nil
}

// now is the clock in UTC, truncated to the millisecond DATETIME(3) keeps (MySQL rounds, never into
// a comparison's favour).
func (s *Service) now() time.Time { return s.d.Now().UTC().Truncate(time.Millisecond) }

func (s *Service) log(ctx context.Context, msg string, err error) {
	slog.Default().ErrorContext(ctx, "platformauth: "+msg, slog.String("err", err.Error()))
}

func (s *Service) audit(ctx context.Context, userID, teamID int64, action string, detail map[string]string) {
	if err := s.d.Store.AuditAuth(ctx, userID, teamID, action, detail, middleware.GetClientIP(ctx)); err != nil {
		s.log(ctx, "audit "+action, err)
	}
}

func (s *Service) rootURL(path, key, secret string) string {
	return "https://" + s.base + path + "#" + key + "=" + secret
}

func (s *Service) teamURL(slug, path, key, secret string) string {
	return "https://" + slug + "." + s.base + path + "#" + key + "=" + secret
}

// letter builds a verify or reset letter: one token row with a link AND a code.
func (s *Service) letter(purpose, email string, ttl time.Duration, now time.Time) platformstore.MakeLetter {
	return func(userID int64, team *platformstore.TeamBrief) (platformstore.NewToken, platformstore.Letter, error) {
		tok, tokHash, err := NewToken()
		if err != nil {
			return platformstore.NewToken{}, platformstore.Letter{}, err
		}
		code, err := NewCode()
		if err != nil {
			return platformstore.NewToken{}, platformstore.Letter{}, err
		}
		nt := platformstore.NewToken{Purpose: purpose, TokenHash: tokHash, CodeHash: CodeHash(s.pepper, purpose, userID, code),
			ExpiresAt: now.Add(ttl)}
		l := platformstore.Letter{To: email, Template: purpose, Code: code}
		switch purpose {
		case PurposeVerify:
			l.Link = s.rootURL("/confirm", "token", tok)
		case PurposeReset:
			l.Link = s.rootURL("/reset", "token", tok)
		}
		if team != nil {
			nt.TeamID = team.ID
			l.TeamName, l.Slug = team.Name, team.Slug
		}
		return nt, l, nil
	}
}

// attempt is the code path's check for (email, code) against letters of purpose.
func (s *Service) attempt(purpose, email, code string, now time.Time) platformstore.CodeAttempt {
	return platformstore.CodeAttempt{
		Email:          email,
		Now:            now,
		MaxRowAttempts: MaxRowCodeAttempts,
		Match: func(userID int64, stored string) bool {
			if userID == 0 || stored == "" {
				_ = CodeHash(s.pepper, purpose, 0, code)
				return false
			}
			return CodeMatches(s.pepper, purpose, userID, code, stored)
		},
		Fail: func(c platformstore.CodeCounters) platformstore.CodeCounters {
			n := CodeState{Failures: c.Failures, Since: c.Since, LockedUntil: c.LockedUntil}.AfterFailure(now)
			return platformstore.CodeCounters{Failures: n.Failures, Since: n.Since, LockedUntil: n.LockedUntil}
		},
		Locked: func(c platformstore.CodeCounters) bool {
			return CodeState{Failures: c.Failures, Since: c.Since, LockedUntil: c.LockedUntil}.Locked(now)
		},
	}
}

// codeInput reads the code path's (email, code). A malformed pair still runs the full check against
// an impossible code, so it costs and counts like a wrong one.
func (s *Service) codeInput(ctx context.Context, rawEmail, rawCode string) (email, code string, err error) {
	if !s.codeIP.Allow(middleware.GetClientIP(ctx)) {
		_ = CodeHash(s.pepper, "", 0, rawCode)
		return "", "", errCode()
	}
	email, err = NormalizeEmail(rawEmail)
	if err != nil {
		_ = CodeHash(s.pepper, "", 0, rawCode)
		return "", "", errCode()
	}
	code, ok := NormalizeCode(rawCode)
	if !ok {
		code = "x" // never matches a six-digit code's hash
	}
	return email, code, nil
}

// handoffToken is a fresh hand-off for a team, valid ttl.
func handoffToken(ttl time.Duration, now time.Time) (string, platformstore.NewToken, error) {
	tok, hash, err := NewToken()
	if err != nil {
		return "", platformstore.NewToken{}, err
	}
	return tok, platformstore.NewToken{Purpose: PurposeHandoff, TokenHash: hash, ExpiresAt: now.Add(ttl)}, nil
}

// --- sign-up ---

// CheckTeamSlug reports whether an address is free, and suggests one from the name (or the address).
func (s *Service) CheckTeamSlug(ctx context.Context, req *auth.CheckTeamSlugRequest) (*auth.CheckTeamSlugResponse, error) {
	if !s.slugIP.Allow(middleware.GetClientIP(ctx)) {
		return nil, errBusy()
	}
	now := s.now()
	taken := func(slug string) (bool, error) { return s.d.Store.SlugTaken(ctx, slug, now) }
	resp := &auth.CheckTeamSlugResponse{}
	slug := strings.ToLower(strings.TrimSpace(req.GetSlug()))
	if slug != "" && tenant.ValidSlug(slug) == nil {
		t, err := taken(slug)
		if err != nil {
			s.log(ctx, "check slug", err)
			return nil, errInternal()
		}
		resp.Available = !t
	}
	source := strings.TrimSpace(req.GetName())
	if source == "" {
		source = slug
	}
	if source != "" {
		sug, err := SuggestSlug(source, taken)
		if err != nil {
			s.log(ctx, "suggest slug", err)
			return nil, errInternal()
		}
		resp.Suggestion = sug
	}
	return resp, nil
}

// SignUp registers the person and a pending team and queues one letter with a code and a link. It
// answers {} whether or not the email was known; only a bad field or a taken address is an error.
// The team's brand, when the sheet carries one, is written with the team (NormalizeTeamBrand: a bad
// value refuses the whole sheet with its key before anything is written).
func (s *Service) SignUp(ctx context.Context, req *auth.SignUpRequest) (*auth.SignUpResponse, error) {
	if err := s.usable(); err != nil {
		return nil, err
	}
	email, err := NormalizeEmail(req.GetEmail())
	if err != nil {
		return nil, errBad(err.Error())
	}
	if err := CheckPassword(req.GetPassword()); err != nil {
		return nil, errBad(err.Error())
	}
	name, err := CleanTeamName(req.GetTeamName())
	if err != nil {
		return nil, errBad(err.Error())
	}
	slug := strings.ToLower(strings.TrimSpace(req.GetTeamSlug()))
	if err := tenant.ValidSlug(slug); err != nil {
		return nil, errBad(err.Error())
	}
	if !req.GetTermsAccepted() {
		return nil, errTerms()
	}
	brand, err := creationBrand(req.GetBrand())
	if err != nil {
		return nil, err
	}
	if !s.signUpIP.Allow(middleware.GetClientIP(ctx)) {
		return &auth.SignUpResponse{}, nil
	}
	logo, err := s.processLogo(ctx, req.GetLogo())
	if err != nil {
		return nil, err
	}
	hash, err := s.d.Hasher.HashPassword(req.GetPassword())
	if err != nil {
		s.log(ctx, "hash password", err)
		return nil, errInternal()
	}
	now := s.now()
	res, err := s.d.Store.SignUp(ctx, platformstore.SignUpParams{
		Email: email, PasswordHash: hash, TeamName: name, Slug: slug, HandleBase: HandleBase(email),
		Now: now, PendingTTL: PendingTeamTTL, MaxLettersPerHour: MaxLettersPerEmailHr,
		PasswordMatches: s.passwordMatcher(req.GetPassword()),
		Letter:          s.letter(PurposeVerify, email, VerifyTTL, now),
		TermsVersion:    s.d.TermsVersion,
		Logo:            logo,
		Brand:           brand,
	})
	if errors.Is(err, platformstore.ErrSlugTaken) {
		return nil, errSlugTaken()
	}
	if err != nil {
		s.log(ctx, "sign-up", err)
		return nil, errInternal()
	}
	if res.Created {
		s.audit(ctx, res.UserID, res.TeamID, "signup", map[string]string{"slug": slug})
	} else {
		s.audit(ctx, 0, 0, "signup_ignored", map[string]string{"reason": res.Reason})
	}
	return &auth.SignUpResponse{}, nil
}

// passwordMatcher checks password against a stored hash (see passwordOK).
func (s *Service) passwordMatcher(password string) func(stored string) bool {
	return func(stored string) bool { return s.passwordOK(password, stored) }
}

// passwordOK checks password against a stored hash. A missing ("") or malformed hash (a row
// seeded before UnusableHash became a real PBKDF2 hash) runs the same check against the dummy hash
// and answers false, so neither a miss nor the account kind shows in the timing.
func (s *Service) passwordOK(password, stored string) bool {
	if !wellFormedHash(stored) {
		_ = s.d.Hasher.Validate(password, s.dummyHash)
		return false
	}
	return s.d.Hasher.Validate(password, stored) == nil
}

// ResendVerification queues a fresh letter for a pending sign-up. Always {}.
func (s *Service) ResendVerification(ctx context.Context, req *auth.ResendVerificationRequest) (*auth.ResendVerificationResponse, error) {
	if err := s.usable(); err != nil {
		return nil, err
	}
	email, err := NormalizeEmail(req.GetEmail())
	if err != nil {
		return &auth.ResendVerificationResponse{}, nil
	}
	now := s.now()
	if _, err := s.d.Store.ResendVerification(ctx, email, now, MaxLettersPerEmailHr, s.letter(PurposeVerify, email, VerifyTTL, now)); err != nil {
		s.log(ctx, "resend verification", err)
	}
	return &auth.ResendVerificationResponse{}, nil
}

// VerifyEmail confirms a sign-up by its link token or by (email, code): the account becomes active,
// the team goes to provisioning, and a 10-minute hand-off into it comes back. With sign-up approval
// on the team goes to review instead: the operators get a letter each, the answer carries the slug
// and awaiting_approval, and no hand-off is made. A second click on the link then answers the same
// and writes nothing.
//
// A first-time confirmation also signs the person in: the root session the password sign-in opens
// (same store row, RootSessionTTL, the "signin" audit) comes back as root_token, under review too.
// It is opened in the confirming transaction, so only a letter consumed there yields one: a used,
// expired or wrong code or link, a disabled account and the repeat of a used link get today's
// answer and no token. An operator gets none either (password only, as on the provider path).
func (s *Service) VerifyEmail(ctx context.Context, req *auth.VerifyEmailRequest) (*auth.VerifyEmailResponse, error) {
	if err := s.usable(); err != nil {
		return nil, err
	}
	now := s.now()
	code, nt, err := handoffToken(HandoffLongTTL, now)
	if err != nil {
		s.log(ctx, "hand-off token", err)
		return nil, errInternal()
	}
	sid, err := NewSessionID()
	if err != nil {
		s.log(ctx, "session id", err)
		return nil, errInternal()
	}
	var res platformstore.VerifyResult
	if tok := strings.TrimSpace(req.GetToken()); tok != "" {
		res, err = s.d.Store.VerifyByLink(ctx, HashToken(tok), now, nt, sid, s.review())
	} else {
		email, c, ierr := s.codeInput(ctx, req.GetEmail(), req.GetCode())
		if ierr != nil {
			return nil, ierr
		}
		res, err = s.d.Store.VerifyByCode(ctx, s.attempt(PurposeVerify, email, c, now), nt, sid, s.review())
	}
	if errors.Is(err, platformstore.ErrTokenInvalid) {
		return nil, errCode()
	}
	if err != nil {
		s.log(ctx, "verify email", err)
		return nil, errInternal()
	}
	var resp *auth.VerifyEmailResponse
	if res.AwaitingApproval {
		s.audit(ctx, res.UserID, res.TeamID, "verify", map[string]string{"team": "review"})
		resp = &auth.VerifyEmailResponse{TeamSlug: res.TeamSlug, AwaitingApproval: true}
	} else {
		s.audit(ctx, res.UserID, res.TeamID, "verify", nil)
		resp = &auth.VerifyEmailResponse{TeamSlug: res.TeamSlug, HandoffCode: code}
	}
	resp.RootToken = s.verifiedRootToken(ctx, res, sid, now)
	return resp, nil
}

// verifiedRootToken mints the token of the root session a confirmation opened ("" when it opened
// none). The confirmation has committed: a mint failure is logged and the answer goes without the
// token (the person signs in with the password), never as an error over a spent letter.
func (s *Service) verifiedRootToken(ctx context.Context, res platformstore.VerifyResult, sid string, now time.Time) string {
	if !res.RootSession {
		return ""
	}
	tok, err := s.mint(ctx, platformstore.User{ID: res.UserID, Handle: res.Handle}, sid, 0, jwt.AudRoot, RootSessionTTL, false, nil, now)
	if err != nil {
		s.log(ctx, "verify root token", err)
		return ""
	}
	s.audit(ctx, res.UserID, 0, "signin", map[string]string{"target": "root", "by": "verify"})
	return tok
}

// review is the store's approval switch: on, with the console address for the operators' letters.
func (s *Service) review() platformstore.Review {
	return platformstore.Review{On: s.d.SignupApproval, ConsoleLink: "https://console." + s.base + "/"}
}

// --- sign-in ---

// SignIn checks login + password and issues the token of the target. The root answer lists the
// person's teams: only here, after the password (no RPC lists teams for an email before it).
func (s *Service) SignIn(ctx context.Context, req *auth.SignInRequest) (*auth.SignInResponse, error) {
	if err := s.usable(); err != nil {
		return nil, err
	}
	target := req.GetTarget()
	if target == auth.SignInTarget_SIGN_IN_TARGET_UNKNOWN {
		return nil, errBad("target is required")
	}
	login := strings.ToLower(strings.TrimSpace(req.GetLogin()))
	if !s.signInIP.Allow(middleware.GetClientIP(ctx)) || (login != "" && !s.signInLogin.Allow(login)) {
		return nil, errBusy()
	}
	if len(req.GetPassword()) > MaxPasswordLen || login == "" {
		return nil, errAuth()
	}
	var (
		u   platformstore.User
		err error
	)
	if strings.Contains(login, "@") {
		login, err = NormalizeEmail(login)
		if err != nil {
			_ = s.passwordOK(req.GetPassword(), "")
			return nil, errAuth()
		}
		u, err = s.d.Store.UserByEmail(ctx, login)
	} else {
		u, err = s.d.Store.UserByHandle(ctx, login)
	}
	if errors.Is(err, platformstore.ErrUserNotFound) {
		_ = s.passwordOK(req.GetPassword(), "")
		return nil, errAuth()
	}
	if err != nil {
		s.log(ctx, "sign-in lookup", err)
		return nil, errInternal()
	}
	if !s.passwordOK(req.GetPassword(), u.PasswordHash) || u.Status != platformstore.UserActive {
		return nil, errAuth()
	}
	now := s.now()
	switch target {
	case auth.SignInTarget_SIGN_IN_TARGET_ROOT:
		return s.signInRoot(ctx, u, now)
	case auth.SignInTarget_SIGN_IN_TARGET_TEAM:
		slug := tenant.HeaderSlug(ctx)
		if slug == "" {
			return nil, errAuth()
		}
		team, err := s.d.Store.Directory().BySlug(ctx, slug)
		if err != nil {
			return nil, errAuth()
		}
		tok, err := s.teamToken(ctx, u, team, now)
		if err != nil {
			return nil, err
		}
		s.audit(ctx, u.ID, team.ID, "signin", map[string]string{"target": "team"})
		return &auth.SignInResponse{Token: tok}, nil
	case auth.SignInTarget_SIGN_IN_TARGET_CONSOLE:
		if !u.IsOperator || !s.isListedOperator(u) {
			return nil, errAuth()
		}
		tok, err := s.session(ctx, u, 0, jwt.AudConsole, s.d.TeamTTL, false, nil, now)
		if err != nil {
			return nil, err
		}
		s.audit(ctx, u.ID, 0, "signin", map[string]string{"target": "console"})
		return &auth.SignInResponse{Token: tok}, nil
	default:
		return nil, errBad("target is required")
	}
}

func (s *Service) isListedOperator(u platformstore.User) bool {
	if !u.Email.Valid {
		return false
	}
	ops, _ := ParseOperatorEmails(s.d.Config.OperatorEmails)
	for _, e := range ops {
		if e == u.Email.String {
			return true
		}
	}
	return false
}

func (s *Service) signInRoot(ctx context.Context, u platformstore.User, now time.Time) (*auth.SignInResponse, error) {
	tok, err := s.session(ctx, u, 0, jwt.AudRoot, RootSessionTTL, false, nil, now)
	if err != nil {
		return nil, err
	}
	teams, err := s.d.Store.MemberTeams(ctx, u.ID)
	if err != nil {
		s.log(ctx, "member teams", err)
		return nil, errInternal()
	}
	resp := &auth.SignInResponse{Token: tok, Teams: s.teamRefs(teams)}
	s.audit(ctx, u.ID, 0, "signin", map[string]string{"target": "root"})
	return resp, nil
}

// teamToken issues a team token for a member: membership active, the team past provisioning, the
// member's admins row present and enabled. Every refusal is the generic one; provisioning is
// UNAVAILABLE so the page can wait.
func (s *Service) teamToken(ctx context.Context, u platformstore.User, team tenant.Team, now time.Time) (string, error) {
	m, ok, err := s.d.Store.Membership(ctx, u.ID, team.ID)
	if err != nil {
		s.log(ctx, "membership", err)
		return "", errInternal()
	}
	if !ok {
		return "", errAuth()
	}
	if m.Status == string(tenant.StatusProvisioning) {
		s.d.Store.Directory().Invalidate(team.ID)
		return "", errNotReady()
	}
	if team.Status == tenant.StatusProvisioning || team.DBName == "" {
		// The directory's copy predates activation: read it again.
		s.d.Store.Directory().Invalidate(team.ID)
		if team, err = s.d.Store.Directory().ByID(ctx, team.ID); err != nil || team.DBName == "" {
			return "", errNotReady()
		}
	}
	if s.schemaBehind(team) {
		return "", errUpdating()
	}
	acc, found, err := s.teamAccount(ctx, team, u)
	if err != nil {
		s.log(ctx, "team account", err)
		return "", errInternal()
	}
	if !found || acc.Disabled {
		return "", errAuth()
	}
	return s.session(ctx, u, team.ID, jwt.AudTeam, s.d.TeamTTL, acc.Super, rbac.EncodePermissions(acc.Perms), now)
}

// schemaBehind: the team has a workspace (active or suspended) whose database is behind the binary.
// A team without one yet is the provisioning answer's business, not this one's.
func (s *Service) schemaBehind(t tenant.Team) bool {
	if s.d.SchemaBehind == nil || t.DBName == "" {
		return false
	}
	if t.Status != tenant.StatusActive && t.Status != tenant.StatusSuspended {
		return false
	}
	return s.d.SchemaBehind(t)
}

// teamAccount reads the member's admins row. A row an invite staged and never finished (disabled,
// join_pending) is finished here: the membership is re-read and must still be active, then the
// team-side compare-and-set enables the row. Nothing finished (0 rows: the row was disabled or
// re-decided meanwhile) is a refusal; a disabled row without the marker stays disabled.
func (s *Service) teamAccount(ctx context.Context, team tenant.Team, u platformstore.User) (TeamAccount, bool, error) {
	acc, found, err := s.d.Hooks.TeamAccount(ctx, team, u.Handle)
	if err != nil || !found || !acc.Disabled || !acc.JoinPending || s.d.Hooks.EnableMember == nil {
		return acc, found, err
	}
	if _, ok, err := s.d.Store.Membership(ctx, u.ID, team.ID); err != nil || !ok {
		return acc, found, err
	}
	enabled, err := s.d.Hooks.EnableMember(ctx, team, u.Handle)
	if err != nil || !enabled {
		return acc, found, err
	}
	acc.Disabled, acc.JoinPending = false, false
	s.audit(ctx, u.ID, team.ID, "member_enabled", map[string]string{"by": "signin"})
	return acc, found, nil
}

// session opens a user_session row and mints its token.
func (s *Service) session(ctx context.Context, u platformstore.User, teamID int64, aud string, ttl time.Duration,
	super bool, perms []string, now time.Time) (string, error) {
	sid, err := NewSessionID()
	if err != nil {
		s.log(ctx, "session id", err)
		return "", errInternal()
	}
	// A team session exists only while the membership is active (checked under its row lock), and
	// only on the credential generation read with the password the caller checked: neither a sign-in
	// that started before a removal nor one that used a password changed since leaves a session.
	if aud == jwt.AudTeam {
		ok, err := s.d.Store.CreateTeamSession(ctx, sid, u.ID, teamID, u.CredGen, now)
		switch {
		case errors.Is(err, platformstore.ErrCredentialsChanged):
			return "", errAuth()
		case err != nil:
			s.log(ctx, "create team session", err)
			return "", errInternal()
		case !ok:
			return "", errAuth()
		}
	} else if err := s.d.Store.CreateSession(ctx, sid, u.ID, teamID, aud, u.CredGen, now); err != nil {
		if errors.Is(err, platformstore.ErrCredentialsChanged) {
			return "", errAuth()
		}
		s.log(ctx, "create session", err)
		return "", errInternal()
	}
	return s.mint(ctx, u, sid, teamID, aud, ttl, super, perms, now)
}

func (s *Service) mint(ctx context.Context, u platformstore.User, sid string, teamID int64, aud string, ttl time.Duration,
	super bool, perms []string, now time.Time) (string, error) {
	tok, err := jwt.NewTeamTokenAt(s.d.JWT, ttl, jwt.TeamClaims{
		Handle: u.Handle, UserID: u.ID, TeamID: teamID, SessionID: sid, Audience: aud, Super: super, Perms: perms,
	}, now)
	if err != nil {
		s.log(ctx, "mint token", err)
		return "", errInternal()
	}
	return tok, nil
}

// IssueHandoff hands a root-host session over to one of the person's teams: a 60-second code in
// the fragment of the team host's /auth page.
func (s *Service) IssueHandoff(ctx context.Context, rootToken string, req *auth.IssueHandoffRequest) (*auth.IssueHandoffResponse, error) {
	if err := s.usable(); err != nil {
		return nil, err
	}
	u, rootSID, err := s.rootCaller(ctx, rootToken)
	if err != nil {
		return nil, err
	}
	slug := strings.ToLower(strings.TrimSpace(req.GetTeamSlug()))
	if slug == "" || len(slug) > maxSlugLen {
		return nil, errNoAccess()
	}
	team, err := s.d.Store.Directory().BySlug(ctx, slug)
	if err != nil {
		return nil, errNoAccess()
	}
	if team.Status == tenant.StatusReview {
		return nil, s.awaitingOrNoAccess(ctx, u.ID, team.ID)
	}
	m, ok, err := s.d.Store.Membership(ctx, u.ID, team.ID)
	if err != nil {
		s.log(ctx, "membership", err)
		return nil, errInternal()
	}
	if !ok {
		return nil, errNoAccess()
	}
	now := s.now()
	code, nt, err := handoffToken(HandoffTTL, now)
	if err != nil {
		s.log(ctx, "hand-off token", err)
		return nil, errInternal()
	}
	nt.TeamID = team.ID
	if err := s.d.Store.IssueHandoff(ctx, u.ID, rootSID, nt, now); err != nil {
		if errors.Is(err, platformstore.ErrTokenInvalid) {
			return nil, errAuth()
		}
		if errors.Is(err, platformstore.ErrWrongTeamStatus) {
			// The team stopped being usable (deleted) after the membership read above.
			return nil, errNoAccess()
		}
		s.log(ctx, "issue hand-off", err)
		return nil, errInternal()
	}
	return &auth.IssueHandoffResponse{Url: s.teamURL(m.Slug, "/auth", "code", code), Code: code}, nil
}

// awaitingOrNoAccess refuses a hand-off into a team in review: team_awaiting_approval to its
// members (the root list already shows them the mark), the generic no-access to anyone else.
func (s *Service) awaitingOrNoAccess(ctx context.Context, userID, teamID int64) error {
	teams, err := s.d.Store.MemberTeams(ctx, userID)
	if err != nil {
		s.log(ctx, "member teams", err)
		return errInternal()
	}
	for _, t := range teams {
		if t.TeamID == teamID && t.Status == string(tenant.StatusReview) {
			return errAwaiting()
		}
	}
	return errNoAccess()
}

// ExchangeHandoff turns a hand-off code into a team token on the team host (x-team). A team still
// being provisioned answers UNAVAILABLE and keeps the code alive.
func (s *Service) ExchangeHandoff(ctx context.Context, req *auth.ExchangeHandoffRequest) (*auth.ExchangeHandoffResponse, error) {
	if err := s.usable(); err != nil {
		return nil, err
	}
	slug := tenant.HeaderSlug(ctx)
	code := strings.TrimSpace(req.GetCode())
	if slug == "" || code == "" {
		return nil, errCode()
	}
	dir := s.d.Store.Directory()
	team, err := dir.BySlug(ctx, slug)
	if err != nil {
		return nil, errCode()
	}
	if team.Status == tenant.StatusProvisioning || team.DBName == "" {
		// The directory's copy may predate activation: read it again before the code is spent.
		dir.Invalidate(team.ID)
		if team, err = dir.ByID(ctx, team.ID); err != nil {
			return nil, errCode()
		}
	}
	if s.schemaBehind(team) {
		return nil, errUpdating()
	}
	if isOperatorCode(code) {
		return s.exchangeOperator(ctx, team, code)
	}
	sid, err := NewSessionID()
	if err != nil {
		s.log(ctx, "session id", err)
		return nil, errInternal()
	}
	now := s.now()
	res, err := s.d.Store.ExchangeHandoff(ctx, HashToken(code), team.ID, sid, now)
	switch {
	case errors.Is(err, platformstore.ErrTeamNotReady):
		dir.Invalidate(team.ID)
		return nil, errNotReady()
	case errors.Is(err, platformstore.ErrTokenInvalid):
		return nil, errCode()
	case err != nil:
		s.log(ctx, "exchange hand-off", err)
		return nil, errInternal()
	}
	if team.Status == tenant.StatusProvisioning || team.DBName == "" {
		dir.Invalidate(team.ID)
		team, err = dir.ByID(ctx, team.ID)
	}
	var acc TeamAccount
	found, behind := false, false
	if err == nil && team.DBName != "" {
		if behind = s.schemaBehind(team); !behind {
			acc, found, err = s.teamAccount(ctx, team, res.User)
		}
	}
	if err != nil || !found || acc.Disabled {
		// The code is spent; close the session it opened. The page sends the person to sign in.
		if rerr := s.d.Store.RevokeSession(ctx, sid, now); rerr != nil {
			s.log(ctx, "revoke session", rerr)
		}
		if err != nil {
			s.log(ctx, "team account", err)
			return nil, errInternal()
		}
		if behind {
			return nil, errUpdating()
		}
		return nil, errCode()
	}
	tok, err := s.mint(ctx, res.User, sid, team.ID, jwt.AudTeam, s.d.TeamTTL, acc.Super, rbac.EncodePermissions(acc.Perms), now)
	if err != nil {
		return nil, err
	}
	s.audit(ctx, res.User.ID, team.ID, "signin", map[string]string{"target": "handoff"})
	return &auth.ExchangeHandoffResponse{Token: tok}, nil
}

// SignOut revokes the session of a platform token. A root token's sign-out also ends every session
// opened from it (hand-offs into teams) and spends its unused hand-off and sign-in codes; a team
// token's ends only that team session. Always {}.
func (s *Service) SignOut(ctx context.Context, token string) (*auth.SignOutResponse, error) {
	if token == "" {
		return &auth.SignOutResponse{}, nil
	}
	claims, err := jwt.VerifyTeamToken(s.d.JWT, token, nil)
	if err != nil || claims.Legacy || claims.SessionID == "" {
		return &auth.SignOutResponse{}, nil
	}
	if claims.Audience == jwt.AudRoot && claims.UserID > 0 {
		sids, err := s.d.Store.SignOutRoot(ctx, claims.SessionID, claims.UserID, s.now())
		if err != nil {
			s.log(ctx, "sign-out", err)
		}
		s.revoked(ctx, sids)
		return &auth.SignOutResponse{}, nil
	}
	if err := s.d.Store.RevokeSession(ctx, claims.SessionID, s.now()); err != nil {
		s.log(ctx, "sign-out", err)
	}
	return &auth.SignOutResponse{}, nil
}

// --- password reset ---

// RequestPasswordReset queues a reset letter (code + link, 1 h) for an active account. Always {}.
func (s *Service) RequestPasswordReset(ctx context.Context, req *auth.RequestPasswordResetRequest) (*auth.RequestPasswordResetResponse, error) {
	if err := s.usable(); err != nil {
		return nil, err
	}
	email, err := NormalizeEmail(req.GetEmail())
	if err != nil {
		return &auth.RequestPasswordResetResponse{}, nil
	}
	now := s.now()
	var uid int64
	mk := s.letter(PurposeReset, email, ResetTTL, now)
	sent, err := s.d.Store.RequestPasswordReset(ctx, email, now, MaxLettersPerEmailHr,
		func(userID int64, team *platformstore.TeamBrief) (platformstore.NewToken, platformstore.Letter, error) {
			uid = userID
			return mk(userID, team)
		})
	if err != nil {
		s.log(ctx, "request reset", err)
	} else if sent {
		s.audit(ctx, uid, 0, "reset_requested", nil)
	}
	return &auth.RequestPasswordResetResponse{}, nil
}

// ResetPassword sets a new password by link token or (email, code) and revokes every session of
// the account; a team #1 member's admins row gets the new hash too.
func (s *Service) ResetPassword(ctx context.Context, req *auth.ResetPasswordRequest) (*auth.ResetPasswordResponse, error) {
	if err := s.usable(); err != nil {
		return nil, err
	}
	if err := CheckPassword(req.GetNewPassword()); err != nil {
		return nil, errBad(err.Error())
	}
	// PBKDF2 is the expensive step: an IP bucket (resetIP for links, codeIP for codes) and a
	// read-only check of the link or code come first, so a stranger cannot burn CPU; the consuming
	// transaction re-checks under locks.
	now := s.now()
	tok := strings.TrimSpace(req.GetToken())
	var (
		tokHash string
		a       platformstore.CodeAttempt
		live    bool
		err     error
	)
	if tok != "" {
		if !s.resetIP.Allow(middleware.GetClientIP(ctx)) {
			return nil, errBusy()
		}
		tokHash = HashToken(tok)
		live, err = s.d.Store.ResetLinkLive(ctx, tokHash, now)
	} else {
		email, c, ierr := s.codeInput(ctx, req.GetEmail(), req.GetCode())
		if ierr != nil {
			return nil, ierr
		}
		a = s.attempt(PurposeReset, email, c, now)
		live, err = s.d.Store.ResetCodeMatches(ctx, a)
	}
	if err != nil {
		s.log(ctx, "reset preflight", err)
		return nil, errInternal()
	}
	if !live {
		if tok == "" {
			// A wrong code still counts against the letter and the account.
			if _, err := s.d.Store.ResetByCode(ctx, a, ""); err != nil && !errors.Is(err, platformstore.ErrTokenInvalid) {
				s.log(ctx, "reset count", err)
			}
		}
		return nil, errCode()
	}
	hash, err := s.d.Hasher.HashPassword(req.GetNewPassword())
	if err != nil {
		s.log(ctx, "hash password", err)
		return nil, errInternal()
	}
	var res platformstore.ResetResult
	if tok != "" {
		res, err = s.d.Store.ResetByLink(ctx, tokHash, hash, now)
	} else {
		res, err = s.d.Store.ResetByCode(ctx, a, hash)
	}
	if errors.Is(err, platformstore.ErrTokenInvalid) {
		return nil, errCode()
	}
	if err != nil {
		s.log(ctx, "reset password", err)
		return nil, errInternal()
	}
	if s.d.Hooks.PasswordChanged != nil {
		s.d.Hooks.PasswordChanged(ctx, res.Handle, res.SIDs)
	}
	if res.TeamOneMem && s.d.Hooks.TeamOnePassword != nil {
		if err := s.d.Hooks.TeamOnePassword(ctx, res.Handle, hash); err != nil {
			// The platform password is set; the old team #1 Login keeps the old one until fixed.
			s.log(ctx, "team #1 password", err)
		}
	}
	s.audit(ctx, res.UserID, 0, "reset", map[string]string{"sessions_revoked": fmt.Sprint(res.Revoked)})
	return &auth.ResetPasswordResponse{}, nil
}

// --- invites ---

// GetInvite shows a live invite to the holder of its link.
func (s *Service) GetInvite(ctx context.Context, req *auth.GetInviteRequest) (*auth.GetInviteResponse, error) {
	if err := s.usable(); err != nil {
		return nil, err
	}
	tok := strings.TrimSpace(req.GetToken())
	if tok == "" {
		return nil, errCode()
	}
	inv, err := s.d.Store.InviteByToken(ctx, HashToken(tok), s.now())
	if errors.Is(err, platformstore.ErrTokenInvalid) {
		return nil, errCode()
	}
	if err != nil {
		s.log(ctx, "get invite", err)
		return nil, errInternal()
	}
	return &auth.GetInviteResponse{TeamName: inv.TeamName, Email: inv.Email, AccountExists: inv.AccountExists}, nil
}

// invitePermission is one entry of team_invite.permissions.
type invitePermission struct {
	Section string `json:"section"`
	Access  string `json:"access"`
}

// InvitePermissions reads team_invite.permissions ([{"section":"…","access":"read|write"}]); an
// owner invite is a super. Unknown sections and accesses are dropped (fail closed).
func InvitePermissions(raw []byte, role string) (super bool, perms []entity.AdminPermission, err error) {
	if role == "owner" {
		return true, nil, nil
	}
	var list []invitePermission
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &list); err != nil {
			return false, nil, fmt.Errorf("platformauth: invite permissions: %w", err)
		}
	}
	for _, p := range list {
		ap := entity.AdminPermission{Section: p.Section, Access: entity.AccessLevel(p.Access)}
		if rbac.ValidSection(ap.Section) && ap.Access.Valid() {
			perms = append(perms, ap)
		}
	}
	return false, perms, nil
}

// AcceptInvite joins the invited team: an existing account proves its password (the handle is
// ignored), a new one is created with the password and the chosen handle. A suspended team refuses.
func (s *Service) AcceptInvite(ctx context.Context, req *auth.AcceptInviteRequest) (*auth.AcceptInviteResponse, error) {
	if err := s.usable(); err != nil {
		return nil, err
	}
	tok := strings.TrimSpace(req.GetToken())
	if tok == "" {
		return nil, errCode()
	}
	if !s.signInIP.Allow(middleware.GetClientIP(ctx)) {
		return nil, errBusy()
	}
	now := s.now()
	tokHash := HashToken(tok)
	inv, err := s.d.Store.InviteByToken(ctx, tokHash, now)
	if errors.Is(err, platformstore.ErrTokenInvalid) {
		return nil, errCode()
	}
	if err != nil {
		s.log(ctx, "read invite", err)
		return nil, errInternal()
	}
	// The invited address shares the sign-in bucket: rotating IPs cannot guess its password here.
	if !s.signInLogin.Allow(strings.ToLower(strings.TrimSpace(inv.Email))) {
		return nil, errBusy()
	}
	pw := req.GetPassword()
	if len(pw) > MaxPasswordLen {
		return nil, errBad(errLongPassword.Error())
	}
	handle := ""
	terms := ""
	if !inv.AccountExists {
		if !req.GetTermsAccepted() {
			return nil, errTerms()
		}
		terms = s.d.TermsVersion
		if err := CheckPassword(pw); err != nil {
			return nil, errBad(err.Error())
		}
		if h := strings.ToLower(strings.TrimSpace(req.GetHandle())); h != "" {
			if err := CheckHandle(h); err != nil {
				return nil, errBad(err.Error())
			}
			handle = h
		}
	}
	// The invite stages and enables a row in the team's database: refused before the token is spent
	// while that database is behind the binary. A directory miss is left to the store's Stage step,
	// which reads the same entry and fails the same way it always has.
	if team, err := s.d.Store.Directory().ByID(ctx, inv.TeamID); err == nil && s.schemaBehind(team) {
		return nil, errUpdating()
	}
	hash, err := s.d.Hasher.HashPassword(pw)
	if err != nil {
		s.log(ctx, "hash password", err)
		return nil, errInternal()
	}
	code, nt, err := handoffToken(HandoffLongTTL, now)
	if err != nil {
		s.log(ctx, "hand-off token", err)
		return nil, errInternal()
	}
	res, err := s.d.Store.AcceptInvite(ctx, platformstore.AcceptParams{
		TokenHash:       tokHash,
		Now:             now,
		PasswordMatches: s.passwordMatcher(pw),
		NewPasswordHash: hash,
		Handle:          handle,
		HandleBase:      HandleBase(inv.Email),
		Handoff:         nt,
		TermsVersion:    terms,
		Stage:           s.stageInvite,
		Enable:          s.enableInvite,
	})
	switch {
	case errors.Is(err, platformstore.ErrTeamReadOnly):
		return nil, status.Error(codes.FailedPrecondition, "team_read_only")
	case errors.Is(err, platformstore.ErrWrongPassword):
		return nil, errAuth()
	case errors.Is(err, platformstore.ErrHandleTaken):
		return nil, status.Error(codes.AlreadyExists, "name taken")
	case errors.Is(err, platformstore.ErrTermsRequired):
		return nil, errTerms()
	case errors.Is(err, platformstore.ErrAlreadyMember):
		return nil, status.Error(codes.FailedPrecondition, "already_member")
	case errors.Is(err, platformstore.ErrTokenInvalid):
		return nil, errCode()
	case err != nil:
		s.log(ctx, "accept invite", err)
		return nil, errInternal()
	}
	if res.EnablePending {
		// Joined in the control plane; a row still staged is finished on the member's next sign-in.
		slog.Default().WarnContext(ctx, "platformauth: invite accepted, team row not enabled by the join")
	}
	s.audit(ctx, res.UserID, res.TeamID, "invite_accept", map[string]string{"new_account": fmt.Sprint(res.Created)})
	return &auth.AcceptInviteResponse{TeamSlug: res.TeamSlug, HandoffCode: code}, nil
}

// stageInvite is an invite's step 2 (AcceptParams.Stage): the member's admins row, disabled, with the
// invite's rights.
func (s *Service) stageInvite(ctx context.Context, inv platformstore.Invite, handle string) error {
	super, perms, err := InvitePermissions(inv.Permissions, inv.Role)
	if err != nil {
		return err
	}
	team, err := s.d.Store.Directory().ByID(ctx, inv.TeamID)
	if err != nil {
		return err
	}
	return s.d.Hooks.StageMember(ctx, team, handle, super, perms)
}

// enableInvite is an invite's last step (AcceptParams.Enable).
func (s *Service) enableInvite(ctx context.Context, inv platformstore.Invite, userID int64, handle string) (bool, error) {
	// Re-read: a member removed after step 3 is not enabled (their removal also clears the
	// team-side marker, so even a stale read cannot enable the row).
	if _, ok, err := s.d.Store.Membership(ctx, userID, inv.TeamID); err != nil || !ok {
		return false, err
	}
	team, err := s.d.Store.Directory().ByID(ctx, inv.TeamID)
	if err != nil {
		return false, err
	}
	return s.d.Hooks.EnableMember(ctx, team, handle)
}

// --- operators ---

// SyncOperators makes is_operator match PLATFORM_OPERATOR_EMAILS (boot, before the mail worker).
func (s *Service) SyncOperators(ctx context.Context) error {
	emails, invalid := ParseOperatorEmails(s.d.Config.OperatorEmails)
	for range invalid {
		slog.Default().WarnContext(ctx, "platformauth: PLATFORM_OPERATOR_EMAILS has an entry that is not an email; skipped")
	}
	created, err := s.d.Store.SyncOperators(ctx, emails, s.now(), UnusableHash(s.d.Hasher), HandleBase)
	if err != nil {
		return err
	}
	slog.Default().InfoContext(ctx, "platformauth: operators synced", slog.Int("listed", len(emails)), slog.Int("created", created))
	return nil
}
