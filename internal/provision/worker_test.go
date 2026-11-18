package provision_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/platformstore"
	"github.com/jekabolt/grbpwr-manager/internal/provision"
	"github.com/jekabolt/grbpwr-manager/internal/store"
	"github.com/jekabolt/grbpwr-manager/internal/tenant"
)

const (
	prefix  = "lkb_t_"
	team1DB = "grbpwr_beta"
)

func TestNameShape(t *testing.T) {
	re := regexp.MustCompile(`^lkb_t_[a-z0-9]{8}$`)
	seen := map[string]bool{}
	for range 200 {
		n, err := provision.NewDBName(prefix, team1DB)
		if err != nil {
			t.Fatal(err)
		}
		if !re.MatchString(n) {
			t.Fatalf("name %q", n)
		}
		seen[n] = true
	}
	if len(seen) < 195 {
		t.Fatalf("names repeat: %d distinct of 200", len(seen))
	}
}

func TestNameAllowlist(t *testing.T) {
	good := []string{"lkb_t_abcd1234", "lkb_t_00000000"}
	for _, n := range good {
		if err := store.NewTeamDBName(prefix, team1DB, n); err != nil {
			t.Errorf("%q refused: %v", n, err)
		}
	}
	bad := []string{
		team1DB, "grbpwr", "lkb_t_abc", "lkb_t_abcd12345", "lkb_t_ABCD1234", "lkb_t_abcd-123",
		"lkb_t_abcd`123", "lkb_t_ab.d1234", "lkb_t_abcd123;", "lk_t_abcd1234", "xlkb_t_abcd1234",
		"lkb_t_abcd1234 ", "", "lkb_t_abc\n1234", "lkb_t_abcd_123",
	}
	for _, n := range bad {
		if err := store.NewTeamDBName(prefix, team1DB, n); err == nil {
			t.Errorf("%q allowed", n)
		}
	}
	// A database that is team #1's is refused even when it fits the pattern.
	if err := store.NewTeamDBName(prefix, "lkb_t_abcd1234", "lkb_t_abcd1234"); err == nil {
		t.Error("team #1's database allowed")
	}
	for _, p := range []string{"", "lkb-t_", "lkb_t_`", "LKB_", strings.Repeat("a", 57)} {
		if err := store.NewTeamDBName(p, team1DB, p+"abcd1234"); err == nil {
			t.Errorf("prefix %q allowed", p)
		}
	}
	// CreateTeamDB checks before it ever connects: a bad name never reaches the server.
	err := store.CreateTeamDB(context.Background(), "u:p@tcp(127.0.0.1:1)/"+team1DB, prefix, team1DB)
	if err == nil || !strings.Contains(err.Error(), "team database name") {
		t.Fatalf("CreateTeamDB(team #1) = %v", err)
	}
	if _, err := store.TeamDSN("u:p@tcp(h:3306)/x", "a`b"); err == nil {
		t.Fatal("TeamDSN took a backtick")
	}
}

func TestNameNewRefusesBadConfig(t *testing.T) {
	pf := newFakePlatform()
	for _, c := range []provision.Config{
		{Prefix: "", Team1DB: team1DB},
		{Prefix: "bad-prefix", Team1DB: team1DB},
		{Prefix: prefix, Team1DB: "lkb_t_abcd1234"}, // team #1 looks like a new team's database
	} {
		if _, err := provision.New(c, pf, &fakeSchema{}, nopDB{}, nopAdmin{}); err == nil {
			t.Errorf("config %+v accepted", c)
		}
	}
}

func TestNameSchemaDiff(t *testing.T) {
	want := []string{"a", "b", "c", "e"}
	got := []string{"a", "c", "d", "e"}
	d := store.FirstSchemaDiff(want, got, 5)
	if fmt.Sprint(d) != "[- b + d]" {
		t.Fatalf("diff %v", d)
	}
	if store.FingerprintOf(want) == store.FingerprintOf(got) || len(store.FingerprintOf(nil)) != 64 {
		t.Fatal("fingerprint")
	}
}

// ─── worker with fakes ─────────────────────────────────────────────────────────────────────────

type nopDB struct{ dependency.DB }
type nopAdmin struct{ dependency.Admin }

type fakeSchema struct {
	mu       sync.Mutex
	latest   string
	created  []string
	migrated []string
	failOn   map[string]error // db → Migrate error
}

func (s *fakeSchema) Create(_ context.Context, db string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.created = append(s.created, db)
	return nil
}
func (s *fakeSchema) Migrate(_ context.Context, db string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.failOn[db]; err != nil {
		return "", err
	}
	s.migrated = append(s.migrated, db)
	return s.latest, nil
}
func (s *fakeSchema) Lines(_ context.Context, db string) ([]string, error) {
	return []string{"c\tt\tid\tint\tNO\t<null>\t"}, nil
}
func (s *fakeSchema) Latest() string { return s.latest }

type fakeDir struct{ pf *fakePlatform }

func (d fakeDir) ByID(_ context.Context, id int64) (tenant.Team, error) {
	d.pf.mu.Lock()
	defer d.pf.mu.Unlock()
	t, ok := d.pf.teams[id]
	if !ok {
		return tenant.Team{}, tenant.ErrNotFound
	}
	return tenant.Team{ID: id, Slug: t.slug, DBName: t.db, Status: tenant.Status(t.status)}, nil
}
func (d fakeDir) BySlug(context.Context, string) (tenant.Team, error) {
	return tenant.Team{}, tenant.ErrNotFound
}
func (d fakeDir) ListActive(context.Context) ([]tenant.Team, error) { return nil, nil }
func (d fakeDir) Invalidate(int64)                                  {}

type fakeTeam struct {
	slug, db, status, version, owner string
}

type fakePlatform struct {
	mu           sync.Mutex
	spares       map[string]*platformstore.Spare
	teams        map[int64]*fakeTeam
	createdSince int
	swept        int
}

func newFakePlatform() *fakePlatform {
	return &fakePlatform{spares: map[string]*platformstore.Spare{}, teams: map[int64]*fakeTeam{}}
}

func ns(s string) sql.NullString { return sql.NullString{String: s, Valid: s != ""} }

func (p *fakePlatform) CountSpares(_ context.Context, v string, _ time.Time) (platformstore.SpareCounts, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	c := platformstore.SpareCounts{CreatedSince: p.createdSince}
	for _, s := range p.spares {
		switch {
		case s.Status == platformstore.SpareReady && s.SchemaVersion.String == v:
			c.Ready++
		case s.Status == platformstore.SpareCreating:
			c.Creating++
		}
	}
	return c, nil
}
func (p *fakePlatform) InsertSpare(_ context.Context, db string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.spares[db] = &platformstore.Spare{DBName: db, Status: platformstore.SpareCreating}
	p.createdSince++
	return nil
}
func (p *fakePlatform) MarkSpareReady(_ context.Context, db, v, fp string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	s := p.spares[db]
	s.Status, s.SchemaVersion, s.SchemaFingerprint = platformstore.SpareReady, ns(v), ns(fp)
	return nil
}
func (p *fakePlatform) MarkSpareFailed(_ context.Context, db, _ string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.spares[db].Status = platformstore.SpareFailed
	return nil
}
func (p *fakePlatform) SetSpareSchema(_ context.Context, db, v, fp string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	s := p.spares[db]
	s.SchemaVersion, s.SchemaFingerprint = ns(v), ns(fp)
	return nil
}
func (p *fakePlatform) ReadySpares(context.Context) ([]platformstore.Spare, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []platformstore.Spare
	for _, s := range p.spares {
		if s.Status == platformstore.SpareReady {
			out = append(out, *s)
		}
	}
	return out, nil
}
func (p *fakePlatform) ProvisioningTeams(context.Context, int) ([]platformstore.ProvisioningTeam, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []platformstore.ProvisioningTeam
	for id, t := range p.teams {
		if t.status == "provisioning" {
			out = append(out, platformstore.ProvisioningTeam{ID: id, Slug: t.slug, DBName: ns(t.db), OwnerHandle: t.owner})
		}
	}
	return out, nil
}
func (p *fakePlatform) ClaimSpare(_ context.Context, id int64, v string) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	t := p.teams[id]
	if t == nil || t.status != "provisioning" {
		return "", platformstore.ErrNotProvisioning
	}
	if t.db != "" {
		return t.db, nil
	}
	for name, s := range p.spares {
		if s.Status == platformstore.SpareReady && s.SchemaVersion.String == v {
			s.Status = platformstore.SpareClaimed
			t.db = name
			return name, nil
		}
	}
	return "", platformstore.ErrNoSpare
}
func (p *fakePlatform) ActivateTeam(_ context.Context, id int64, v, _ string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	t := p.teams[id]
	if t.status != "provisioning" {
		return platformstore.ErrNotProvisioning
	}
	t.status, t.version = "active", v
	return nil
}
func (p *fakePlatform) SetTeamSchema(_ context.Context, id int64, v, _ string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.teams[id].version = v
	return nil
}
func (p *fakePlatform) SchemaTeams(context.Context) ([]platformstore.SchemaTeam, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []platformstore.SchemaTeam
	for id, t := range p.teams {
		if t.status == "active" || t.status == "suspended" {
			out = append(out, platformstore.SchemaTeam{ID: id, Slug: t.slug, DBName: t.db, Status: t.status, SchemaVersion: ns(t.version)})
		}
	}
	return out, nil
}
func (p *fakePlatform) ReferenceSpare(context.Context, string) (string, string, bool, error) {
	return "", "", false, nil
}
func (p *fakePlatform) SweepPendingTeams(context.Context, time.Time, int) (int, error) {
	p.swept++
	return 0, nil
}
func (p *fakePlatform) Directory() tenant.Directory { return fakeDir{p} }

func newWorker(t *testing.T, pf *fakePlatform, sc *fakeSchema, spares int) *provision.Worker {
	t.Helper()
	w, err := provision.New(provision.Config{Prefix: prefix, Team1DB: team1DB, Spares: spares, PerHour: 3}, pf, sc, nopDB{}, nopAdmin{})
	if err != nil {
		t.Fatal(err)
	}
	return w
}

func TestWorkerKeeperFillsAndRespectsCap(t *testing.T) {
	pf, sc := newFakePlatform(), &fakeSchema{latest: "0477_x.sql"}
	w := newWorker(t, pf, sc, 2)
	ctx := context.Background()
	for range 4 {
		if err := w.KeepSparesOnce(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if len(sc.created) != 2 {
		t.Fatalf("created %v, want 2 spares", sc.created)
	}
	// The hourly cap stops creation even when spares run short.
	pf.createdSince = 3
	for _, s := range pf.spares {
		s.Status = platformstore.SpareClaimed
	}
	if err := w.KeepSparesOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if len(sc.created) != 2 {
		t.Fatalf("created past the cap: %v", sc.created)
	}
}

func TestWorkerFailedSpareStaysFailed(t *testing.T) {
	pf := newFakePlatform()
	sc := &fakeSchema{latest: "0477_x.sql", failOn: map[string]error{}}
	w := newWorker(t, pf, sc, 1)
	// Fail every migrate: the name is random, so fail through a wrapper.
	failing := &failingSchema{fakeSchema: sc}
	w2, _ := provision.New(provision.Config{Prefix: prefix, Team1DB: team1DB, Spares: 1}, pf, failing, nopDB{}, nopAdmin{})
	_ = w
	if err := w2.KeepSparesOnce(context.Background()); err == nil {
		t.Fatal("no error from a failing migrate")
	}
	if len(pf.spares) != 1 {
		t.Fatalf("spares %v", pf.spares)
	}
	for _, s := range pf.spares {
		if s.Status != platformstore.SpareFailed {
			t.Fatalf("spare state %s", s.Status)
		}
	}
}

type failingSchema struct{ *fakeSchema }

func (f *failingSchema) Migrate(context.Context, string) (string, error) {
	return "", errors.New("boom")
}

func TestWorkerClaimSeedActivate(t *testing.T) {
	pf, sc := newFakePlatform(), &fakeSchema{latest: "0477_x.sql"}
	w := newWorker(t, pf, sc, 1)
	ctx := context.Background()
	if err := w.KeepSparesOnce(ctx); err != nil {
		t.Fatal(err)
	}
	pf.teams[7] = &fakeTeam{slug: "alpha", status: "provisioning", owner: "kate"}
	var seeded []string
	w.SetSeedForTest(func(ctx context.Context, owner string) error {
		tm, _ := tenant.From(ctx)
		seeded = append(seeded, fmt.Sprintf("%d:%s:%s", tm.ID, tm.DBName, owner))
		return nil
	})
	if err := w.ClaimOnce(ctx); err != nil {
		t.Fatal(err)
	}
	team := pf.teams[7]
	if team.status != "active" || team.version != "0477_x.sql" || team.db == "" {
		t.Fatalf("team %+v", team)
	}
	if len(seeded) != 1 || seeded[0] != "7:"+team.db+":kate" {
		t.Fatalf("seeded %v", seeded)
	}
	// Claimed spare is not migrated again (it is at the latest version already).
	if len(sc.migrated) != 1 {
		t.Fatalf("migrated %v", sc.migrated)
	}
}

func TestWorkerColdPath(t *testing.T) {
	pf, sc := newFakePlatform(), &fakeSchema{latest: "0477_x.sql"}
	w := newWorker(t, pf, sc, 1)
	w.SetSeedForTest(func(context.Context, string) error { return nil })
	pf.teams[9] = &fakeTeam{slug: "bravo", status: "provisioning", owner: "ann"}
	if err := w.ClaimOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if pf.teams[9].status != "active" || len(sc.created) != 1 || pf.teams[9].db != sc.created[0] {
		t.Fatalf("team %+v created %v", pf.teams[9], sc.created)
	}
}

func TestWorkerSeedFailureRetriesWithBackoff(t *testing.T) {
	pf, sc := newFakePlatform(), &fakeSchema{latest: "0477_x.sql"}
	w := newWorker(t, pf, sc, 1)
	now := time.Date(2026, 11, 19, 10, 0, 0, 0, time.UTC)
	w.SetNowForTest(func() time.Time { return now })
	calls := 0
	w.SetSeedForTest(func(context.Context, string) error {
		calls++
		if calls == 1 {
			return errors.New("seed broke")
		}
		return nil
	})
	pf.teams[3] = &fakeTeam{slug: "c", status: "provisioning", owner: "o"}
	ctx := context.Background()
	if err := w.ClaimOnce(ctx); err == nil {
		t.Fatal("seed failure not reported")
	}
	if pf.teams[3].status != "provisioning" {
		t.Fatal("activated after a failed seed")
	}
	_ = w.ClaimOnce(ctx) // not due yet
	if calls != 1 {
		t.Fatalf("retried before the backoff: %d calls", calls)
	}
	now = now.Add(3 * time.Second)
	if err := w.ClaimOnce(ctx); err != nil {
		t.Fatal(err)
	}
	// The rerun found the database already attached: it migrated it (rerun path), seeded, activated.
	if calls != 2 || pf.teams[3].status != "active" {
		t.Fatalf("calls %d team %+v", calls, pf.teams[3])
	}
}

func TestWorkerRefusesForeignDatabase(t *testing.T) {
	pf, sc := newFakePlatform(), &fakeSchema{latest: "0477_x.sql"}
	w := newWorker(t, pf, sc, 1)
	seeded := false
	w.SetSeedForTest(func(context.Context, string) error { seeded = true; return nil })
	// A provisioning team pointing at team #1's database (a corrupted row) is never touched.
	pf.teams[4] = &fakeTeam{slug: "evil", status: "provisioning", owner: "o", db: team1DB}
	if err := w.ClaimOnce(context.Background()); err == nil {
		t.Fatal("team #1's database accepted")
	}
	if seeded || len(sc.migrated) != 0 || pf.teams[4].status != "provisioning" {
		t.Fatalf("touched: seeded=%v migrated=%v", seeded, sc.migrated)
	}
	// Team #1 itself is never provisioned.
	if err := w.ProvisionTeam(context.Background(), platformstore.ProvisioningTeam{ID: tenant.TeamOneID, OwnerHandle: "o"}); err == nil {
		t.Fatal("team #1 provisioned")
	}
}

func TestWorkerFleetMigratesOnlyBehind(t *testing.T) {
	pf, sc := newFakePlatform(), &fakeSchema{latest: "0477_x.sql", failOn: map[string]error{}}
	w := newWorker(t, pf, sc, 1)
	pf.teams[2] = &fakeTeam{slug: "cur", status: "active", db: "lkb_t_cur00000", version: "0477_x.sql"}
	pf.teams[3] = &fakeTeam{slug: "old", status: "suspended", db: "lkb_t_old00000", version: "0476_y.sql"}
	pf.teams[5] = &fakeTeam{slug: "brk", status: "active", db: "lkb_t_brk00000", version: "0476_y.sql"}
	pf.teams[6] = &fakeTeam{slug: "bad", status: "active", db: team1DB, version: "0476_y.sql"}
	pf.spares["lkb_t_spr00000"] = &platformstore.Spare{DBName: "lkb_t_spr00000", Status: platformstore.SpareReady, SchemaVersion: ns("0476_y.sql")}
	sc.failOn["lkb_t_brk00000"] = errors.New("lock wait")
	if err := w.MigrateFleetOnce(context.Background()); err == nil {
		t.Fatal("failures not reported")
	}
	got := fmt.Sprint(sc.migrated)
	for _, want := range []string{"lkb_t_old00000", "lkb_t_spr00000"} {
		if !strings.Contains(got, want) {
			t.Fatalf("migrated %v, missing %s", sc.migrated, want)
		}
	}
	if strings.Contains(got, "lkb_t_cur00000") || strings.Contains(got, team1DB) {
		t.Fatalf("migrated %v", sc.migrated)
	}
	if pf.teams[3].version != "0477_x.sql" || pf.teams[5].version != "0476_y.sql" || pf.spares["lkb_t_spr00000"].SchemaVersion.String != "0477_x.sql" {
		t.Fatalf("versions: old=%s brk=%s spare=%s", pf.teams[3].version, pf.teams[5].version, pf.spares["lkb_t_spr00000"].SchemaVersion.String)
	}
}

func TestWorkerStartStop(t *testing.T) {
	pf, sc := newFakePlatform(), &fakeSchema{latest: "0477_x.sql"}
	w, err := provision.New(provision.Config{Prefix: prefix, Team1DB: team1DB, Spares: 1,
		Every: provision.Intervals{Keeper: 5 * time.Millisecond, Claim: 5 * time.Millisecond, Fleet: time.Hour, Sweep: 5 * time.Millisecond, FleetDelay: time.Hour}},
		pf, sc, nopDB{}, nopAdmin{})
	if err != nil {
		t.Fatal(err)
	}
	w.SetSeedForTest(func(context.Context, string) error { return nil })
	pf.teams[8] = &fakeTeam{slug: "d", status: "provisioning", owner: "o"}
	stop := w.Start(context.Background())
	deadline := time.Now().Add(2 * time.Second)
	for {
		pf.mu.Lock()
		done := pf.teams[8].status == "active"
		pf.mu.Unlock()
		if done || time.Now().After(deadline) {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	stop()
	if pf.teams[8].status != "active" {
		t.Fatal("the running worker did not provision the team")
	}
}
