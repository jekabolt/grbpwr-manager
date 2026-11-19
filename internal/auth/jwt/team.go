package jwt

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/go-chi/jwtauth/v5"
)

// Audiences of platform tokens (02-DESIGN §4): a team host, the console, the root host.
const (
	AudTeam    = "team"
	AudConsole = "console"
	AudRoot    = "root"
)

const (
	claimUserID    = "uid"
	claimTeamID    = "tid"
	claimSessionID = "sid"
)

// TeamClaims are the claims of a platform token.
//
// Legacy marks a token minted before teams (today's admin token: sub + super + perms, maybe the
// configured iss/aud, no uid, no platform audience). Callers map a legacy token to team #1; its
// TeamID stays 0 here. A legacy token without the super claim carries no access at all (the
// pre-RBAC "no claim = full access" rule is gone).
type TeamClaims struct {
	Handle    string
	UserID    int64
	TeamID    int64
	SessionID string
	Audience  string
	Super     bool
	Perms     []string
	Legacy    bool
	// IssuedAt is the token's iat (zero when absent). The edge refuses a legacy token issued
	// before the holder's last password change.
	IssuedAt time.Time
}

var errTeamClaims = errors.New("jwt: bad platform token claims")

func isPlatformAud(a string) bool { return a == AudTeam || a == AudConsole || a == AudRoot }

// NewTeamTokenAt mints a platform token: sub, uid, tid (team audience only), sid, aud, super,
// perms, iat, exp. It carries no iss: the old verifier with an issuer expectation refuses it, so a
// platform token never passes as a legacy admin token.
func NewTeamTokenAt(a *jwtauth.JWTAuth, ttl time.Duration, c TeamClaims, now time.Time) (string, error) {
	if !isPlatformAud(c.Audience) {
		return "", fmt.Errorf("%w: audience %q", errTeamClaims, c.Audience)
	}
	if c.UserID <= 0 || c.SessionID == "" || c.Handle == "" {
		return "", fmt.Errorf("%w: user, session and handle are required", errTeamClaims)
	}
	if (c.Audience == AudTeam) != (c.TeamID > 0) {
		return "", fmt.Errorf("%w: a team token needs a team, other audiences none", errTeamClaims)
	}
	if ttl <= 0 {
		return "", fmt.Errorf("%w: ttl must be positive", errTeamClaims)
	}
	perms := c.Perms
	if perms == nil {
		perms = []string{}
	}
	claims := map[string]any{
		"sub":          c.Handle,
		"aud":          c.Audience,
		"iat":          now.Unix(),
		"exp":          now.Add(ttl).Unix(),
		claimUserID:    c.UserID,
		claimSessionID: c.SessionID,
		claimSuper:     c.Super,
		claimPerms:     perms,
	}
	if c.TeamID > 0 {
		claims[claimTeamID] = c.TeamID
	}
	_, ts, err := a.Encode(claims)
	if err != nil {
		return "", err
	}
	return ts, nil
}

// VerifyTeamToken verifies signature and expiry and reads the claims.
//
// A platform token (uid or a platform audience present) must be whole: one platform audience, exp
// and iat with exp after iat, a positive uid, a sid, a positive tid exactly when the audience is team. When exp.Audience is a
// platform audience the token's must equal it. exp.Issuer and a non-platform exp.Audience are the
// legacy admin token's expectations and apply to legacy tokens only.
//
// A legacy token (no uid, no platform audience) comes back with Legacy=true and TeamID=0.
func VerifyTeamToken(a *jwtauth.JWTAuth, token string, exp *Expectations) (TeamClaims, error) {
	t, err := jwtauth.VerifyToken(a, token)
	if err != nil {
		return TeamClaims{}, err
	}
	m, err := t.AsMap(context.Background())
	if err != nil {
		return TeamClaims{}, err
	}
	auds := t.Audience()
	platformAud := ""
	for _, x := range auds {
		if isPlatformAud(x) {
			platformAud = x
		}
	}
	_, hasUID := m[claimUserID]
	_, hasTID := m[claimTeamID]

	c := TeamClaims{Handle: t.Subject(), IssuedAt: t.IssuedAt()}
	c.Super, c.Perms = authzClaims(m)

	if !hasUID && platformAud == "" {
		if hasTID {
			return TeamClaims{}, fmt.Errorf("%w: team without user", errTeamClaims)
		}
		if exp != nil && (exp.Issuer != "" || (exp.Audience != "" && !isPlatformAud(exp.Audience))) {
			legacyExp := *exp
			if isPlatformAud(legacyExp.Audience) {
				legacyExp.Audience = ""
			}
			if err := checkExpectations(t, &legacyExp); err != nil {
				return TeamClaims{}, err
			}
		}
		if exp != nil && isPlatformAud(exp.Audience) && exp.Audience != AudTeam {
			// A console or root endpoint never takes a legacy (team #1 admin) token.
			return TeamClaims{}, ErrInvalidClaims
		}
		c.Legacy = true
		return c, nil
	}

	if len(auds) != 1 || platformAud == "" {
		return TeamClaims{}, fmt.Errorf("%w: exactly one platform audience", errTeamClaims)
	}
	c.Audience = platformAud
	if exp != nil && isPlatformAud(exp.Audience) && exp.Audience != c.Audience {
		return TeamClaims{}, ErrInvalidClaims
	}
	// jwtauth checks exp only when present: a platform token must carry both, exp after iat.
	if iat, ex := t.IssuedAt(), t.Expiration(); iat.IsZero() || ex.IsZero() || !ex.After(iat) {
		return TeamClaims{}, fmt.Errorf("%w: exp and iat required, exp after iat", errTeamClaims)
	}
	if c.UserID, err = int64Claim(m[claimUserID]); err != nil || c.UserID <= 0 {
		return TeamClaims{}, fmt.Errorf("%w: uid", errTeamClaims)
	}
	if hasTID {
		if c.TeamID, err = int64Claim(m[claimTeamID]); err != nil || c.TeamID <= 0 {
			return TeamClaims{}, fmt.Errorf("%w: tid", errTeamClaims)
		}
	}
	if (c.Audience == AudTeam) != (c.TeamID > 0) {
		return TeamClaims{}, fmt.Errorf("%w: team audience and tid disagree", errTeamClaims)
	}
	sid, _ := m[claimSessionID].(string)
	if sid == "" {
		return TeamClaims{}, fmt.Errorf("%w: sid", errTeamClaims)
	}
	c.SessionID = sid
	if c.Handle == "" {
		return TeamClaims{}, fmt.Errorf("%w: sub", errTeamClaims)
	}
	return c, nil
}

func authzClaims(m map[string]any) (super bool, perms []string) {
	if b, ok := m[claimSuper].(bool); ok {
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
	return super, perms
}

func int64Claim(v any) (int64, error) {
	switch n := v.(type) {
	case float64:
		if n != math.Trunc(n) || n > math.MaxInt64 || n < math.MinInt64 {
			return 0, errTeamClaims
		}
		return int64(n), nil
	case int64:
		return n, nil
	case int:
		return int64(n), nil
	case json.Number:
		return n.Int64()
	default:
		return 0, errTeamClaims
	}
}
