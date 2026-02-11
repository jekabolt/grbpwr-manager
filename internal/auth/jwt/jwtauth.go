package jwt

import (
	"time"

	"github.com/go-chi/jwtauth/v5"
)

func VerifyToken(jwtAuth *jwtauth.JWTAuth, token string) (string, error) {
	t, err := jwtauth.VerifyToken(jwtAuth, token)
	if err != nil {
		return "", err
	}
	return t.Subject(), nil
}

func NewToken(jwtAuth *jwtauth.JWTAuth, ttl time.Duration) (string, error) {
	return NewTokenWithSubject(jwtAuth, ttl, "")
}

// NewTokenWithSubject creates a JWT with optional subject (username) claim.
// Subject is used for admin audit trails.
func NewTokenWithSubject(jwtAuth *jwtauth.JWTAuth, ttl time.Duration, subject string) (string, error) {
	claims := map[string]interface{}{
		"exp": time.Now().Add(ttl).Unix(),
	}
	if subject != "" {
		claims["sub"] = subject
	}
	_, ts, err := jwtAuth.Encode(claims)
	if err != nil {
		return ts, err
	}
	return ts, nil
}
