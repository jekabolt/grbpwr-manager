package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"log/slog"

	"github.com/go-chi/jwtauth/v5"
	"github.com/jekabolt/grbpwr-manager/internal/auth/jwt"
	"github.com/jekabolt/grbpwr-manager/internal/auth/pwhash"
	"github.com/jekabolt/grbpwr-manager/internal/dependency"
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

// Server implements the heartbeat service.
type Server struct {
	auth.UnimplementedAuthServiceServer
	adminRepository dependency.Admin
	pwhash          *pwhash.PasswordHasher
	JwtAuth         *jwtauth.JWTAuth
	jwtTTL          time.Duration
	c               *Config
	masterHash      string
}

// Config contains the configuration for the auth server.
type Config struct {
	JWTSecret                string `mapstructure:"jwt_secret"`
	MasterPassword           string `mapstructure:"master_password"`
	PasswordHasherSaltSize   int    `mapstructure:"password_hasher_salt_size"`
	PasswordHasherIterations int    `mapstructure:"password_hasher_iterations"`
	JWTTTL                   string `mapstructure:"jwt_ttl"`
}

// New creates a new auth server.
func New(c *Config, ar dependency.Admin) (*Server, error) {

	ph, err := pwhash.New(c.PasswordHasherSaltSize, c.PasswordHasherIterations)
	if err != nil {
		return nil, fmt.Errorf("failed to create password hasher: %w", err)
	}
	hash, err := ph.HashPassword(c.MasterPassword)
	if err != nil {
		return nil, fmt.Errorf("failed to hash master password: %w", err)
	}

	if err := ph.Validate(c.MasterPassword, hash); err != nil {
		return nil, fmt.Errorf("failed to validate master password: %w", err)
	}

	ttl, err := time.ParseDuration(c.JWTTTL)
	if err != nil {
		return nil, fmt.Errorf("failed to parse jwt ttl: %w", err)
	}
	s := &Server{
		adminRepository: ar,
		pwhash:          ph,
		JwtAuth:         jwtauth.New("HS256", []byte(c.JWTSecret), nil),
		c:               c,
		jwtTTL:          ttl,
		masterHash:      hash,
	}

	return s, nil
}

// Login get auth token for provided username and password.
func (s *Server) Login(ctx context.Context, req *auth.LoginRequest) (*auth.LoginResponse, error) {
	username := strings.ToLower(req.Username)

	pwHash, err := s.adminRepository.PasswordHashByUsername(ctx, username)
	if err != nil {
		slog.Default().ErrorContext(ctx, "failed to get password hash by username",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Unauthenticated, "not authenticated")
	}

	err = s.pwhash.Validate(req.Password, pwHash)
	if err != nil {
		slog.Default().ErrorContext(ctx, "failed to validate password",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Unauthenticated, "not authenticated")
	}

	token, err := jwt.NewTokenWithSubject(s.JwtAuth, s.jwtTTL, username)
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

// Create creates a new user requires an admin password.
func (s *Server) Create(ctx context.Context, req *auth.CreateRequest) (*auth.CreateResponse, error) {

	err := s.pwhash.Validate(s.c.MasterPassword, s.masterHash)
	if err != nil {
		slog.Default().ErrorContext(ctx, "failed to validate master password",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Unauthenticated, "not authenticated")
	}

	username := strings.ToLower(req.User.Username)

	pwHash, err := s.pwhash.HashPassword(req.User.Password)
	if err != nil {
		slog.Default().ErrorContext(ctx, "failed to hash password",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Unauthenticated, "not authenticated")
	}

	token, err := jwt.NewTokenWithSubject(s.JwtAuth, s.jwtTTL, username)
	if err != nil {
		slog.Default().ErrorContext(ctx, "failed to create jwt token",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Unauthenticated, "not authenticated")
	}

	err = s.adminRepository.AddAdmin(ctx, username, pwHash)
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
	err := s.pwhash.Validate(req.MasterPassword, s.masterHash)
	if err != nil {
		slog.Default().ErrorContext(ctx, "failed to validate master password",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Unauthenticated, "not authenticated")
	}
	username := strings.ToLower(req.Username)
	err = s.adminRepository.DeleteAdmin(ctx, username)
	if err != nil {
		return nil, err
	}
	return &auth.DeleteResponse{}, nil
}

// ChangePassword changes the password of the user. It requires the old password or admin password provided.
func (s *Server) ChangePassword(ctx context.Context, req *auth.ChangePasswordRequest) (*auth.ChangePasswordResponse, error) {
	username := strings.ToLower(req.Username)

	currentPwdHash, err := s.adminRepository.PasswordHashByUsername(ctx, username)
	if err != nil {
		slog.Default().ErrorContext(ctx, "failed to get password hash by username",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Unauthenticated, "not authenticated")
	}

	err = s.pwhash.Validate(req.CurrentPassword, s.masterHash)
	if err != nil {
		err = s.pwhash.Validate(req.CurrentPassword, currentPwdHash)
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

	token, err := jwt.NewToken(s.JwtAuth, s.jwtTTL)
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
		_, err := jwt.VerifyToken(s.JwtAuth, token)
		if err != nil {
			// Create a new error message
			errMsg := errorMessage{Error: err.Error()}

			// Set content type to JSON
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)

			// Write the JSON error message
			json.NewEncoder(w).Encode(errMsg)
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

const adminServicePrefix = "/admin.AdminService/"

// UnaryAdminAuthInterceptor returns an interceptor that extracts the JWT subject (admin username)
// from admin RPCs and puts it in context for downstream use (e.g. stock change history).
func (s *Server) UnaryAdminAuthInterceptor() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		if !strings.HasPrefix(info.FullMethod, adminServicePrefix) {
			return handler(ctx, req)
		}
		token, err := GetTokenMetadata(ctx)
		if err != nil {
			return handler(ctx, req) // no token, continue; auth will fail elsewhere if required
		}
		sub, err := jwt.VerifyToken(s.JwtAuth, token)
		if err != nil {
			return handler(ctx, req)
		}
		if sub != "" {
			ctx = PutAdminUsername(ctx, sub)
		}
		return handler(ctx, req)
	}
}
