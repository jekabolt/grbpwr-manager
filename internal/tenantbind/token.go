package tenantbind

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/tenant"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// TokenTeams verifies a public link token (/api/p|pv|rp|f/{token}) and returns the team it carries
// signed: team #1 for a v1 token. ok=false = not a valid token. patterntoken.Minter.TokenTeam.
type TokenTeams func(token string) (teamID int64, ok bool)

// errTokenGone: the token's team cannot serve links (unknown, deleted, not yet live). The edge
// answers the same bare 404 as a bad or revoked token, so a signed-out probe learns nothing.
var errTokenGone = errors.New("tenantbind: token team gone")

// UseTokenTeams installs the verifier the edge binds public link requests with. nil keeps every
// link on team #1 (the pre-v2 behaviour).
func (b *Binder) UseTokenTeams(v TokenTeams) {
	b.mu.Lock()
	b.tokenTeams = v
	b.mu.Unlock()
}

// tokenOfPath is the {token} segment of /api/<kind>/{token}; "" when the path has another shape
// (chi answers 404 to it anyway).
func tokenOfPath(path string) string {
	rest, ok := strings.CutPrefix(path, "/api/")
	if !ok {
		return ""
	}
	_, tok, ok := strings.Cut(rest, "/")
	if !ok || tok == "" || strings.Contains(tok, "/") {
		return ""
	}
	return tok
}

// bindToken binds a public link request to the team its token was minted in.
//
// The team comes from the token alone — never from the Host or a header: the link is opened signed
// out, on the one API address, by people outside the team. The signature covers the team, so a
// token re-labelled to another team does not verify and stays on team #1, where the handler's own
// check (token team == bound team) answers 404. A v1 token is team #1, forever.
//
// A team that is active or suspended (paused = read-only) serves its links; deleted, pending,
// review or provisioning do not — the same 404, whether the team exists or not. A team whose
// database is behind the binary answers 503 like every other call of that team (only a holder of a
// validly signed link of that team can see it).
func (b *Binder) bindToken(ctx context.Context, path string) (context.Context, error) {
	b.mu.RLock()
	verify := b.tokenTeams
	b.mu.RUnlock()
	if verify == nil || !b.TeamsMode() {
		return tenant.With(ctx, b.teamOne), nil
	}
	teamID, ok := verify(tokenOfPath(path))
	if !ok || teamID == tenant.TeamOneID {
		return tenant.With(ctx, b.teamOne), nil
	}
	t, err := b.TeamByID(ctx, teamID)
	if err != nil {
		if errors.Is(err, tenant.ErrNotFound) {
			return nil, errTokenGone
		}
		return nil, status.Error(codes.Unavailable, "team lookup unavailable")
	}
	if t.Status != tenant.StatusActive && t.Status != tenant.StatusSuspended {
		return nil, errTokenGone
	}
	if b.SchemaBehind(t) {
		return nil, status.Error(codes.Unavailable, "team is being updated")
	}
	return tenant.With(ctx, t), nil
}

// serveToken is Middleware for the token class: a refusal is the handlers' uniform bare 404, or a
// bare 503 when the control plane or the team's database is not ready.
func (b *Binder) serveToken(w http.ResponseWriter, r *http.Request, next http.Handler) {
	bound, err := b.bindToken(r.Context(), r.URL.Path)
	if err != nil {
		w.Header().Set("Cache-Control", "private, no-store")
		if status.Code(err) == codes.Unavailable {
			http.Error(w, http.StatusText(http.StatusServiceUnavailable), http.StatusServiceUnavailable)
			return
		}
		http.NotFound(w, r)
		return
	}
	next.ServeHTTP(w, r.WithContext(bound))
}
