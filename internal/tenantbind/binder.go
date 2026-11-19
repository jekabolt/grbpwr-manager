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
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/feature"
	"github.com/jekabolt/grbpwr-manager/internal/platformstore"
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
	// TeamSession reads the live tuple behind a team token: the session sid of user uid in team
	// tid (aud team) is not revoked, the platform user is active and the membership is active.
	// ok=false when any part is missing; role is the membership role.
	TeamSession(ctx context.Context, sid string, uid, tid int64) (role string, ok bool, err error)
	// PasswordChangedAt is the last password change of the platform user with this handle (the
	// «not before» of legacy team #1 tokens). ok=false: no such user or never changed.
	PasswordChangedAt(ctx context.Context, handle string) (at time.Time, ok bool, err error)
}

// Membership is the live state behind a team token (TeamSession).
type Membership struct {
	UserID int64
	TeamID int64
	Role   string // "owner" or "member"
	Active bool
}

type memberEntry struct {
	m       Membership
	expires time.Time
}

// MsgTeamMismatch is the error text when the Host / X-Team of a request name no team that can serve
// it (an invalid or unknown address, Host and X-Team disagree) or not the token's team.
const MsgTeamMismatch = "team_mismatch"

// teamMismatchStatus is the refusal RequestedTeam returns for such a request (a func, not a package var: G4).
func teamMismatchStatus() error { return status.Error(codes.PermissionDenied, MsgTeamMismatch) }

type featureEntry struct {
	eff     map[feature.Key]bool
	expires time.Time
}

type boolEntry struct {
	ok      bool
	expires time.Time
}

type notBeforeEntry struct {
	at      time.Time // zero = no password change recorded
	expires time.Time
}

// Binder is the edge binder.
type Binder struct {
	teamOne tenant.Team
	now     func() time.Time

	mu            sync.RWMutex
	plane         controlPlane // nil = teams mode off
	dir           tenant.Directory
	storefrontID  int64
	baseDomain    string
	legacyOrigins map[string]bool
	behind        func(tenant.Team) bool // nil = every team is current

	cacheMu   sync.Mutex
	features  map[int64]featureEntry
	sessions  map[string]boolEntry
	operators map[int64]boolEntry
	members   map[string]memberEntry
	// notBefore caches PasswordChangedAt by lower-cased handle; credGen counts invalidations, so a
	// lookup that raced one never stores the answer it read before the change.
	notBefore map[string]notBeforeEntry
	credGen   uint64
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
		now:          time.Now,
		dir:          dir,
		storefrontID: tenant.TeamOneID,
		features:     map[int64]featureEntry{},
		sessions:     map[string]boolEntry{},
		operators:    map[int64]boolEntry{},
		members:      map[string]memberEntry{},
		notBefore:    map[string]notBeforeEntry{},
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

// UseSchemaCheck injects the predicate «this team's database is behind the binary's migrations»
// (the fleet migrator's). nil restores the default: every team is current.
func (b *Binder) UseSchemaCheck(behind func(tenant.Team) bool) {
	b.mu.Lock()
	b.behind = behind
	b.mu.Unlock()
}

// SchemaBehind reports whether a team's database is behind the binary's migrations: its calls
// answer UNAVAILABLE until the fleet migrator catches it up. Team #1 migrates synchronously at
// boot and is never behind.
func (b *Binder) SchemaBehind(t tenant.Team) bool {
	if t.ID == tenant.TeamOneID {
		return false
	}
	b.mu.RLock()
	behind := b.behind
	b.mu.RUnlock()
	return behind != nil && behind(t)
}

// RequestedTeam is the team a request names by its Host and X-Team header (teams §6). ok=false: it
// names none (teams mode off; the API, root or any non-team host; no header). Host and X-Team
// must agree when both name a team. Host is lower-cased and loses its port and trailing dot; a team
// label is one ASCII slug label under the base domain. An invalid, reserved or unknown address and a
// disagreement is refused with PermissionDenied team_mismatch; a control-plane failure is
// Unavailable. The Origin is not consulted: it is a browser defence only.
func (b *Binder) RequestedTeam(ctx context.Context, host, xteam string) (tenant.Team, bool, error) {
	if !b.TeamsMode() {
		return tenant.Team{}, false, nil
	}
	b.mu.RLock()
	base := b.baseDomain
	b.mu.RUnlock()

	fromHost, err := b.hostSlug(host, base)
	if err != nil {
		return tenant.Team{}, false, err
	}
	fromHeader := ""
	if h := strings.ToLower(strings.TrimSpace(xteam)); h != "" {
		if !b.validTeamLabel(h) {
			return tenant.Team{}, false, teamMismatchStatus()
		}
		fromHeader = h
	}
	slug := fromHost
	switch {
	case fromHost != "" && fromHeader != "" && fromHost != fromHeader:
		return tenant.Team{}, false, teamMismatchStatus()
	case slug == "":
		slug = fromHeader
	}
	if slug == "" {
		return tenant.Team{}, false, nil
	}
	t, err := b.teamBySlug(ctx, slug)
	if err != nil {
		if errors.Is(err, tenant.ErrNotFound) {
			return tenant.Team{}, false, teamMismatchStatus()
		}
		return tenant.Team{}, false, status.Error(codes.Unavailable, "team lookup unavailable")
	}
	return t, true, nil
}

// hostSlug returns the team label of a Host under base, "" for a host that names no team.
func (b *Binder) hostSlug(host, base string) (string, error) {
	h := strings.ToLower(strings.TrimSpace(host))
	if hh, _, err := net.SplitHostPort(h); err == nil {
		h = hh
	}
	h = strings.TrimSuffix(h, ".")
	if base == "" || h == "" || h == base || !strings.HasSuffix(h, "."+base) {
		return "", nil
	}
	label := strings.TrimSuffix(h, "."+base)
	if !strings.Contains(label, ".") && label != b.teamOne.Slug && tenant.IsReservedSlug(label) {
		return "", nil // a platform host (api, console, www …)
	}
	if strings.Contains(label, ".") || !b.validTeamLabel(label) {
		return "", teamMismatchStatus()
	}
	return label, nil
}

// validTeamLabel: team #1's own slug, or a valid (ASCII, unreserved) workspace address.
func (b *Binder) validTeamLabel(l string) bool {
	return l == b.teamOne.Slug || tenant.ValidSlug(l) == nil
}

// TeamSession re-checks the live tuple behind a team token (15 s cache by sid; the cached answer
// must match uid and tid). Teams mode off or an empty sid: not active.
func (b *Binder) TeamSession(ctx context.Context, sid string, uid, tid int64) (role string, active bool, err error) {
	m, err := b.teamSession(ctx, sid, uid, tid)
	return m.Role, m.Active, err
}

func (b *Binder) teamSession(ctx context.Context, sid string, uid, tid int64) (Membership, error) {
	p, _ := b.state()
	if p == nil || sid == "" || uid <= 0 || tid <= 0 {
		return Membership{}, nil
	}
	now := b.now()
	b.cacheMu.Lock()
	e, hit := b.members[sid]
	b.cacheMu.Unlock()
	if !hit || !now.Before(e.expires) || e.m.UserID != uid || e.m.TeamID != tid {
		role, ok, err := p.TeamSession(ctx, sid, uid, tid)
		if err != nil {
			return Membership{}, err
		}
		e = memberEntry{m: Membership{UserID: uid, TeamID: tid, Role: role, Active: ok}, expires: now.Add(cacheTTL)}
		b.cacheMu.Lock()
		if len(b.members) >= sessionCacheMax {
			for k, old := range b.members {
				if !now.Before(old.expires) {
					delete(b.members, k)
				}
			}
			if len(b.members) >= sessionCacheMax {
				b.members = map[string]memberEntry{}
			}
		}
		b.members[sid] = e
		b.cacheMu.Unlock()
	}
	return e.m, nil
}

// InvalidateSession drops the cached session and membership answers of sid on this instance
// (removal, demotion, sign-out). Other instances see the change within cacheTTL.
func (b *Binder) InvalidateSession(sid string) {
	b.cacheMu.Lock()
	delete(b.members, sid)
	delete(b.sessions, sid)
	b.cacheMu.Unlock()
}

// LegacyNotBefore is the instant before which a legacy team #1 token of this handle is refused:
// its platform user's last password change (reset or admin-panel change; 15 s cache, dropped on
// this instance by InvalidateCredentials). Legacy tokens carry no session id, so this is how a
// password change reaches them. Zero time = nothing to refuse (teams mode off, no platform user,
// never changed).
func (b *Binder) LegacyNotBefore(ctx context.Context, handle string) (time.Time, error) {
	p, _ := b.state()
	key := strings.ToLower(strings.TrimSpace(handle))
	if p == nil || key == "" {
		return time.Time{}, nil
	}
	now := b.now()
	b.cacheMu.Lock()
	e, hit := b.notBefore[key]
	gen := b.credGen
	b.cacheMu.Unlock()
	if hit && now.Before(e.expires) {
		return e.at, nil
	}
	at, ok, err := p.PasswordChangedAt(ctx, key)
	if err != nil {
		return time.Time{}, err
	}
	if !ok {
		at = time.Time{}
	}
	b.cacheMu.Lock()
	if b.credGen == gen {
		if len(b.notBefore) >= sessionCacheMax {
			b.notBefore = map[string]notBeforeEntry{}
		}
		b.notBefore[key] = notBeforeEntry{at: at, expires: now.Add(cacheTTL)}
	}
	b.cacheMu.Unlock()
	return at, nil
}

// InvalidateCredentials drops the cached LegacyNotBefore of a handle on this instance (password
// reset or change). Other instances see the change within cacheTTL.
func (b *Binder) InvalidateCredentials(handle string) {
	b.cacheMu.Lock()
	delete(b.notBefore, strings.ToLower(strings.TrimSpace(handle)))
	b.credGen++
	b.cacheMu.Unlock()
}

// InvalidateMember drops every cached answer of user uid in team tid on this instance.
func (b *Binder) InvalidateMember(uid, tid int64) {
	b.cacheMu.Lock()
	for k, e := range b.members {
		if e.m.UserID == uid && e.m.TeamID == tid {
			delete(b.members, k)
		}
	}
	b.cacheMu.Unlock()
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
		b.members = map[string]memberEntry{}
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
		if b.SchemaBehind(t) {
			return nil, status.Error(codes.Unavailable, "team is being updated")
		}
		return tenant.With(ctx, t), nil
	case ClassToken:
		return tenant.With(ctx, b.teamOne), nil
	case ClassHealth:
		// Probes, swagger, static files: team #1 in both modes. /readyz pings the strict store, which
		// refuses a ctx with no team (503 on every probe); team #1 is the boot team whose database
		// the process cannot run without, so it is the readiness signal. /livez touches no database.
		return tenant.With(ctx, b.teamOne), nil
	case ClassLegacyAuth:
		if slug := tenant.HeaderSlug(ctx); slug != "" && !strings.EqualFold(slug, b.teamOne.Slug) {
			return nil, status.Error(codes.PermissionDenied, errTeamMismatch.Error())
		}
		return tenant.With(ctx, b.teamOne), nil
	case ClassXTeam, ClassClaim, ClassPlatform:
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

func (p storePlane) TeamSession(ctx context.Context, sid string, uid, tid int64) (string, bool, error) {
	return p.st.TeamSession(ctx, sid, uid, tid)
}
func (p storePlane) PasswordChangedAt(ctx context.Context, handle string) (time.Time, bool, error) {
	return p.st.PasswordChangedAt(ctx, handle)
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
