package tenantbind

import "strings"

// Class is how a request finds its team (teams §6, G5). Every gRPC method and every chi route has
// exactly one.
type Class string

const (
	// ClassUnknown: not classified — refused in teams mode, a G5 test failure.
	ClassUnknown Class = ""
	// ClassClaim: the tid claim of the admin token (AdminService, /api/admin, admin uploads,
	// /statusz); bound by the admin chain in internal/apisrv/auth.
	ClassClaim Class = "claim"
	// ClassXTeam: the new AuthService RPCs — control plane; the handler reads x-team (B3).
	ClassXTeam Class = "x-team"
	// ClassLegacyAuth: the old AuthService RPCs (Login, Create, Delete, ChangePassword) — team #1,
	// refused when x-team names another team.
	ClassLegacyAuth Class = "legacy-auth"
	// ClassStorefront: FrontendService, /api/frontend, the webhooks — TENANT_STOREFRONT_TEAM_ID.
	ClassStorefront Class = "storefront"
	// ClassToken: public token reads /api/p|pv|rp|f/{token} — team #1 for v1 tokens (B6 signs v2).
	ClassToken Class = "token"
	// ClassPlatform: PlatformService and its gateway /api/platform — control plane only,
	// aud=console, no team in ctx.
	ClassPlatform Class = "platform"
	// ClassHealth: probes, swagger and static files — team #1 (the boot team) in both modes.
	ClassHealth Class = "health"
)

// Service prefixes of the registered gRPC services.
const (
	AdminServicePrefix    = "/admin.AdminService/"
	PlatformServicePrefix = "/admin.PlatformService/"
	FrontendServicePrefix = "/frontend.FrontendService/"
	AuthServicePrefix     = "/auth.AuthService/"
	healthServicePrefix   = "/grpc.health.v1.Health/"
)

// isLegacyAuthMethod names the AuthService RPCs of the single-tenant admin (team #1 accounts).
func isLegacyAuthMethod(name string) bool {
	switch name {
	case "Login", "Create", "Delete", "ChangePassword":
		return true
	}
	return false
}

// ClassifyMethod returns the binding class of a gRPC full method.
func ClassifyMethod(fullMethod string) Class {
	switch {
	case strings.HasPrefix(fullMethod, AdminServicePrefix):
		return ClassClaim
	case strings.HasPrefix(fullMethod, PlatformServicePrefix):
		return ClassPlatform
	case strings.HasPrefix(fullMethod, FrontendServicePrefix):
		return ClassStorefront
	case strings.HasPrefix(fullMethod, AuthServicePrefix):
		if isLegacyAuthMethod(strings.TrimPrefix(fullMethod, AuthServicePrefix)) {
			return ClassLegacyAuth
		}
		return ClassXTeam
	case strings.HasPrefix(fullMethod, healthServicePrefix):
		return ClassHealth
	}
	return ClassUnknown
}

// ClassifyPath returns the binding class of a chi request path (or a chi route pattern).
//
// /api/admin and /api/auth are grpc-gateway mounts: the gateway calls the gRPC server over
// loopback, where ClassifyMethod binds again; the chi class only covers the wrapper in front.
func ClassifyPath(path string) Class {
	switch {
	case under(path, "/api/admin"), under(path, "/api/files"), under(path, "/api/techcard-archive"),
		path == "/statusz":
		return ClassClaim
	case under(path, "/api/auth"):
		return ClassXTeam
	case under(path, "/api/platform"):
		return ClassPlatform
	case under(path, "/api/frontend"), under(path, "/api/webhooks"):
		return ClassStorefront
	case under(path, "/api/p"), under(path, "/api/pv"), under(path, "/api/rp"), under(path, "/api/f"):
		return ClassToken
	case under(path, "/api"):
		// Any other /api path has no handler (404) and must not run with a team.
		return ClassUnknown
	}
	// /livez, /readyz, /health, the swagger page at /, static files.
	return ClassHealth
}

// under reports whether path is prefix itself or below it (segment boundary).
func under(path, prefix string) bool {
	return path == prefix || strings.HasPrefix(path, prefix+"/")
}
