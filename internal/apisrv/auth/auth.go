package auth

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"log/slog"

	"github.com/go-chi/jwtauth/v5"
	"github.com/jekabolt/grbpwr-manager/internal/auth/jwt"
	"github.com/jekabolt/grbpwr-manager/internal/auth/pwhash"
	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/middleware"
	"github.com/jekabolt/grbpwr-manager/internal/ratelimit"
	"github.com/jekabolt/grbpwr-manager/internal/rbac"
	"github.com/jekabolt/grbpwr-manager/proto/gen/auth"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

const (
	// AuthMetadataKey is header key to match auth token
	AuthMetadataKey = "Grpc-Metadata-Authorization"
)

// Brute-force throttling for the admin auth RPCs. These endpoints gate admin
// account creation/deletion, password changes and login, and are exposed to the
// internet, so unlimited online password guessing against an admin account or
// the master password must be stopped. PBKDF2 slows each guess but does not cap
// the total, so we add an in-memory sliding-window limiter (the same primitive
// the frontend service uses) keyed by client IP and, where present, by the
// targeted username. Limits are intentionally tight for admin auth.
const (
	// authRateWindow is the sliding window over which auth attempts are counted.
	authRateWindow = time.Minute
	// authMaxPerIP caps auth attempts per client IP per window (network-wide
	// guessing, e.g. against the master password which has no username key).
	authMaxPerIP = 10
	// authMaxPerUser caps auth attempts per targeted username per window
	// (tighter, since a single account should never see this many attempts).
	authMaxPerUser = 5
)

// Server implements the heartbeat service.
type Server struct {
	auth.UnimplementedAuthServiceServer
	adminRepository dependency.Admin
	pwhash          *pwhash.PasswordHasher
	JwtAuth         *jwtauth.JWTAuth
	jwtTTL          time.Duration
	jwtExpectations *jwt.Expectations
	c               *Config
	masterHash      string
	rateLimiter     *authRateLimiter
}

// authRateLimiter throttles brute-force attempts against the admin auth RPCs.
// It reuses the same in-memory sliding-window limiter primitive the frontend
// service uses (ratelimit.MultiKeyLimiter is built from these), keyed per IP
// and per targeted username.
type authRateLimiter struct {
	ip   *ratelimit.Limiter
	user *ratelimit.Limiter
}

func newAuthRateLimiter() *authRateLimiter {
	return &authRateLimiter{
		ip:   ratelimit.NewLimiter(authRateWindow, authMaxPerIP),
		user: ratelimit.NewLimiter(authRateWindow, authMaxPerUser),
	}
}

// stop terminates the cleanup goroutines of both underlying limiters.
func (l *authRateLimiter) stop() {
	l.ip.Stop()
	l.user.Stop()
}

// check applies the per-IP limit always and, when a username is supplied, the
// tighter per-username limit. It returns a ResourceExhausted gRPC error so the
// throttled response is distinct from a normal failed login at the transport
// level. An empty ip is still rate limited as a single bucket (fail closed
// rather than skip when the client-IP middleware did not populate it).
func (l *authRateLimiter) check(ip, username string) error {
	if !l.ip.Allow(ip) {
		return status.Errorf(codes.ResourceExhausted, "too many attempts, please try again later")
	}
	if username != "" && !l.user.Allow(username) {
		return status.Errorf(codes.ResourceExhausted, "too many attempts, please try again later")
	}
	return nil
}

// Config contains the configuration for the auth server.
type Config struct {
	JWTSecret                string `mapstructure:"jwt_secret"`
	JWTIssuer                string `mapstructure:"jwt_issuer"`
	JWTAudience              string `mapstructure:"jwt_audience"`
	MasterPassword           string `mapstructure:"master_password"`
	PasswordHasherSaltSize   int    `mapstructure:"password_hasher_salt_size"`
	PasswordHasherIterations int    `mapstructure:"password_hasher_iterations"`
	JWTTTL                   string `mapstructure:"jwt_ttl"`
}

// New creates a new auth server.
func New(c *Config, ar dependency.Admin) (*Server, error) {

	// An empty HS256 secret would validate any token signed with an empty key,
	// allowing trivial admin token forgery. Fail closed at startup — and also
	// reject a too-short secret, since HS256 strength equals the key's strength.
	if c.JWTSecret == "" {
		return nil, fmt.Errorf("auth.jwt_secret is required")
	}
	if err := jwt.RequireStrongSecret("auth.jwt_secret", c.JWTSecret); err != nil {
		return nil, err
	}

	// Trim surrounding whitespace: secret managers / env injection frequently add
	// a trailing newline, which would otherwise make the master password never
	// match what callers send. Fail closed only if it's unset (a functional
	// requirement); merely warn if it's weak, so a legacy short password logs loudly
	// but does not block startup. This is a human-entered password, so the floor is
	// softer than for machine secrets.
	masterPassword := strings.TrimSpace(c.MasterPassword)
	if masterPassword == "" {
		return nil, fmt.Errorf("auth.master_password is required")
	}
	if len(masterPassword) < 12 {
		slog.Warn("weak auth.master_password — rotate to at least 12 characters",
			"got_chars", len(masterPassword))
	}

	ph, err := pwhash.New(c.PasswordHasherSaltSize, c.PasswordHasherIterations)
	if err != nil {
		return nil, fmt.Errorf("failed to create password hasher: %w", err)
	}
	hash, err := ph.HashPassword(masterPassword)
	if err != nil {
		return nil, fmt.Errorf("failed to hash master password: %w", err)
	}

	if err := ph.Validate(masterPassword, hash); err != nil {
		return nil, fmt.Errorf("failed to validate master password: %w", err)
	}

	ttl, err := time.ParseDuration(c.JWTTTL)
	if err != nil {
		return nil, fmt.Errorf("failed to parse jwt ttl: %w", err)
	}
	var jwtExp *jwt.Expectations
	if c.JWTIssuer != "" || c.JWTAudience != "" {
		jwtExp = &jwt.Expectations{Issuer: c.JWTIssuer, Audience: c.JWTAudience}
	}
	s := &Server{
		adminRepository: ar,
		pwhash:          ph,
		JwtAuth:         jwtauth.New("HS256", []byte(c.JWTSecret), nil),
		jwtTTL:          ttl,
		jwtExpectations: jwtExp,
		c:               c,
		masterHash:      hash,
		rateLimiter:     newAuthRateLimiter(),
	}

	return s, nil
}

// StopRateLimiter terminates the auth rate-limiter cleanup goroutines. Called
// from App.Stop so the limiters follow the same lifecycle discipline as the
// other background components (idempotent).
func (s *Server) StopRateLimiter() {
	if s.rateLimiter != nil {
		s.rateLimiter.stop()
	}
}

func (s *Server) jwtIssueOpts() *jwt.IssueOpts {
	if s.c.JWTIssuer == "" && s.c.JWTAudience == "" {
		return nil
	}
	return &jwt.IssueOpts{Issuer: s.c.JWTIssuer, Audience: s.c.JWTAudience}
}

// Login get auth token for provided username and password.
func (s *Server) Login(ctx context.Context, req *auth.LoginRequest) (*auth.LoginResponse, error) {
	username := strings.ToLower(req.Username)

	// Throttle online password guessing per IP and per targeted username before
	// doing any (expensive) PBKDF2 work or DB lookup.
	ip := middleware.GetClientIP(ctx)
	if err := s.rateLimiter.check(ip, username); err != nil {
		slog.Default().WarnContext(ctx, "login attempt throttled",
			slog.String("ip", ip),
			slog.String("username", username),
		)
		return nil, err
	}

	account, err := s.adminRepository.GetAccountWithPermissions(ctx, username)
	if err != nil {
		// Unknown username is an expected failed login (client 401), not a server
		// error — log at warn to keep it out of error dashboards. A genuine DB
		// failure still logs at error so ops can tell the two apart.
		if errors.Is(err, sql.ErrNoRows) {
			slog.Default().WarnContext(ctx, "login attempt for unknown username",
				slog.String("username", username),
			)
		} else {
			slog.Default().ErrorContext(ctx, "failed to get account by username",
				slog.String("username", username),
				slog.String("err", err.Error()),
			)
		}
		return nil, status.Errorf(codes.Unauthenticated, "not authenticated")
	}

	err = s.pwhash.Validate(req.Password, account.PasswordHash)
	if err != nil {
		// Wrong password is also an expected failed login, not a server error.
		slog.Default().WarnContext(ctx, "login attempt with invalid password",
			slog.String("username", username),
		)
		return nil, status.Errorf(codes.Unauthenticated, "not authenticated")
	}

	// A disabled account keeps a valid password but may not obtain new tokens.
	// Enforcement is stateless (permissions ride in the JWT), so this is the point
	// where a revoked account is actually shut out; any still-valid token it holds
	// lives until it expires.
	if account.Disabled {
		slog.Default().WarnContext(ctx, "login attempt for disabled account",
			slog.String("username", username),
		)
		return nil, status.Errorf(codes.PermissionDenied, "account is disabled")
	}

	token, err := jwt.NewAdminToken(s.JwtAuth, s.jwtTTL, username,
		account.IsSuper, rbac.EncodePermissions(account.Permissions), s.jwtIssueOpts())
	if err != nil {
		slog.Default().ErrorContext(ctx, "failed to create jwt token",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Unauthenticated, "not authenticated")
	}

	return &auth.LoginResponse{
		AuthToken: token,
	}, nil
}

// Create bootstraps a new super-admin account, gated by the master password.
// This is the bootstrap path: it always creates a full-access (super) account.
// Scoped accounts are created in-panel via AdminService, which is itself gated by
// the accounts permission a super-admin holds.
func (s *Server) Create(ctx context.Context, req *auth.CreateRequest) (*auth.CreateResponse, error) {

	// Gated by the master password, which has no username key — throttle per IP
	// to stop online guessing of the master password.
	ip := middleware.GetClientIP(ctx)
	if err := s.rateLimiter.check(ip, ""); err != nil {
		slog.Default().WarnContext(ctx, "admin create attempt throttled",
			slog.String("ip", ip),
		)
		return nil, err
	}

	err := s.pwhash.Validate(strings.TrimSpace(req.MasterPassword), s.masterHash)
	if err != nil {
		slog.Default().ErrorContext(ctx, "failed to validate master password",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Unauthenticated, "not authenticated")
	}

	if req.GetUser() == nil {
		return nil, status.Errorf(codes.InvalidArgument, "user is required")
	}

	username := strings.ToLower(req.User.Username)

	pwHash, err := s.pwhash.HashPassword(req.User.Password)
	if err != nil {
		slog.Default().ErrorContext(ctx, "failed to hash password",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Unauthenticated, "not authenticated")
	}

	// Bootstrap accounts are super-admins: full access, no per-section grants.
	token, err := jwt.NewAdminToken(s.JwtAuth, s.jwtTTL, username, true, nil, s.jwtIssueOpts())
	if err != nil {
		slog.Default().ErrorContext(ctx, "failed to create jwt token",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Unauthenticated, "not authenticated")
	}

	err = s.adminRepository.AddAccount(ctx, username, pwHash, true, nil)
	if err != nil {
		slog.Default().ErrorContext(ctx, "failed to add admin",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Unauthenticated, "not authenticated")
	}
	return &auth.CreateResponse{
		AuthToken: token,
	}, nil

}

// Delete deletes a user.
func (s *Server) Delete(ctx context.Context, req *auth.DeleteRequest) (*auth.DeleteResponse, error) {
	// Gated by the master password (no per-account secret to guess) — throttle
	// per IP to stop online guessing of the master password.
	ip := middleware.GetClientIP(ctx)
	if err := s.rateLimiter.check(ip, ""); err != nil {
		slog.Default().WarnContext(ctx, "admin delete attempt throttled",
			slog.String("ip", ip),
		)
		return nil, err
	}

	err := s.pwhash.Validate(strings.TrimSpace(req.MasterPassword), s.masterHash)
	if err != nil {
		slog.Default().ErrorContext(ctx, "failed to validate master password",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Unauthenticated, "not authenticated")
	}
	username := strings.ToLower(req.Username)
	err = s.adminRepository.DeleteAdmin(ctx, username)
	if err != nil {
		slog.Default().ErrorContext(ctx, "failed to delete admin",
			slog.String("username", username),
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "failed to delete admin")
	}
	return &auth.DeleteResponse{}, nil
}

// ChangePassword changes the password of the user. It requires the old password or admin password provided.
func (s *Server) ChangePassword(ctx context.Context, req *auth.ChangePasswordRequest) (*auth.ChangePasswordResponse, error) {
	username := strings.ToLower(req.Username)

	// Accepts either the account's current password or the master password, so
	// throttle per IP and per targeted username to stop guessing of either.
	ip := middleware.GetClientIP(ctx)
	if err := s.rateLimiter.check(ip, username); err != nil {
		slog.Default().WarnContext(ctx, "change password attempt throttled",
			slog.String("ip", ip),
			slog.String("username", username),
		)
		return nil, err
	}

	account, err := s.adminRepository.GetAccountWithPermissions(ctx, username)
	if err != nil {
		slog.Default().ErrorContext(ctx, "failed to get account by username",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Unauthenticated, "not authenticated")
	}

	err = s.pwhash.Validate(req.CurrentPassword, s.masterHash)
	if err != nil {
		err = s.pwhash.Validate(req.CurrentPassword, account.PasswordHash)
		if err != nil {
			slog.Default().ErrorContext(ctx, "failed to validate current password",
				slog.String("err", err.Error()),
			)
			return nil, status.Errorf(codes.Unauthenticated, "not authenticated")
		}
	}

	pwHashNew, err := s.pwhash.HashPassword(req.NewPassword)
	if err != nil {
		slog.Default().ErrorContext(ctx, "failed to hash new password",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Unauthenticated, "not authenticated")
	}

	// Re-issue the token with the account's current authorization so a password
	// change never widens or drops the caller's permissions.
	token, err := jwt.NewAdminToken(s.JwtAuth, s.jwtTTL, username,
		account.IsSuper, rbac.EncodePermissions(account.Permissions), s.jwtIssueOpts())
	if err != nil {
		slog.Default().ErrorContext(ctx, "failed to create jwt token",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Unauthenticated, "not authenticated")
	}

	err = s.adminRepository.ChangePassword(ctx, username, pwHashNew)
	if err != nil {
		slog.Default().ErrorContext(ctx, "failed to change password",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Unauthenticated, "not authenticated")
	}
	return &auth.ChangePasswordResponse{
		AuthToken: token,
	}, nil
}

// Error message struct
type errorMessage struct {
	Error string `json:"error"`
}

// WithAuth middleware checks if the user is authenticated.

func (s *Server) WithAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := strings.TrimPrefix(r.Header.Get(AuthMetadataKey), "Bearer ")
		_, err := jwt.VerifyTokenWithExpectations(s.JwtAuth, token, s.jwtExpectations)
		if err != nil {
			// Return a constant message to the (anonymous) caller; the raw jwx error
			// distinguishes expired vs bad-signature vs audience-mismatch and is an
			// oracle for probers. Keep the detail server-side.
			slog.Default().WarnContext(r.Context(), "auth token verification failed", slog.String("err", err.Error()))
			errMsg := errorMessage{Error: "unauthorized"}

			// Set content type to JSON
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)

			// Write the JSON error message
			if err := json.NewEncoder(w).Encode(errMsg); err != nil {
				slog.Default().Error("failed to encode auth error response", slog.String("err", err.Error()))
			}
			return
		}

		next.ServeHTTP(w, r)
	})
}

// GetTokenMetadata returns the token from grpc metadata context.
func GetTokenMetadata(ctx context.Context) (string, error) {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return "", fmt.Errorf("missing metadata in context")
	}
	tokenHeaders := md.Get(AuthMetadataKey)
	if len(tokenHeaders) == 0 {
		return "", fmt.Errorf("missing auth header")
	}
	token := strings.TrimPrefix(tokenHeaders[0], "Bearer ")

	return token, nil
}

type adminContextKey string

const adminUsernameKey adminContextKey = "admin_username"

// GetAdminUsername returns the admin username from context (set by AdminAuthInterceptor).
// Returns empty string if not set (e.g. for non-admin or unauthenticated requests).
func GetAdminUsername(ctx context.Context) string {
	u, _ := ctx.Value(adminUsernameKey).(string)
	return u
}

// PutAdminUsername adds admin username to context.
func PutAdminUsername(ctx context.Context, username string) context.Context {
	return context.WithValue(ctx, adminUsernameKey, username)
}

// AdminAuthz is the authorization resolved from an admin JWT and stashed in
// context by the interceptor for handlers (e.g. GetCurrentAccount) to read.
// Legacy marks a pre-RBAC token, which is treated as full access. Super grants
// full access. Perms is the section→access map for scoped accounts.
type AdminAuthz struct {
	Legacy bool
	Super  bool
	Perms  map[string]entity.AccessLevel
}

// FullAccess reports whether the authorization grants unrestricted access
// (super-admin or a legacy pre-RBAC token).
func (a AdminAuthz) FullAccess() bool { return a.Super || a.Legacy }

const adminAuthzKey adminContextKey = "admin_authz"

// GetAdminAuthz returns the admin authorization from context (set by the
// interceptor). ok is false on the non-admin / unauthenticated path.
func GetAdminAuthz(ctx context.Context) (AdminAuthz, bool) {
	a, ok := ctx.Value(adminAuthzKey).(AdminAuthz)
	return a, ok
}

func putAdminAuthz(ctx context.Context, a AdminAuthz) context.Context {
	return context.WithValue(ctx, adminAuthzKey, a)
}

const adminServicePrefix = rbac.MethodPrefix

// UnaryAdminAuthInterceptor returns an interceptor that authenticates every admin
// RPC via its JWT and authorizes it against the account's embedded permissions.
// Authorization is stateless — it reads only the token's claims (super + perms)
// via rbac.Authorize, which fails closed on any method not explicitly mapped or
// allowlisted. Tokens minted before RBAC carry no permission claims and are
// treated as full access (legacy) so already-issued sessions keep working until
// they expire. The resolved subject and authorization are placed in context.
func (s *Server) UnaryAdminAuthInterceptor() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		if !strings.HasPrefix(info.FullMethod, adminServicePrefix) {
			return handler(ctx, req)
		}
		token, err := GetTokenMetadata(ctx)
		if err != nil {
			return nil, status.Error(codes.Unauthenticated, "unauthorized")
		}
		sub, super, permStrs, legacy, err := jwt.VerifyAdminToken(s.JwtAuth, token, s.jwtExpectations)
		if err != nil {
			// Log the raw verification error server-side; return a constant so the
			// jwx failure stage is not disclosed to the caller.
			slog.Default().WarnContext(ctx, "invalid admin auth token", slog.String("err", err.Error()))
			return nil, status.Error(codes.Unauthenticated, "unauthorized")
		}
		perms := rbac.ParsePermissions(permStrs)
		if !rbac.Authorize(info.FullMethod, legacy, super, perms) {
			slog.Default().WarnContext(ctx, "admin authorization denied",
				slog.String("username", sub),
				slog.String("method", info.FullMethod),
			)
			return nil, status.Errorf(codes.PermissionDenied, "insufficient permissions for %s", info.FullMethod)
		}
		if sub != "" {
			ctx = PutAdminUsername(ctx, sub)
		}
		ctx = putAdminAuthz(ctx, AdminAuthz{Legacy: legacy, Super: super, Perms: perms})
		return handler(ctx, req)
	}
}
