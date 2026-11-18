// Package tenantbind puts the team of a request into its context at the edge, and answers the
// team-plane questions the admin chain asks (directory, features, sessions, operators, origins).
//
// Every gRPC method and every chi route has exactly one binding class (Class). The binder binds
// the classes it can resolve by itself — storefront, token (team #1), legacy auth (team #1) — and
// leaves the claim class to the admin chain in internal/apisrv/auth, which knows the token.
//
// Teams mode OFF (no control plane) behaves exactly like the single-tenant binary: every call,
// stream and request that is not bound by a token runs as team #1.
package tenantbind

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/feature"
	"github.com/jekabolt/grbpwr-manager/internal/platformstore"
	"github.com/jekabolt/grbpwr-manager/internal/store"
	"github.com/jekabolt/grbpwr-manager/internal/tenant"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// cacheTTL bounds every per-instance cache of the binder (features, sessions, operators): a change
// made by another instance is seen no later than this.
const cacheTTL = 15 * time.Second

// sessionCacheMax bounds the session cache; expired entries are swept when it is reached.
const sessionCacheMax = 20000

// controlPlane is what the binder reads from the control plane (platformstore.Store in prod, a
// fake in tests).
type controlPlane interface {
	Directory() tenant.Directory
	SessionActive(ctx context.Context, sid string) (bool, error)
	FeatureOverrides(ctx context.Context, teamID int64) (map[string]bool, error)
	OperatorActive(ctx context.Context, userID int64) (bool, error)
}

type featureEntry struct {
	eff     map[feature.Key]bool
	expires time.Time
}

type boolEntry struct {
	ok      bool
	expires time.Time
}

// Binder is the edge binder.
type Binder struct {
	teamOne      tenant.Team
	latestSchema string
	now          func() time.Time

	mu            sync.RWMutex
	plane         controlPlane // nil = teams mode off
	dir           tenant.Directory
	storefrontID  int64
	baseDomain    string
	legacyOrigins map[string]bool

	cacheMu   sync.Mutex
	features  map[int64]featureEntry
	sessions  map[string]boolEntry
	operators map[int64]boolEntry
}

// New returns the binder for team #1 with teams mode off. UsePlatform switches teams mode on.
func New(teamOne tenant.Team) (*Binder, error) {
	if teamOne.ID != tenant.TeamOneID || teamOne.DBName == "" {
		return nil, fmt.Errorf("tenantbind: binder needs team #1 with its database, got team %d", teamOne.ID)
	}
	dir, err := tenant.NewStaticDirectory(teamOne)
	if err != nil {
		return nil, fmt.Errorf("tenantbind: %w", err)
	}
	return &Binder{
		teamOne:      teamOne,
		latestSchema: store.LatestMigrationID(),
		now:          time.Now,
		dir:          dir,
		storefrontID: tenant.TeamOneID,
		features:     map[int64]featureEntry{},
		sessions:     map[string]boolEntry{},
		operators:    map[int64]boolEntry{},
	}, nil
}

// NewCompat is the wave-1 name of New, kept so app.go keeps compiling until it is renamed.
//
// Deprecated: use New.
func NewCompat(teamOne tenant.Team) (*Binder, error) { return New(teamOne) }

// UsePlatform switches teams mode on: the directory, sessions, feature overrides and operators come
// from the control plane. A nil store keeps teams mode off.
func (b *Binder) UsePlatform(st *platformstore.Store, cfg platformstore.Config) {
	if st == nil {
		return
	}
	b.usePlane(storePlane{st}, cfg)
}

func (b *Binder) usePlane(p controlPlane, cfg platformstore.Config) {
	legacy := map[string]bool{}
	for _, o := range cfg.LegacyOrigins() {
		if n := normOrigin(o); n != "" {
			legacy[n] = true
		}
	}
	sf := cfg.StorefrontTeamID
	if sf <= 0 {
		sf = tenant.TeamOneID
	}
	b.mu.Lock()
	b.plane = p
	b.dir = p.Directory()
	b.storefrontID = sf
	b.baseDomain = strings.ToLower(strings.Trim(strings.TrimSpace(cfg.BaseDomain), "."))
	b.legacyOrigins = legacy
	b.mu.Unlock()
	b.InvalidateTeam(0)
}

func (b *Binder) state() (controlPlane, tenant.Directory) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.plane, b.dir
}

// TeamsMode reports whether a control plane is wired.
func (b *Binder) TeamsMode() bool {
	p, _ := b.state()
	return p != nil
}

// TeamOne returns team #1 as configured at boot.
func (b *Binder) TeamOne() tenant.Team { return b.teamOne }

// TeamByID resolves a team. Team #1 is always the boot-time team (it can never be suspended and
// its database is fixed at boot), so the legacy admin keeps working through a control-plane blip.
func (b *Binder) TeamByID(ctx context.Context, id int64) (tenant.Team, error) {
	if id == tenant.TeamOneID {
		return b.teamOne, nil
	}
	_, dir := b.state()
	return dir.ByID(ctx, id)
}

// teamBySlug resolves a team by slug (team #1 included).
func (b *Binder) teamBySlug(ctx context.Context, slug string) (tenant.Team, error) {
	if strings.EqualFold(slug, b.teamOne.Slug) {
		return b.teamOne, nil
	}
	_, dir := b.state()
	return dir.BySlug(ctx, strings.ToLower(slug))
}

// StorefrontTeam is the team the public storefront and the webhooks run as.
func (b *Binder) StorefrontTeam(ctx context.Context) (tenant.Team, error) {
	b.mu.RLock()
	id := b.storefrontID
	b.mu.RUnlock()
	return b.TeamByID(ctx, id)
}

// SchemaBehind reports whether a team's database is behind the binary's migrations (its calls
// answer UNAVAILABLE until the fleet migrator catches it up). Team #1 migrates synchronously at
// boot and is never behind.
func (b *Binder) SchemaBehind(t tenant.Team) bool {
	if t.ID == tenant.TeamOneID || b.latestSchema == "" {
		return false
	}
	return t.SchemaVersion == "" || t.SchemaVersion < b.latestSchema
}

// OriginAllowed is the second lock of the admin chain (teams §6). An empty origin passes (no
// browser). Otherwise the origin must be https://<team.slug>.<base>, or — for team #1 only — one of
// PLATFORM_LEGACY_ADMIN_ORIGINS. legacy marks a pre-teams token: it is team #1 but never accepted
// on a lekalo team host, so only the legacy origins admit it.
func (b *Binder) OriginAllowed(origin string, t tenant.Team, legacy bool) bool {
	if strings.TrimSpace(origin) == "" {
		return true
	}
	n := normOrigin(origin)
	if n == "" {
		return false
	}
	b.mu.RLock()
	base, legacyOK := b.baseDomain, b.legacyOrigins[n]
	b.mu.RUnlock()
	if t.ID == tenant.TeamOneID && legacyOK {
		return true
	}
	if legacy || base == "" || t.Slug == "" {
		return false
	}
	return n == "https://"+strings.ToLower(t.Slug)+"."+base
}

// normOrigin lower-cases scheme and host and drops a trailing slash; "" when it is not an origin.
func normOrigin(o string) string {
	u, err := url.Parse(strings.TrimSpace(o))
	if err != nil || u.Host == "" || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.User != nil {
		return ""
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "https" && scheme != "http" {
		return ""
	}
	return scheme + "://" + strings.ToLower(u.Host)
}

// SessionActive reports whether a platform session is live (15 s cache). Teams mode off: false.
func (b *Binder) SessionActive(ctx context.Context, sid string) (bool, error) {
	p, _ := b.state()
	if p == nil || sid == "" {
		return false, nil
	}
	now := b.now()
	b.cacheMu.Lock()
	if e, ok := b.sessions[sid]; ok && now.Before(e.expires) {
		b.cacheMu.Unlock()
		return e.ok, nil
	}
	b.cacheMu.Unlock()
	ok, err := p.SessionActive(ctx, sid)
	if err != nil {
		return false, err
	}
	b.cacheMu.Lock()
	if len(b.sessions) >= sessionCacheMax {
		for k, e := range b.sessions {
			if !now.Before(e.expires) {
				delete(b.sessions, k)
			}
		}
		if len(b.sessions) >= sessionCacheMax {
			b.sessions = map[string]boolEntry{}
		}
	}
	b.sessions[sid] = boolEntry{ok: ok, expires: now.Add(cacheTTL)}
	b.cacheMu.Unlock()
	return ok, nil
}

// Operator re-checks that a platform user is an active operator (15 s cache). Teams mode off: false.
func (b *Binder) Operator(ctx context.Context, userID int64) (bool, error) {
	p, _ := b.state()
	if p == nil || userID <= 0 {
		return false, nil
	}
	now := b.now()
	b.cacheMu.Lock()
	if e, ok := b.operators[userID]; ok && now.Before(e.expires) {
		b.cacheMu.Unlock()
		return e.ok, nil
	}
	b.cacheMu.Unlock()
	ok, err := p.OperatorActive(ctx, userID)
	if err != nil {
		return false, err
	}
	b.cacheMu.Lock()
	b.operators[userID] = boolEntry{ok: ok, expires: now.Add(cacheTTL)}
	b.cacheMu.Unlock()
	return ok, nil
}

// Features returns the effective feature switches of a team (15 s cache, InvalidateTeam drops it).
// Teams mode off: team #1, profile all, no overrides.
func (b *Binder) Features(ctx context.Context, t tenant.Team) (map[feature.Key]bool, error) {
	p, _ := b.state()
	if p == nil {
		return feature.Effective(t.ID, profileOf(t), nil), nil
	}
	now := b.now()
	b.cacheMu.Lock()
	if e, ok := b.features[t.ID]; ok && now.Before(e.expires) {
		b.cacheMu.Unlock()
		return e.eff, nil
	}
	b.cacheMu.Unlock()
	overrides, err := p.FeatureOverrides(ctx, t.ID)
	if err != nil {
		return nil, err
	}
	eff := feature.Effective(t.ID, profileOf(t), overrides)
	b.cacheMu.Lock()
	b.features[t.ID] = featureEntry{eff: eff, expires: now.Add(cacheTTL)}
	b.cacheMu.Unlock()
	return eff, nil
}

func profileOf(t tenant.Team) string {
	if t.ID == tenant.TeamOneID && t.Profile == "" {
		return tenant.ProfileAll
	}
	return t.Profile
}

// InvalidateTeam drops this instance's cached features, directory entry and status of a team, so
// a toggle made here is seen by the next call. teamID 0 drops every team's features.
//
// It is a method, not a package function: the binder is the cache owner, and a package-level
// registry would add process-wide mutable state (tenantgate G4a).
func (b *Binder) InvalidateTeam(teamID int64) {
	b.cacheMu.Lock()
	if teamID == 0 {
		b.features = map[int64]featureEntry{}
	} else {
		delete(b.features, teamID)
	}
	b.cacheMu.Unlock()
	if teamID != 0 {
		_, dir := b.state()
		dir.Invalidate(teamID)
	}
}

// ---------------------------------------------------------------- binding

// errTeamMismatch: an old (team #1) AuthService call named another team in x-team.
var errTeamMismatch = errors.New("tenantbind: legacy auth is team #1 only")

// bindFor returns the context of a call of the given class, or an error the caller turns into a
// refusal. ClassClaim / ClassPlatform / ClassHealth are left unbound in teams mode (the admin
// chain binds the claim; the control plane and health have no team).
func (b *Binder) bindFor(ctx context.Context, c Class) (context.Context, error) {
	teams := b.TeamsMode()
	switch c {
	case ClassStorefront:
		t, err := b.StorefrontTeam(ctx)
		if err != nil {
			return nil, status.Error(codes.Unavailable, "storefront team unavailable")
		}
		return tenant.With(ctx, t), nil
	case ClassToken:
		return tenant.With(ctx, b.teamOne), nil
	case ClassLegacyAuth:
		if slug := tenant.HeaderSlug(ctx); slug != "" && !strings.EqualFold(slug, b.teamOne.Slug) {
			return nil, status.Error(codes.PermissionDenied, errTeamMismatch.Error())
		}
		return tenant.With(ctx, b.teamOne), nil
	case ClassXTeam, ClassClaim, ClassPlatform, ClassHealth:
		if !teams {
			// Exactly as the single-tenant binary: everything runs as team #1.
			return tenant.With(ctx, b.teamOne), nil
		}
		return ctx, nil
	default:
		if !teams {
			return tenant.With(ctx, b.teamOne), nil
		}
		return nil, status.Error(codes.Unimplemented, "unclassified method")
	}
}

// Unary binds the team of a unary call by its method's class.
func (b *Binder) Unary() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		bound, err := b.bindFor(ctx, ClassifyMethod(info.FullMethod))
		if err != nil {
			return nil, err
		}
		return handler(bound, req)
	}
}

// Stream binds the team of a stream by its method's class.
func (b *Binder) Stream() grpc.StreamServerInterceptor {
	return func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		bound, err := b.bindFor(ss.Context(), ClassifyMethod(info.FullMethod))
		if err != nil {
			return err
		}
		return handler(srv, &boundStream{ServerStream: ss, ctx: bound})
	}
}

// Middleware binds the team of a chi / net/http request by its path's class.
func (b *Binder) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bound, err := b.bindFor(r.Context(), ClassifyPath(r.URL.Path))
		if err != nil {
			w.Header().Set("Content-Type", "application/json")
			if status.Code(err) == codes.Unavailable {
				w.WriteHeader(http.StatusServiceUnavailable)
			} else {
				w.WriteHeader(http.StatusForbidden)
			}
			_, _ = w.Write([]byte(`{"error":"team unavailable"}`))
			return
		}
		next.ServeHTTP(w, r.WithContext(bound))
	})
}

type boundStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (s *boundStream) Context() context.Context { return s.ctx }

// ---------------------------------------------------------------- control plane over the store

// storePlane is controlPlane over platformstore.Store; the operator re-check reads platform_user.
type storePlane struct{ st *platformstore.Store }

func (p storePlane) Directory() tenant.Directory { return p.st.Directory() }
func (p storePlane) SessionActive(ctx context.Context, sid string) (bool, error) {
	return p.st.SessionActive(ctx, sid)
}
func (p storePlane) FeatureOverrides(ctx context.Context, teamID int64) (map[string]bool, error) {
	return p.st.FeatureOverrides(ctx, teamID)
}

func (p storePlane) OperatorActive(ctx context.Context, userID int64) (bool, error) {
	var row struct {
		IsOperator bool   `db:"is_operator"`
		Status     string `db:"status"`
	}
	err := p.st.DB().GetContext(ctx, &row, `SELECT is_operator, status FROM platform_user WHERE id = ?`, userID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, fmt.Errorf("tenantbind: operator: %w", err)
	}
	return row.IsOperator && row.Status == "active", nil
}
