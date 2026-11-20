package provision_test

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/schemacompat"
)

// recLog counts records by message.
type recLog struct {
	mu   sync.Mutex
	msgs map[string]int
	lvl  map[string]slog.Level
}

func (r *recLog) Enabled(context.Context, slog.Level) bool { return true }
func (r *recLog) Handle(_ context.Context, rec slog.Record) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.msgs == nil {
		r.msgs, r.lvl = map[string]int{}, map[string]slog.Level{}
	}
	r.msgs[rec.Message]++
	r.lvl[rec.Message] = rec.Level
	return nil
}
func (r *recLog) WithAttrs([]slog.Attr) slog.Handler { return r }
func (r *recLog) WithGroup(string) slog.Handler      { return r }
func (r *recLog) count(m string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.msgs[m]
}

const (
	vOld = "0484_library_file_folder.sql"
	vNew = "0486_new.sql"
)

func fleetTeams(pf *fakePlatform, n int, version string) {
	for i := 1; i <= n; i++ {
		db := prefix + string(rune('a'+i-1)) + "aaaaaaa"
		pf.teams[int64(i+1)] = &fakeTeam{slug: "t" + string(rune('a'+i-1)), db: db, status: "active", version: version}
	}
}

// One team's migration fails (data the others never had): every other team is migrated and
// recorded, the failed one keeps its old version, is reported ONCE as an error, waits for its
// backoff, and recovers on a later pass.
func TestFleetOneFailedTeamDoesNotCloseOthers(t *testing.T) {
	pf, sc := newFakePlatform(), &fakeSchema{latest: vNew}
	fleetTeams(pf, 5, vOld)
	bad := pf.teams[4].db
	sc.failOn = map[string]error{bad: errors.New("duplicate entry on their data")}
	w := newWorker(t, pf, sc, 0)
	rec := &recLog{}
	w.SetLoggerForTest(slog.New(rec))
	now := time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)
	w.SetNowForTest(func() time.Time { return now })
	ctx := context.Background()

	if err := w.MigrateFleetOnce(ctx); err == nil {
		t.Fatal("a failed team must surface in the pass error")
	}
	for id, tm := range pf.teams {
		want := vNew
		if tm.db == bad {
			want = vOld
		}
		if tm.version != want {
			t.Fatalf("team %d version %q, want %q", id, tm.version, want)
		}
	}
	set := schemacompat.Set{Latest: vNew, Need: vOld} // vNew declared lazy: the failed team stays open
	if !set.Servable(pf.teams[4].version, pf.teams[4].minCode) {
		t.Fatal("a team behind only a lazy migration must stay open")
	}
	if got := rec.count("team_migrate_failed"); got != 1 {
		t.Fatalf("team_migrate_failed logged %d times, want 1", got)
	}
	if rec.lvl["team_migrate_failed"] != slog.LevelError {
		t.Fatalf("team_migrate_failed level %v", rec.lvl["team_migrate_failed"])
	}

	// Inside the backoff nothing is retried.
	attempts := len(sc.migrated)
	_ = w.MigrateFleetOnce(ctx)
	if len(sc.migrated) != attempts || rec.count("provision: team migration still failing") != 0 {
		t.Fatal("retried inside the backoff")
	}
	// After it: retried, still failing = a warning, not a second error.
	now = now.Add(2 * time.Minute)
	_ = w.MigrateFleetOnce(ctx)
	if rec.count("team_migrate_failed") != 1 || rec.count("provision: team migration still failing") != 1 {
		t.Fatalf("second failure: errors %d warns %d", rec.count("team_migrate_failed"), rec.count("provision: team migration still failing"))
	}
	// Fixed by hand: the next due pass migrates it and says so.
	sc.mu.Lock()
	sc.failOn = nil
	sc.mu.Unlock()
	now = now.Add(5 * time.Minute)
	if err := w.MigrateFleetOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if pf.teams[4].version != vNew || rec.count("team_migrate_recovered") != 1 {
		t.Fatalf("not recovered: version %q, recovered %d", pf.teams[4].version, rec.count("team_migrate_recovered"))
	}
}

// A failed run records what it really reached, so the gate judges the team by its database.
func TestFleetRecordsPartialProgress(t *testing.T) {
	pf, sc := newFakePlatform(), &fakeSchema{latest: vNew}
	fleetTeams(pf, 1, vOld)
	db := pf.teams[2].db
	sc.failOn = map[string]error{db: errors.New("lazy backfill failed")}
	sc.applied = map[string]string{db: "0485_tech_card_recent.sql"}
	w := newWorker(t, pf, sc, 0)
	w.SetLoggerForTest(slog.New(&recLog{}))
	_ = w.MigrateFleetOnce(context.Background())
	if got := pf.teams[2].version; got != "0485_tech_card_recent.sql" {
		t.Fatalf("version %q, want the applied 0485", got)
	}
}

// A rollback binary never touches a database ahead of it.
func TestFleetLeavesTeamsAheadAlone(t *testing.T) {
	pf, sc := newFakePlatform(), &fakeSchema{latest: vOld}
	fleetTeams(pf, 2, vNew)
	w := newWorker(t, pf, sc, 0)
	if err := w.MigrateFleetOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(sc.migrated) != 0 {
		t.Fatalf("migrated a database ahead of the binary: %v", sc.migrated)
	}
}

// The canary runs alone first; the rest never exceed the parallel bound; min code is raised
// before the migration when the target carries a breaking migration.
func TestFleetCanaryThenBoundedParallel(t *testing.T) {
	pf, sc := newFakePlatform(), &fakeSchema{latest: "0487_breaking_drop_x.sql"}
	sc.set = &schemacompat.Set{Latest: "0487_breaking_drop_x.sql", Need: "0487_breaking_drop_x.sql",
		Breaking: []string{"0487_breaking_drop_x.sql"}}
	fleetTeams(pf, 8, vOld)
	var (
		mu       sync.Mutex
		running  int
		peak     int
		order    []string
		minAtRun = map[string]string{}
	)
	sc.onMigrate = func(db string) {
		mu.Lock()
		running++
		peak = max(peak, running)
		order = append(order, db)
		for _, tm := range pf.teams {
			if tm.db == db {
				pf.mu.Lock()
				minAtRun[db] = tm.minCode
				pf.mu.Unlock()
			}
		}
		first := len(order) == 1
		mu.Unlock()
		if first {
			time.Sleep(30 * time.Millisecond) // nobody else may start while the canary runs
		} else {
			time.Sleep(10 * time.Millisecond)
		}
		mu.Lock()
		running--
		mu.Unlock()
	}
	w := newWorker(t, pf, sc, 0)
	if err := w.MigrateFleetOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if order[0] != pf.teams[2].db {
		t.Fatalf("canary %s, want the lowest-id team %s", order[0], pf.teams[2].db)
	}
	if peak > 3 || peak < 2 {
		t.Fatalf("peak parallel %d, want 2..3", peak)
	}
	for db, mc := range minAtRun {
		if mc != "0487_breaking_drop_x.sql" {
			t.Fatalf("%s migrated before its min code was raised (%q)", db, mc)
		}
	}
}

// After fleetBreaker failures no further team starts in that pass.
func TestFleetBreakerStopsLaunching(t *testing.T) {
	pf, sc := newFakePlatform(), &fakeSchema{latest: vNew, failAll: true}
	fleetTeams(pf, 10, vOld)
	w := newWorker(t, pf, sc, 0)
	rec := &recLog{}
	w.SetLoggerForTest(slog.New(rec))
	var mu sync.Mutex
	started := 0
	sc.onMigrate = func(string) { mu.Lock(); started++; mu.Unlock() }
	_ = w.MigrateFleetOnce(context.Background())
	if started >= 10 || rec.count("fleet_breaker_open") != 1 {
		t.Fatalf("started %d of 10, breaker logs %d", started, rec.count("fleet_breaker_open"))
	}
}
