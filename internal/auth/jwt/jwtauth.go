package jwt

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/go-chi/jwtauth/v5"
	"github.com/google/uuid"
	"github.com/lestrrat-go/jwx/jwt"
)

var ErrInvalidClaims = errors.New("token is unauthorized")

// MinSymmetricSecretBytes is the minimum length for symmetric secrets and HMAC
// peppers. HS256 token signatures and the HMAC peppers are only as strong as
// their key, so require ~256 bits of key material.
const MinSymmetricSecretBytes = 32

// RequireStrongSecret warns when a symmetric secret/pepper is shorter than a safe
// HS256 / HMAC key length. It logs loudly rather than failing closed: a live deploy
// may carry a legacy weak secret, and refusing to boot would force either an outage
// or an unsafe rotation — notably the storefront login pepper is mixed into every
// stored password hash and cannot be rotated without locking out all customers. The
// warning names the offending setting so it can be rotated deliberately, out of band.
// It always returns nil; the error return is retained for call-site compatibility.
func RequireStrongSecret(name, v string) error {
	if len(v) < MinSymmetricSecretBytes {
		slog.Warn("weak symmetric secret — rotate to at least the recommended length",
			"name", name, "got_bytes", len(v), "min_bytes", MinSymmetricSecretBytes)
	}
	return nil
}

// Expectations holds optional issuer/audience checks. Empty strings mean skip.
type Expectations struct {
	Issuer   string
	Audience string
}

func VerifyToken(jwtAuth *jwtauth.JWTAuth, token string) (string, error) {
	return VerifyTokenWithExpectations(jwtAuth, token, nil)
}

// VerifyTokenWithExpectations verifies the JWT and optionally checks iss/aud.
// When exp is nil or both fields empty, only signature and exp are validated.
func VerifyTokenWithExpectations(jwtAuth *jwtauth.JWTAuth, tokenString string, exp *Expectations) (string, error) {
	sub, _, _, err := VerifyTokenFull(jwtAuth, tokenString, exp)
	return sub, err
}

// VerifyTokenFull verifies the JWT, optionally checks iss/aud, and returns subject, jti, and expiration.
// Jti is empty when the token has no jti claim. Exp is zero when not set.
func VerifyTokenFull(jwtAuth *jwtauth.JWTAuth, tokenString string, exp *Expectations) (sub, jti string, expAt time.Time, err error) {
	t, err := jwtauth.VerifyToken(jwtAuth, tokenString)
	if err != nil {
		return "", "", time.Time{}, err
	}
	if exp != nil && (exp.Issuer != "" || exp.Audience != "") {
		if err := checkExpectations(t, exp); err != nil {
			return "", "", time.Time{}, err
		}
	}
	sub = t.Subject()
	if m, e := t.AsMap(context.Background()); e == nil {
		if j, ok := m["jti"].(string); ok && j != "" {
			jti = j
		}
	}
	expAt = t.Expiration()
	return sub, jti, expAt, nil
}

func checkExpectations(t jwt.Token, exp *Expectations) error {
	if exp.Issuer != "" {
		if t.Issuer() != exp.Issuer {
			return ErrInvalidClaims
		}
	}
	if exp.Audience != "" {
		aud := t.Audience()
		found := false
		for _, a := range aud {
			if a == exp.Audience {
				found = true
				break
			}
		}
		if !found {
			return ErrInvalidClaims
		}
	}
	return nil
}

// IssueOpts holds optional issuer/audience/jti for minting.
type IssueOpts struct {
	Issuer    string
	Audience  string
	IncludeJti bool
}

func NewToken(jwtAuth *jwtauth.JWTAuth, ttl time.Duration) (string, error) {
	return NewTokenWithSubjectOpts(jwtAuth, ttl, "", nil)
}

// NewTokenWithSubject creates a JWT with optional subject (username) claim.
// Subject is used for admin audit trails.
func NewTokenWithSubject(jwtAuth *jwtauth.JWTAuth, ttl time.Duration, subject string) (string, error) {
	return NewTokenWithSubjectOpts(jwtAuth, ttl, subject, nil)
}

// NewTokenWithSubjectOpts creates a JWT with subject and optional iss/aud.
// Always sets iat and exp. When opts is nil or both fields empty, only sub/exp/iat are set.
func NewTokenWithSubjectOpts(jwtAuth *jwtauth.JWTAuth, ttl time.Duration, subject string, opts *IssueOpts) (string, error) {
	return NewTokenWithSubjectOptsAt(jwtAuth, ttl, subject, opts, time.Now().UTC())
}

// NewTokenWithSubjectOptsAt is like NewTokenWithSubjectOpts but accepts explicit now for consistent expiration.
// Use when the caller needs to return the same expiration time (e.g. AccessExpiresAt).
func NewTokenWithSubjectOptsAt(jwtAuth *jwtauth.JWTAuth, ttl time.Duration, subject string, opts *IssueOpts, now time.Time) (string, error) {
	claims := map[string]interface{}{
		"iat": now.Unix(),
		"exp": now.Add(ttl).Unix(),
	}
	if subject != "" {
		claims["sub"] = subject
	}
	if opts != nil {
		if opts.Issuer != "" {
			claims["iss"] = opts.Issuer
		}
		if opts.Audience != "" {
			claims["aud"] = opts.Audience
		}
		if opts.IncludeJti {
			claims["jti"] = uuid.New().String()
		}
	}
	_, ts, err := jwtAuth.Encode(claims)
	if err != nil {
		return "", err
	}
	return ts, nil
}
