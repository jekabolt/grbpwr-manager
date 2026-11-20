package auth

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"strconv"
	"strings"

	"github.com/go-chi/jwtauth/v5"
	"github.com/jekabolt/grbpwr-manager/internal/aiprov"
	"github.com/jekabolt/grbpwr-manager/internal/auth/jwt"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/feature"
	"github.com/jekabolt/grbpwr-manager/internal/observability"
	"github.com/jekabolt/grbpwr-manager/internal/platformstore"
	"github.com/jekabolt/grbpwr-manager/internal/rbac"
	"github.com/jekabolt/grbpwr-manager/internal/tenant"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
)

// Edge is what the admin chain needs from the tenant binder (internal/tenantbind.Binder implements
// it). Without one (tests, a zero Server) the chain runs as the single-tenant admin did: no team
// lookup, no origin lock, every feature on.
type Edge interface {
	// TeamsMode reports whether a control plane is wired (PLATFORM_MYSQL_DSN set).
	TeamsMode() bool
	TeamOne() tenant.Team
	TeamByID(ctx context.Context, id int64) (tenant.Team, error)
	SchemaBehind(t tenant.Team) bool
	OriginAllowed(origin string, t tenant.Team, legacy bool) bool
	// SessionActive: a console / root session (PlatformService chain) is live.
	SessionActive(ctx context.Context, sid string) (bool, error)
	// TeamSession re-checks the live tuple behind a team token: session sid of user uid in team
	// tid unrevoked, platform user active, membership active. role is the membership role.
	TeamSession(ctx context.Context, sid string, uid, tid int64) (role string, active bool, err error)
	// RequestedTeam is the team a request names by Host / X-Team (named=false: none). Its errors
	// are gRPC statuses (PermissionDenied team_mismatch, Unavailable).
	RequestedTeam(ctx context.Context, host, xteam string) (team tenant.Team, named bool, err error)
	Features(ctx context.Context, t tenant.Team) (map[feature.Key]bool, error)
	Operator(ctx context.Context, userID int64) (bool, error)
	// LegacyCredGen is the credential generation of the team #1 admin with this handle (0: no
	// linked platform user). A legacy team #1 token carries no session, so one whose generation
	// is not this one is refused. atLeast is the token's own: a cache behind it reads again.
	LegacyCredGen(ctx context.Context, handle string, atLeast int64) (int64, error)
}

// SetEdge wires the tenant binder into the admin chain. Called once at boot, before serving.
func (s *Server) SetEdge(e Edge) { s.edge = e }

// PlatformStore returns the control plane handed to SetPlatform; ok is false with teams mode off.
func (s *Server) PlatformStore() (*platformstore.Store, platformstore.Config, bool) {
	return s.platform.store, s.platform.cfg, s.platform.store != nil
}

func (s *Server) teamsMode() bool { return s.edge != nil && s.edge.TeamsMode() }

// Workspace is the team an admin call runs in, as GetCurrentAccount shows it.
type Workspace struct {
	Team     tenant.Team
	Role     string        // "owner" (super) or "member"
	Features []feature.Key // effective feature keys that are on, in catalog order
	ReadOnly bool          // the team is suspended
	// Operator: the caller is a console operator who entered the team (teams §23), not a member.
	Operator bool
	// UserID is the caller's platform user id (teams mode, platform token); 0 for a legacy token.
	UserID int64
}

const workspaceKey adminContextKey = "admin_workspace"

// GetWorkspace returns the workspace the admin chain resolved; ok is false outside it.
func GetWorkspace(ctx context.Context) (Workspace, bool) {
	w, ok := ctx.Value(workspaceKey).(Workspace)
	return w, ok
}

// PutWorkspace returns ctx carrying w, as the admin chain leaves it (tests, in-process callers).
func PutWorkspace(ctx context.Context, w Workspace) context.Context {
	return context.WithValue(ctx, workspaceKey, w)
}

// PutPlatformCaller returns ctx carrying c, as the platform chain leaves it (tests).
func PutPlatformCaller(ctx context.Context, c PlatformCaller) context.Context {
	return context.WithValue(ctx, platformCallerKey, c)
}

// PlatformCaller is the operator a PlatformService call runs for (aud=console token).
type PlatformCaller struct {
	UserID    int64
	Handle    string
	SessionID string
}

const platformCallerKey adminContextKey = "platform_caller"

// GetPlatformCaller returns the operator of a PlatformService call; ok is false elsewhere.
func GetPlatformCaller(ctx context.Context) (PlatformCaller, bool) {
	c, ok := ctx.Value(platformCallerKey).(PlatformCaller)
	return c, ok
}

// Error texts the admin client reads (teams §6–7).
const (
	msgTeamReadOnly = "team_read_only"
	msgFeatureOff   = "feature_off"
	msgTeamMismatch = "team_mismatch"
)

// adminCall is one admin request as the chain sees it.
type adminCall struct {
	token  string
	origin string
	// host and xteam name the team the request is presented as (Host / X-Forwarded-Host, X-Team).
	host  string
	xteam string
	// method is the gRPC full method, or "METHOD /route/pattern" on the chi routes (for logs).
	method string
	// readOnly: the call only reads, so it passes on a suspended team.
	readOnly bool
	// features are the feature keys the call needs; ok=false fails closed (feature_off).
	features func() ([]feature.Key, bool)
	// grpcMethod is set on gRPC calls: RBAC is checked against it. The chi routes check the
	// section in the handler (RequireAdminSection), as before.
	grpcMethod string
}

// adminChain runs the admin chain of teams §6 and returns the context the handler runs with:
// verify JWT → team → status gate → Origin lock → session → account state → feature → RBAC →
// tenant.With. Errors are gRPC status errors; the HTTP wrappers map them to codes.
//
// Teams mode off (no control plane) it is the single-tenant chain: legacy tokens only, team #1
// (already bound by the tenant binder), no origin lock and no session check.
func (s *Server) adminChain(ctx context.Context, call adminCall) (context.Context, error) {
	claims, err := jwt.VerifyTeamToken(s.JwtAuth, call.token, s.jwtExpectations)
	if err != nil {
		// The raw jwx error is an oracle for probers: keep it server-side.
		slog.Default().WarnContext(ctx, "invalid admin auth token",
			slog.String("method", call.method), slog.String("err", err.Error()))
		return ctx, status.Error(codes.Unauthenticated, "unauthorized")
	}
	teamsMode := s.teamsMode()
	tid := claims.TeamID
	if claims.Legacy {
		tid = tenant.TeamOneID
	} else if !teamsMode || claims.Audience != jwt.AudTeam {
		slog.Default().WarnContext(ctx, "admin call with a non-team token",
			slog.String("method", call.method), slog.String("aud", claims.Audience), slog.Bool("teams_mode", teamsMode))
		return ctx, status.Error(codes.Unauthenticated, "unauthorized")
	}

	var team tenant.Team
	if s.edge != nil {
		team, err = s.edge.TeamByID(ctx, tid)
		if err != nil {
			if errors.Is(err, tenant.ErrNotFound) {
				return ctx, status.Error(codes.Unauthenticated, "unauthorized")
			}
			slog.Default().ErrorContext(ctx, "admin call: team lookup failed",
				slog.Int64("tid", tid), slog.String("err", err.Error()))
			return ctx, status.Error(codes.Unavailable, "team unavailable")
		}
		if err := s.statusGate(team, call.readOnly); err != nil {
			return ctx, err
		}
	} else {
		team = tenant.Team{ID: tenant.TeamOneID, Slug: tenant.TeamOneSlug, Name: tenant.TeamOneName,
			Profile: tenant.ProfileAll, Status: tenant.StatusActive}
	}

	if teamsMode {
		if !s.edge.OriginAllowed(call.origin, team, claims.Legacy) {
			slog.Default().WarnContext(ctx, "admin call from a foreign origin",
				slog.Int64("tid", team.ID), slog.String("origin", call.origin), slog.Bool("legacy", claims.Legacy))
			return ctx, status.Error(codes.Unauthenticated, "unauthorized")
		}
		// The team the request is presented as (Host / X-Team) must be the token's team: a
		// team-A token never runs while the request names team B.
		named, isNamed, err := s.edge.RequestedTeam(ctx, call.host, call.xteam)
		if err != nil {
			slog.Default().WarnContext(ctx, "admin call: requested team refused",
				slog.Int64("tid", team.ID), slog.String("host", call.host), slog.String("x_team", call.xteam),
				slog.String("err", err.Error()))
			return ctx, err
		}
		if isNamed && named.ID != team.ID {
			slog.Default().WarnContext(ctx, "admin call presented as another team",
				slog.Int64("tid", team.ID), slog.Int64("requested", named.ID))
			return ctx, status.Error(codes.PermissionDenied, msgTeamMismatch)
		}
	}

	// The account state reads the team's admins table, so the team is bound first. Mode off the
	// binder has already bound team #1; binding it again is the same value.
	if s.edge != nil {
		ctx = tenant.With(ctx, team)
	}
	authz, err := s.teamAccess(ctx, team, call.token, claims)
	if err != nil {
		return ctx, err
	}

	eff, err := s.features(ctx, team)
	if err != nil {
		slog.Default().ErrorContext(ctx, "admin call: feature lookup failed",
			slog.Int64("tid", team.ID), slog.String("err", err.Error()))
		return ctx, status.Error(codes.Unavailable, "features unavailable")
	}
	keys, known := call.features()
	if !known || !feature.Allowed(eff, keys) {
		slog.Default().WarnContext(ctx, "admin call: feature off",
			slog.Int64("tid", team.ID), slog.String("method", call.method))
		return ctx, status.Error(codes.PermissionDenied, msgFeatureOff)
	}

	if call.grpcMethod != "" && !authz.allows(call.grpcMethod) {
		slog.Default().WarnContext(ctx, "admin authorization denied",
			slog.String("username", claims.Handle), slog.String("method", call.grpcMethod))
		return ctx, status.Errorf(codes.PermissionDenied, "insufficient permissions for %s", call.grpcMethod)
	}

	slog.Default().InfoContext(ctx, "admin call",
		slog.Int64("tid", team.ID),
		slog.String("db", team.DBName),
		slog.String("sub", claims.Handle),
		slog.String("origin", call.origin),
		slog.String("method", call.method),
	)
	w := workspaceOf(team, authz.Super, eff)
	// teamAccess let an op token through only on its operator branch.
	w.Operator = claims.Operator
	if !claims.Legacy {
		w.UserID = claims.UserID
	}
	return s.populateAdmin(ctx, claims.Handle, authz, w), nil
}

// claimOperator marks a team token minted by operator entry (a console operator opening a team's
// workspace through a hand-off code). Its holder is a platform user who is not a member of the
// team and has no admins row in its database.
const claimOperator = "op"

// teamAccess is THE decision «may this verified token still act on this team», and the only place
// that reads membership: every admin entry point (adminChain for gRPC and the upload routes,
// authenticate for WithAuth) calls it, no handler looks membership up on its own. ctx carries the
// team already (the account lookup reads the team's admins table).
//
// In this order: an op token takes the operator branch; in teams mode a team token's live control tuple must
// hold (session unrevoked and matching sid / uid / tid, platform user active, membership active —
// a removed member or a disabled user is refused within the binder's cache TTL, at once on the
// instance that removed them); the subject's admins row in the team must exist and be enabled. The
// authorization is the token's perms and its super claim, the latter only while the membership is
// still owner (a demoted owner loses super at once). Mode OFF a pre-RBAC token keeps full non-super
// access.
//
// Operator entry (teams §23): an op token is accepted only in teams mode, only as a platform team
// token whose op claim is true, while its session is unrevoked and its user is still an active
// operator (both re-checked through the edge's 15 s caches) — instead of a membership and an admins
// row, which the operator does not have. It acts as super in the team; a suspended team stays
// read-only (statusGate ran before). Any other token carrying op is refused, fail closed.
func (s *Server) teamAccess(ctx context.Context, team tenant.Team, token string, claims jwt.TeamClaims) (AdminAuthz, error) {
	if hasClaim(s.JwtAuth, token, claimOperator) {
		if !s.teamsMode() || claims.Legacy || !claims.Operator || claims.Audience != jwt.AudTeam {
			slog.Default().WarnContext(ctx, "operator-entry token refused",
				slog.Int64("tid", team.ID), slog.String("sub", claims.Handle))
			return AdminAuthz{}, status.Error(codes.Unauthenticated, "unauthorized")
		}
		live, err := s.edge.SessionActive(ctx, claims.SessionID)
		if err != nil {
			slog.Default().ErrorContext(ctx, "operator entry: session check failed", slog.String("err", err.Error()))
			return AdminAuthz{}, status.Error(codes.Unavailable, "session check unavailable")
		}
		op := false
		if live {
			if op, err = s.edge.Operator(ctx, claims.UserID); err != nil {
				slog.Default().ErrorContext(ctx, "operator entry: operator check failed", slog.String("err", err.Error()))
				return AdminAuthz{}, status.Error(codes.Unavailable, "operator check unavailable")
			}
		}
		if !live || !op {
			slog.Default().WarnContext(ctx, "operator entry: session or operator not live",
				slog.Int64("tid", team.ID), slog.Int64("uid", claims.UserID))
			return AdminAuthz{}, status.Error(codes.Unauthenticated, "unauthorized")
		}
		return AdminAuthz{Super: true}, nil
	}
	super := claims.Super
	if s.teamsMode() && !claims.Legacy {
		role, active, err := s.edge.TeamSession(ctx, claims.SessionID, claims.UserID, team.ID)
		if err != nil {
			slog.Default().ErrorContext(ctx, "admin call: session check failed", slog.String("err", err.Error()))
			return AdminAuthz{}, status.Error(codes.Unavailable, "session check unavailable")
		}
		if !active {
			slog.Default().WarnContext(ctx, "admin call: session, user or membership not live",
				slog.Int64("tid", team.ID), slog.Int64("uid", claims.UserID))
			return AdminAuthz{}, status.Error(codes.Unauthenticated, "unauthorized")
		}
		super = super && role == roleOwner
	}
	if s.teamsMode() && claims.Legacy {
		if err := s.legacyNotRevoked(ctx, claims); err != nil {
			return AdminAuthz{}, err
		}
	}
	if err := s.checkAdminAccountState(ctx, claims.Handle); err != nil {
		return AdminAuthz{}, err
	}
	// Teams mode OFF is an identity: a legacy token minted before the super / perms claims existed
	// keeps full non-super access, as before teams. Teams mode keeps the strict rule.
	if !s.teamsMode() && claims.Legacy && !hasClaim(s.JwtAuth, token, claimSuper) {
		return AdminAuthz{Legacy: true}, nil
	}
	return AdminAuthz{Super: super, Perms: rbac.ParsePermissions(claims.Perms)}, nil
}

// legacyNotRevoked refuses a legacy team #1 token whose credential generation (claim cg, absent =
// 0) is not its account's current one: every password change (reset, change, admin reset,
// re-created account) bumps it in the same transaction, and the token a change hands back is
// minted after the commit with the new generation. Equality, not ordering: no clock involved.
func (s *Server) legacyNotRevoked(ctx context.Context, claims jwt.TeamClaims) error {
	gen, err := s.edge.LegacyCredGen(ctx, claims.Handle, claims.CredGen)
	if err != nil {
		slog.Default().ErrorContext(ctx, "admin call: credential generation lookup failed", slog.String("err", err.Error()))
		return status.Error(codes.Unavailable, "session check unavailable")
	}
	if claims.CredGen != gen {
		slog.Default().WarnContext(ctx, "admin call: legacy token of an old credential generation")
		return status.Error(codes.Unauthenticated, "unauthorized")
	}
	return nil
}

// roleOwner is the team_member role that may carry super.
const roleOwner = "owner"

// claimSuper is the super claim of an admin token; its absence marks a pre-RBAC token.
const claimSuper = "super"

// allows is the RBAC decision for an admin method under this authorization.
func (a AdminAuthz) allows(fullMethod string) bool {
	if a.Legacy {
		return rbac.AllowPreRBAC(fullMethod)
	}
	return rbac.Allow(fullMethod, a.Super, a.Perms)
}

// hasClaim reports whether an already verified token carries a claim jwt.TeamClaims does not
// expose. A token that no longer verifies counts as carrying it (fail closed).
func hasClaim(a *jwtauth.JWTAuth, token, claim string) bool {
	t, err := jwtauth.VerifyToken(a, token)
	if err != nil {
		return true
	}
	_, ok := t.PrivateClaims()[claim]
	return ok
}

// statusGate is teams §7: deleted → 401, provisioning or schema behind → UNAVAILABLE, suspended →
// reads only.
func (s *Server) statusGate(team tenant.Team, readOnly bool) error {
	switch team.Status {
	case tenant.StatusDeleted:
		return status.Error(codes.Unauthenticated, "unauthorized")
	case tenant.StatusPending, tenant.StatusProvisioning:
		return status.Error(codes.Unavailable, "team is being set up")
	case tenant.StatusSuspended:
		if !readOnly {
			return status.Error(codes.FailedPrecondition, msgTeamReadOnly)
		}
	case tenant.StatusActive:
	default:
		return status.Error(codes.Unavailable, "team unavailable")
	}
	if s.edge.SchemaBehind(team) {
		return status.Error(codes.Unavailable, "team is being updated")
	}
	return nil
}

func (s *Server) features(ctx context.Context, team tenant.Team) (map[feature.Key]bool, error) {
	if s.edge == nil {
		return feature.Effective(tenant.TeamOneID, tenant.ProfileAll, nil), nil
	}
	return s.edge.Features(ctx, team)
}

func workspaceOf(team tenant.Team, super bool, eff map[feature.Key]bool) Workspace {
	w := Workspace{Team: team, Role: "member", ReadOnly: team.Status == tenant.StatusSuspended}
	if super {
		w.Role = "owner"
	}
	for _, info := range feature.Catalog() {
		if eff[info.Key] {
			w.Features = append(w.Features, info.Key)
		}
	}
	return w
}

// populateAdmin puts the resolved caller into ctx: username, authorization, workspace, and the
// AI ledger / Sentry actor.
func (s *Server) populateAdmin(ctx context.Context, sub string, authz AdminAuthz, w Workspace) context.Context {
	if sub != "" {
		ctx = PutAdminUsername(ctx, sub)
		// WHO a provider call is made for — the AI spend ledger's actor. A failed id lookup never
		// fails the call: the actor then carries the username alone.
		adminID := s.adminIDFor(ctx, sub)
		ctx = aiprov.WithActor(ctx, aiprov.Actor{Username: sub, AdminID: adminID})
		observability.SetAdmin(ctx, adminID)
	}
	ctx = putAdminAuthz(ctx, authz)
	return context.WithValue(ctx, workspaceKey, w)
}

// platformChain guards PlatformService (teams §6): an aud=console token with a live session whose
// user is still an active operator. No team is bound. Refused with teams mode off.
func (s *Server) platformChain(ctx context.Context, method string) (context.Context, error) {
	if !s.teamsMode() {
		return ctx, status.Error(codes.Unimplemented, "teams mode is off")
	}
	token, err := GetTokenMetadata(ctx)
	if err != nil {
		return ctx, status.Error(codes.Unauthenticated, "unauthorized")
	}
	claims, err := jwt.VerifyTeamToken(s.JwtAuth, token, &jwt.Expectations{Audience: jwt.AudConsole})
	if err != nil || claims.Legacy || claims.Audience != jwt.AudConsole {
		if err != nil {
			slog.Default().WarnContext(ctx, "invalid platform token", slog.String("err", err.Error()))
		}
		return ctx, status.Error(codes.Unauthenticated, "unauthorized")
	}
	// With PLATFORM_OPERATOR_SECOND_STEP on, a console token minted without the authenticator-app
	// step (before the switch, or by a password alone) opens nothing.
	if s.platform.cfg.SecondStepOn() && !claims.SecondStep {
		return ctx, status.Error(codes.Unauthenticated, "unauthorized")
	}
	ok, err := s.edge.SessionActive(ctx, claims.SessionID)
	if err != nil {
		return ctx, status.Error(codes.Unavailable, "session check unavailable")
	}
	if !ok {
		return ctx, status.Error(codes.Unauthenticated, "unauthorized")
	}
	op, err := s.edge.Operator(ctx, claims.UserID)
	if err != nil {
		return ctx, status.Error(codes.Unavailable, "operator check unavailable")
	}
	if !op {
		slog.Default().WarnContext(ctx, "platform call by a non-operator",
			slog.String("sub", claims.Handle), slog.String("method", method))
		return ctx, status.Error(codes.PermissionDenied, "operators only")
	}
	slog.Default().InfoContext(ctx, "platform call", slog.String("sub", claims.Handle), slog.String("method", method))
	return context.WithValue(ctx, platformCallerKey, PlatformCaller{
		UserID: claims.UserID, Handle: claims.Handle, SessionID: claims.SessionID,
	}), nil
}

const platformServicePrefix = "/admin.PlatformService/"

// guard runs the chain a gRPC full method needs; other services pass through untouched.
func (s *Server) guard(ctx context.Context, fullMethod string) (context.Context, error) {
	switch {
	case strings.HasPrefix(fullMethod, adminServicePrefix):
		token, err := GetTokenMetadata(ctx)
		if err != nil {
			return ctx, status.Error(codes.Unauthenticated, "unauthorized")
		}
		access, _ := rbac.Access(fullMethod)
		return s.adminChain(ctx, adminCall{
			token:      token,
			origin:     grpcOrigin(ctx),
			host:       grpcHost(ctx),
			xteam:      tenant.HeaderSlug(ctx),
			method:     fullMethod,
			readOnly:   access == entity.AccessRead,
			features:   func() ([]feature.Key, bool) { return feature.ForMethod(fullMethod) },
			grpcMethod: fullMethod,
		})
	case strings.HasPrefix(fullMethod, platformServicePrefix):
		return s.platformChain(ctx, fullMethod)
	}
	return ctx, nil
}

// UnaryAdminAuthInterceptor guards every AdminService and PlatformService call (adminChain,
// platformChain). There is no pre-RBAC "no super claim = full access" rule any more (teams §3).
func (s *Server) UnaryAdminAuthInterceptor() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		ctx, err := s.guard(ctx, info.FullMethod)
		if err != nil {
			return nil, err
		}
		return handler(ctx, req)
	}
}

// StreamAdminAuthInterceptor is the same guard for streaming methods (none today; a future
// streaming AdminService method must not run unguarded).
func (s *Server) StreamAdminAuthInterceptor() grpc.StreamServerInterceptor {
	return func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		ctx, err := s.guard(ss.Context(), info.FullMethod)
		if err != nil {
			return err
		}
		return handler(srv, &guardedStream{ServerStream: ss, ctx: ctx})
	}
}

type guardedStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (g *guardedStream) Context() context.Context { return g.ctx }

// grpcOrigin is the browser Origin of a call: grpc-gateway forwards it as grpcgateway-origin; a
// direct gRPC-web or native client may send origin.
func grpcOrigin(ctx context.Context) string {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return ""
	}
	for _, k := range []string{"grpcgateway-origin", "origin"} {
		if v := md.Get(k); len(v) > 0 && strings.TrimSpace(v[0]) != "" {
			return strings.TrimSpace(v[0])
		}
	}
	return ""
}

// grpcHost is the host a gRPC call was addressed to. x-forwarded-host is the grpc-gateway's
// annotation of the public Host, so it is trusted only on the gateway's loopback hop (peer
// 127.0.0.1 / ::1); any other caller could forge it, so a direct gRPC call answers by :authority.
// The HTTP side strips a client X-Forwarded-Host before the gateway (api/http), so the annotation
// is r.Host.
func grpcHost(ctx context.Context) string {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return ""
	}
	keys := []string{":authority"}
	if fromLoopback(ctx) {
		keys = []string{"x-forwarded-host", ":authority"}
	}
	for _, k := range keys {
		if v := md.Get(k); len(v) > 0 && strings.TrimSpace(v[0]) != "" {
			return strings.TrimSpace(v[0])
		}
	}
	return ""
}

// fromLoopback reports whether the gRPC peer is this host (the grpc-gateway dials its own port).
func fromLoopback(ctx context.Context) bool {
	p, ok := peer.FromContext(ctx)
	if !ok || p.Addr == nil {
		return false
	}
	host := p.Addr.String()
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// accountKey is the account-state / admin-id cache key: (team, username). Team #1 (and a context
// with no team) keeps the bare username, so the single-tenant cache is unchanged.
func accountKey(ctx context.Context, username string) string {
	if t, ok := tenant.From(ctx); ok && t.ID != tenant.TeamOneID {
		return "#" + strconv.FormatInt(t.ID, 10) + "\x00" + username
	}
	return username
}

// accountKeyUser is the username part of an accountKey.
func accountKeyUser(key string) string {
	if strings.HasPrefix(key, "#") {
		if i := strings.IndexByte(key, 0); i >= 0 {
			return key[i+1:]
		}
	}
	return key
}
