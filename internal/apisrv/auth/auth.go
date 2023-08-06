package auth

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/jwtauth/v5"
	"github.com/jekabolt/grbpwr-manager/internal/auth/jwt"
	"github.com/jekabolt/grbpwr-manager/internal/auth/pwhash"
	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/proto/gen/auth"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
)

const (
	// AuthMetadataKey is header key to match auth token
	AuthMetadataKey = "Grpc-Metadata-Authorization"
)

// Server implements the heartbeat service.
type Server struct {
	auth.UnimplementedAuthServer
	adminRepository dependency.Admin
	pwhash          *pwhash.PasswordHasher
	JwtAuth         *jwtauth.JWTAuth
	jwtTTL          time.Duration
	c               *Config
	masterHash      string
}

// Config contains the configuration for the auth server.
type Config struct {
	JWTSecret                string `mapstructure:"jwtSecret"`
	MasterPassword           string `mapstructure:"masterPassword"`
	PasswordHasherSaltSize   int    `mapstructure:"passwordHasherSaltSize"`
	PasswordHasherIterations int    `mapstructure:"passwordHasherIterations"`
	JWTTTL                   string `mapstructure:"jwtttl"`
}

// New creates a new auth server.
func New(c *Config, ar dependency.Admin) (*Server, error) {

	ph, err := pwhash.New(c.PasswordHasherSaltSize, c.PasswordHasherIterations)
	if err != nil {
		return nil, err
	}
	hash, err := ph.HashPassword(c.MasterPassword)
	if err != nil {
		return nil, err
	}

	if err := ph.Validate(c.MasterPassword, hash); err != nil {
		return nil, err
	}

	ttl, err := time.ParseDuration(c.JWTTTL)
	if err != nil {
		return nil, err
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
		return nil, err
	}

	err = s.pwhash.Validate(req.Password, pwHash)
	if err != nil {
		return nil, status.Errorf(codes.Unauthenticated, "not authenticated %v", err.Error())
	}

	token, err := jwt.NewToken(s.JwtAuth, s.jwtTTL)
	if err != nil {
		return nil, err
	}

	return &auth.LoginResponse{
		AuthToken: token,
	}, nil
}

// Create creates a new user requires an admin password.
func (s *Server) Create(ctx context.Context, req *auth.CreateUserRequest) (*auth.CreateUserResponse, error) {

	err := s.pwhash.Validate(s.c.MasterPassword, s.masterHash)
	if err != nil {
		return nil, status.Error(codes.Unauthenticated, "not authenticated")
	}

	username := strings.ToLower(req.User.Username)

	pwHash, err := s.pwhash.HashPassword(req.User.Password)
	if err != nil {
		return nil, err
	}

	token, err := jwt.NewToken(s.JwtAuth, s.jwtTTL)
	if err != nil {
		return nil, err
	}

	err = s.adminRepository.AddAdmin(ctx, username, pwHash)
	if err != nil {
		return nil, err
	}
	return &auth.CreateUserResponse{
		AuthToken: token,
	}, nil

}

// Delete deletes a user.
func (s *Server) Delete(ctx context.Context, req *auth.DeleteUserRequest) (*emptypb.Empty, error) {
	err := s.pwhash.Validate(req.MasterPassword, s.masterHash)
	if err != nil {
		return nil, status.Error(codes.Unauthenticated, "not authenticated")
	}
	username := strings.ToLower(req.Username)
	err = s.adminRepository.DeleteAdmin(ctx, username)
	if err != nil {
		return nil, err
	}
	return &emptypb.Empty{}, nil
}

// ChangePassword changes the password of the user. It requires the old password or admin password provided.
func (s *Server) ChangePassword(ctx context.Context, req *auth.ChangePasswordRequest) (*auth.ChangePasswordResponse, error) {
	username := strings.ToLower(req.Username)

	currentPwdHash, err := s.adminRepository.PasswordHashByUsername(ctx, username)
	if err != nil {
		return nil, fmt.Errorf("cannot get a password %v", err.Error())
	}

	err = s.pwhash.Validate(req.CurrentPassword, s.masterHash)
	if err != nil {
		err = s.pwhash.Validate(req.CurrentPassword, currentPwdHash)
		if err != nil {
			return nil, fmt.Errorf("neither master and provided passwords didn't pass %v", err.Error())
		}
	}

	pwHashNew, err := s.pwhash.HashPassword(req.NewPassword)
	if err != nil {
		return nil, err
	}

	token, err := jwt.NewToken(s.JwtAuth, s.jwtTTL)
	if err != nil {
		return nil, err
	}

	err = s.adminRepository.ChangePassword(ctx, username, pwHashNew)
	if err != nil {
		return nil, err
	}
	return &auth.ChangePasswordResponse{
		AuthToken: token,
	}, nil
}

// WithAuth middleware checks if the user is authenticated.
func (s *Server) WithAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := strings.TrimPrefix(r.Header.Get(AuthMetadataKey), "Bearer ")
		_, err := jwt.VerifyToken(s.JwtAuth, token)
		if err != nil {
			http.Error(w, fmt.Sprintf("invalid token %v", err.Error()), http.StatusUnauthorized)
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
