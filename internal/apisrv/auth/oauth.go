package auth

import (
	"net/http"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/platformauth/oauthflow"
	"github.com/jekabolt/grbpwr-manager/internal/tenant"
)

// Provider sign-in routes (teams O §3, lane O3): the registry is built in SetPlatform from the
// OAUTH_* settings, the handler is mounted by the HTTP server at /api/auth/oauth.

// setupOAuth builds the provider registry and the provider routes' handler (SetPlatform, teams
// mode on). A settings error is kept for OAuthConfigErr (the boot refuses it); no handler then.
func (s *Server) setupOAuth() {
	s.platform.oauthReg, s.platform.oauthErr = oauthflow.NewRegistry(s.platform.cfg)
}

// buildOAuthHandler wires the handler once the identity service is built (nil service: every
// provider route answers 404).
func (s *Server) buildOAuthHandler() {
	if s.platform.oauthErr != nil {
		return
	}
	d := oauthflow.Deps{
		Origins:    edgeOrigins{s: s},
		Providers:  s.platform.oauthReg,
		BaseDomain: s.platform.cfg.BaseDomain,
	}
	if s.platform.svc != nil {
		d.Flow = s.platform.svc
	}
	s.platform.oauth = oauthflow.New(d)
}

// OAuthHandler is the provider routes' handler (POST /api/auth/oauth/{provider}/start, GET
// …/google/callback, POST …/apple/callback). nil with teams mode off or unusable OAUTH_* settings.
func (s *Server) OAuthHandler() http.Handler {
	if s.platform.oauth == nil {
		return nil
	}
	return s.platform.oauth
}

// OAuthConfigErr is why the OAUTH_* settings refuse the boot (it names the variable, never its
// value). nil with teams mode off or usable settings (none set included).
func (s *Server) OAuthConfigErr() error { return s.platform.oauthErr }

func (s *Server) closeOAuth() {
	if s.platform.oauth != nil {
		s.platform.oauth.Close()
	}
}

// edgeOrigins are the binder's origin rules, read when a start arrives (SetEdge runs after
// SetPlatform). No edge, or an edge without the root rule: every origin is refused.
type edgeOrigins struct{ s *Server }

func (o edgeOrigins) RootOriginAllowed(origin string) bool {
	r, ok := o.s.edge.(interface{ RootOriginAllowed(string) bool })
	return ok && r.RootOriginAllowed(origin)
}

func (o edgeOrigins) OriginAllowed(origin string, t tenant.Team, legacy bool) bool {
	return o.s.edge != nil && strings.TrimSpace(origin) != "" && o.s.edge.OriginAllowed(origin, t, legacy)
}
