package auth

import (
	"context"
	"errors"
	"log/slog"
	"strconv"
	"strings"

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
	SessionActive(ctx context.Context, sid string) (bool, error)
	Features(ctx context.Context, t tenant.Team) (map[feature.Key]bool, error)
	Operator(ctx context.Context, userID int64) (bool, error)
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
}

const workspaceKey adminContextKey = "admin_workspace"

// GetWorkspace returns the workspace the admin chain resolved; ok is false outside it.
func GetWorkspace(ctx context.Context) (Workspace, bool) {
	w, ok := ctx.Value(workspaceKey).(Workspace)
	return w, ok
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
)

// adminCall is one admin request as the chain sees it.
type adminCall struct {
	token  string
	origin string
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
		// An empty sid is only possible on a legacy (tid 1, old Login) token, which the
		// account-state check below still covers.
		if !claims.Legacy {
			ok, err := s.edge.SessionActive(ctx, claims.SessionID)
			if err != nil {
				slog.Default().ErrorContext(ctx, "admin call: session check failed", slog.String("err", err.Error()))
				return ctx, status.Error(codes.Unavailable, "session check unavailable")
			}
			if !ok {
				return ctx, status.Error(codes.Unauthenticated, "unauthorized")
			}
		}
	}

	// The account state reads the team's admins table, so the team is bound first. Mode off the
	// binder has already bound team #1; binding it again is the same value.
	if s.edge != nil {
		ctx = tenant.With(ctx, team)
	}
	if err := s.checkAdminAccountState(ctx, claims.Handle); err != nil {
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

	perms := rbac.ParsePermissions(claims.Perms)
	if call.grpcMethod != "" && !rbac.Allow(call.grpcMethod, claims.Super, perms) {
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
	return s.populateAdmin(ctx, claims.Handle, AdminAuthz{Super: claims.Super, Perms: perms}, workspaceOf(team, claims.Super, eff)), nil
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
