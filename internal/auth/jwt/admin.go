package jwt

import (
	"context"
	"time"

	"github.com/go-chi/jwtauth/v5"
	"github.com/google/uuid"
)

// Admin authorization claim keys. The presence of claimSuper marks a token as
// RBAC-aware ("new style"). Tokens minted before RBAC carry neither claim; such
// tokens are reported as legacy by VerifyAdminToken and callers treat them as
// full access so the rollout does not lock out already-issued sessions.
const (
	claimSuper = "super"
	claimPerms = "perms"
)

// NewAdminToken mints an admin JWT carrying the account's authorization: a super
// flag and, for non-super accounts, a list of "section:access" permission
// strings (e.g. "orders:write"). super and perms are always set (perms may be an
// empty array for a no-access account) so the token is distinguishable from a
// legacy pre-RBAC token. opts carries optional iss/aud/jti (sub/iat/exp are
// always set).
func NewAdminToken(jwtAuth *jwtauth.JWTAuth, ttl time.Duration, subject string, super bool, perms []string, opts *IssueOpts) (string, error) {
	return NewAdminTokenAt(jwtAuth, ttl, subject, super, perms, opts, time.Now().UTC())
}

// NewAdminTokenAt is like NewAdminToken but accepts an explicit now for
// deterministic expiration (used in tests and where the caller must echo exp).
func NewAdminTokenAt(jwtAuth *jwtauth.JWTAuth, ttl time.Duration, subject string, super bool, perms []string, opts *IssueOpts, now time.Time) (string, error) {
	if perms == nil {
		perms = []string{}
	}
	claims := map[string]any{
		"iat":      now.Unix(),
		"exp":      now.Add(ttl).Unix(),
		claimSuper: super,
		claimPerms: perms,
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

// VerifyAdminToken verifies an admin JWT and extracts its authorization claims.
//
//   - legacy=true  → the token predates RBAC (no super claim); super/perms are
//     zero and the caller should treat the account as full access.
//   - legacy=false → super and perms reflect the embedded claims. perms holds the
//     raw "section:access" strings; callers parse them.
func VerifyAdminToken(jwtAuth *jwtauth.JWTAuth, tokenString string, exp *Expectations) (sub string, super bool, perms []string, legacy bool, err error) {
	t, err := jwtauth.VerifyToken(jwtAuth, tokenString)
	if err != nil {
		return "", false, nil, false, err
	}
	if exp != nil && (exp.Issuer != "" || exp.Audience != "") {
		if err := checkExpectations(t, exp); err != nil {
			return "", false, nil, false, err
		}
	}
	sub = t.Subject()
	m, err := t.AsMap(context.Background())
	if err != nil {
		return "", false, nil, false, err
	}
	rawSuper, hasSuper := m[claimSuper]
	if !hasSuper {
		// Pre-RBAC token: no authorization claims embedded.
		return sub, false, nil, true, nil
	}
	if b, ok := rawSuper.(bool); ok {
		super = b
	}
	if raw, ok := m[claimPerms].([]any); ok {
		perms = make([]string, 0, len(raw))
		for _, v := range raw {
			if s, ok := v.(string); ok && s != "" {
				perms = append(perms, s)
			}
		}
	}
	return sub, super, perms, false, nil
}
