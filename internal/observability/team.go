package observability

import (
	"context"
	"strings"

	"github.com/getsentry/sentry-go"
	"google.golang.org/grpc"

	"github.com/jekabolt/grbpwr-manager/internal/tenant"
)

// Tags every event carries so one error answers «only this team, or everyone?» and «which install?».
const (
	// TagTeam is the team's address word (slug, e.g. "grbpwr"), never its numeric id.
	TagTeam = "team"
	// TagInstall is the install: "beta", "prod", or the raw SENTRY_ENVIRONMENT when it says neither.
	TagInstall = "install"
)

// Stable messages other parts of the backend log at Error so a Sentry alert rule can match them. The
// text must never change and never carry dynamic parts (those go into attrs): the handler groups one
// Sentry issue per message, so the alert fires once per kind, not once per occurrence.
const (
	// MsgTeamStuckAfterMigration: a team's database did not reach the binary's schema after a deploy
	// (attr "team" = slug).
	MsgTeamStuckAfterMigration = "team stuck after migration"
	// MsgSignupMailBreaker: sign-up letters per hour went over the global limit; new sign-up letters
	// are held.
	MsgSignupMailBreaker = "sign-up letter breaker tripped"
)

// InstallOf reduces a Sentry environment to the install word: anything naming prod is "prod",
// anything naming beta is "beta" ("lekalo-beta" → "beta"), otherwise the environment itself.
func InstallOf(env string) string {
	e := strings.ToLower(strings.TrimSpace(env))
	switch {
	case strings.Contains(e, "prod"):
		return "prod"
	case strings.Contains(e, "beta"):
		return "beta"
	case e == "":
		return DefaultEnvironment
	}
	return e
}

// teamAttrKeys are the slog attr keys a team slug travels under when the ctx carries no team (a
// worker logging about a team from outside it, e.g. tenant.Each logs "slug").
var teamAttrKeys = []string{"team", "team_slug", "slug"}

// teamOf is the slug of the team an event belongs to: the ctx team first, then a slog attr.
func teamOf(ctx context.Context, extra map[string]any) string {
	if t, ok := tenant.From(ctx); ok && t.Slug != "" {
		return t.Slug
	}
	for _, k := range teamAttrKeys {
		if v, ok := extra[k].(string); ok && v != "" && tenant.ValidSlug(v) == nil {
			return v
		}
	}
	return ""
}

// tagTeam stamps the team of ctx on the call's hub. The hub is the one the outer interceptor put
// in the context, shared by pointer, so the recovery handler outside the team binder sees the tag
// too (a panic is reported with its team).
func tagTeam(ctx context.Context) {
	t, ok := tenant.From(ctx)
	if !ok || t.Slug == "" {
		return
	}
	if hub := sentry.GetHubFromContext(ctx); hub != nil {
		hub.Scope().SetTag(TagTeam, t.Slug)
	}
}

// TeamUnaryInterceptor tags the call's hub with its team. It must sit right AFTER the team binder
// (the ctx has the team) and inside UnaryServerInterceptor (the ctx has a per-call hub). A
// pass-through when disabled.
func TeamUnaryInterceptor() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		if Enabled() {
			tagTeam(ctx)
		}
		return handler(ctx, req)
	}
}

// TeamStreamInterceptor is the streaming twin of TeamUnaryInterceptor.
func TeamStreamInterceptor() grpc.StreamServerInterceptor {
	return func(srv any, ss grpc.ServerStream, _ *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		if Enabled() {
			tagTeam(ss.Context())
		}
		return handler(srv, ss)
	}
}
