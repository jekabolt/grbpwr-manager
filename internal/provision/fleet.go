package provision

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/observability"

	"github.com/jekabolt/grbpwr-manager/internal/platformstore"
	"github.com/jekabolt/grbpwr-manager/internal/schemacompat"
	"github.com/jekabolt/grbpwr-manager/internal/store"
)

// Fleet tuning (X3).
const (
	// fleetParallel bounds team migrations running at once: one small managed cluster.
	fleetParallel = 3
	// fleetBreaker: after this many failed teams in one pass no further team is started.
	fleetBreaker      = 3
	fleetFirstBackoff = time.Minute
	fleetMaxBackoff   = 30 * time.Minute
	// teamOneCheckEvery spaces team #1's fingerprint check: the fleet ticks every minute.
	teamOneCheckEvery = 30 * time.Minute
)

// MigrateFleetOnce brings every active / suspended team (and every deleted one whose wipe is still
// ahead, so a restore finds it current) and every ready spare whose schema_version is behind the
// binary's latest forward, then checks its fingerprint against the reference (G8). X3:
//   - spares first (an empty database proves the chain), then a canary team alone (the lowest
//     id), then the rest at most fleetParallel at once; after fleetBreaker failures in a pass no
//     further team starts;
//   - a database AHEAD of the binary (a rollback) is never touched: it is served while it carries
//     no breaking migration this binary predates (schemacompat);
//   - a failed team is isolated: its real progress is recorded, it is retried with backoff
//     (fleetFirstBackoff doubling to fleetMaxBackoff), and reported as one error
//     observability.MsgTeamStuckAfterMigration per team per target version per process (the retry
//     state is in memory: a restart or another instance reports again); it keeps serving while its
//     version is still servable.
//
// Team #1 is never migrated here (store.New does it at boot); it is only fingerprinted, every
// teamOneCheckEvery. Each database is migrated under its lease (DBLeaseKey, scheduling) and its
// lock (Schema.Lock, the fence held on a connection for the whole run), the lease taken in a short
// control-plane statement, never a transaction held across the migration; a database whose lease
// another instance holds is skipped this pass.
func (w *Worker) MigrateFleetOnce(ctx context.Context) error {
	_, err := w.fleetPass(ctx)
	return err
}

// FleetPass is what one fleet pass did to teams (slugs, never ids).
type FleetPass struct {
	Migrated, Failed []string
	Skipped          int // another worker held the database
}

func (w *Worker) fleetPass(ctx context.Context) (FleetPass, error) {
	var pass FleetPass
	latest := w.schema.Latest()
	if latest == "" {
		return pass, errors.New("provision: no embedded migrations")
	}
	var errs []error

	spares, err := w.pf.ReadySpares(ctx)
	if err != nil {
		w.log.ErrorContext(ctx, "provision: list ready spares", slog.String("err", err.Error()))
		errs = append(errs, err)
	}
	for _, sp := range spares {
		if ctx.Err() != nil {
			return pass, ctx.Err()
		}
		if sp.SchemaVersion.String == latest || w.set.Ahead(sp.SchemaVersion.String) {
			continue
		}
		if err := w.migrateSpare(ctx, sp.DBName); err != nil {
			errs = append(errs, err)
		}
	}

	teams, err := w.pf.SchemaTeams(ctx)
	if err != nil {
		w.log.ErrorContext(ctx, "provision: list teams", slog.String("err", err.Error()))
		return pass, errors.Join(append(errs, err)...)
	}
	var todo []platformstore.SchemaTeam
	for _, t := range teams {
		v := t.SchemaVersion.String
		if v == latest || w.set.Ahead(v) || !w.fleetDue(t.ID) {
			continue
		}
		todo = append(todo, t)
	}
	sort.Slice(todo, func(i, j int) bool { return todo[i].ID < todo[j].ID })
	errs = append(errs, w.migrateTeams(ctx, todo, &pass)...)

	w.checkTeamOne(ctx, latest)
	return pass, errors.Join(errs...)
}

// migrateTeams runs the canary (todo[0]) alone, then the rest at most fleetParallel at once,
// starting no new team after fleetBreaker failures or once ctx ends.
func (w *Worker) migrateTeams(ctx context.Context, todo []platformstore.SchemaTeam, pass *FleetPass) []error {
	if len(todo) == 0 {
		return nil
	}
	var (
		mu     sync.Mutex
		errs   []error
		failed int
	)
	run := func(t platformstore.SchemaTeam) {
		err := w.migrateTeam(ctx, t)
		mu.Lock()
		defer mu.Unlock()
		switch {
		case errors.Is(err, errSkipped):
			pass.Skipped++
		case err != nil:
			errs = append(errs, err)
			pass.Failed = append(pass.Failed, t.Slug)
			failed++
		default:
			pass.Migrated = append(pass.Migrated, t.Slug)
		}
	}
	run(todo[0]) // canary
	sem := make(chan struct{}, fleetParallel)
	var wg sync.WaitGroup
	for _, t := range todo[1:] {
		if ctx.Err() != nil {
			break
		}
		mu.Lock()
		nFailed := failed
		mu.Unlock()
		if nFailed >= fleetBreaker {
			w.log.WarnContext(ctx, "fleet_breaker_open", slog.Int("failed", nFailed), slog.Int("left", len(todo)),
				slog.String("version", w.schema.Latest()))
			break
		}
		sem <- struct{}{}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			run(t)
		}()
	}
	wg.Wait()
	if err := ctx.Err(); err != nil {
		errs = append(errs, err)
	}
	return errs
}

// fleetDue: no failed attempt of this team waits for its retry time.
func (w *Worker) fleetDue(id int64) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	r, ok := w.fleetRetry[id]
	return !ok || !w.now().Before(r.next)
}

// fleetResult books one team's outcome: success clears its backoff (and says it recovered after a
// reported failure); a failure doubles it and is reported as an error only once per version.
func (w *Worker) fleetResult(ctx context.Context, t platformstore.SchemaTeam, err error) {
	latest := w.schema.Latest()
	w.mu.Lock()
	reported := w.fleetFailed[t.ID]
	if err == nil {
		delete(w.fleetRetry, t.ID)
		delete(w.fleetFailed, t.ID)
	} else {
		d := fleetFirstBackoff
		if r, ok := w.fleetRetry[t.ID]; ok {
			d = min(r.delay*2, fleetMaxBackoff)
		}
		w.fleetRetry[t.ID] = retry{next: w.now().Add(d), delay: d}
		w.fleetFailed[t.ID] = latest
	}
	w.mu.Unlock()
	switch {
	case err == nil && reported != "":
		w.log.InfoContext(ctx, "team_migrate_recovered", slog.String("team", t.Slug),
			slog.String("version", latest))
	case err != nil && reported != latest:
		w.log.ErrorContext(ctx, observability.MsgTeamStuckAfterMigration, slog.String("team", t.Slug),
			slog.String("err", err.Error()), slog.String("version", latest))
	case err != nil:
		w.log.WarnContext(ctx, "provision: team migration still failing", slog.String("team", t.Slug),
			slog.String("version", latest), slog.String("err", err.Error()))
	}
}

// migrateSpare migrates one ready spare under its database lease; held elsewhere = skipped.
func (w *Worker) migrateSpare(ctx context.Context, dbName string) error {
	if err := w.allowed(dbName); err != nil {
		w.log.ErrorContext(ctx, "provision: spare name refused", slog.String("db", dbName), slog.String("err", err.Error()))
		return err
	}
	held, err := w.withLease(ctx, platformstore.DBLeaseKey(dbName), func(ctx context.Context, token string) error {
		lock, ok, err := w.schema.Lock(ctx, dbName)
		if err != nil {
			return fmt.Errorf("provision: lock spare %s: %w", dbName, err)
		}
		if !ok {
			w.log.InfoContext(ctx, "provision: spare locked by another worker; skipped", slog.String("db", dbName))
			return nil
		}
		defer lock.Release(ctx)
		version, err := lock.Migrate(ctx)
		if err != nil {
			w.log.ErrorContext(ctx, "spare_migrate_failed", slog.String("db", dbName), slog.String("err", err.Error()))
			return err
		}
		lines, err := w.schema.Lines(ctx, dbName)
		if err != nil {
			return fmt.Errorf("provision: fingerprint spare %s: %w", dbName, err)
		}
		return w.pf.SetSpareSchema(ctx, dbName, token, version, store.FingerprintOf(lines))
	})
	if err == nil && !held {
		w.log.InfoContext(ctx, "provision: spare migrated elsewhere; skipped", slog.String("db", dbName))
	}
	return err
}

// migrateTeam migrates one team database under its lease; held elsewhere = skipped. The schema
// version is written only while the lease is still this worker's. X3: the team's min code is
// raised to the newest breaking migration of the target BEFORE the run (an older binary stops
// serving it before its schema changes under it), and a failed run records what it really
// reached (gorp_migrations), so a team stuck on a lazy migration keeps serving.
func (w *Worker) migrateTeam(ctx context.Context, t platformstore.SchemaTeam) (err error) {
	id, slug, dbName := t.ID, t.Slug, t.DBName
	defer func() {
		if !errors.Is(err, errSkipped) { // skipped: neither a success nor a failure; the caller counts it
			w.fleetResult(ctx, t, err)
		}
	}()
	if err := w.allowed(dbName); err != nil {
		return fmt.Errorf("database name refused: %w", err)
	}
	held, err := w.withLease(ctx, platformstore.DBLeaseKey(dbName), func(ctx context.Context, token string) error {
		// The lease only schedules; the database lock is the fence: a second migrator (one that took
		// an expired lease) cannot start while this one's lock connection lives.
		lock, ok, err := w.schema.Lock(ctx, dbName)
		if err != nil {
			return fmt.Errorf("provision: lock team %s: %w", slug, err)
		}
		if !ok {
			w.log.InfoContext(ctx, "provision: team database locked by another worker; skipped", slog.String("slug", slug))
			return errSkipped
		}
		defer lock.Release(ctx)
		if mc := w.set.MinCodeAt(w.schema.Latest()); schemacompat.Raise(t.SchemaMinCode.String, mc) != t.SchemaMinCode.String {
			if err := w.pf.RaiseTeamMinCode(ctx, id, mc); err != nil {
				return fmt.Errorf("provision: min code of %s: %w", slug, err)
			}
		}
		version, err := lock.Migrate(ctx)
		if err != nil {
			w.recordProgress(ctx, t, token)
			return err
		}
		lines, err := w.schema.Lines(ctx, dbName)
		if err != nil {
			// Migrated but not fingerprinted: record the version (the team serves again) without a
			// fingerprint; the console shows it unknown.
			w.log.ErrorContext(ctx, "provision: fingerprint team", slog.String("slug", slug), slog.String("err", err.Error()))
			_ = w.pf.SetTeamSchema(ctx, id, dbName, token, version, "")
			return err
		}
		if err := w.pf.SetTeamSchema(ctx, id, dbName, token, version, store.FingerprintOf(lines)); err != nil {
			return err
		}
		w.log.InfoContext(ctx, "provision: team migrated", slog.String("slug", slug), slog.String("version", version))
		w.compare(ctx, slug, version, lines)
		return nil
	})
	if err == nil && !held {
		w.log.InfoContext(ctx, "provision: team migrated elsewhere; skipped", slog.String("slug", slug))
		return errSkipped
	}
	return err
}

// errSkipped: another worker has the database; neither a success nor a failure of this one.
var errSkipped = errors.New("provision: skipped")

// recordProgress writes the version a failed run really reached, when it is newer than the
// recorded one (fingerprint unknown): partial progress is never lost, and the gate judges the
// team by what its database holds. Best effort: a failure here only keeps the older version.
func (w *Worker) recordProgress(ctx context.Context, t platformstore.SchemaTeam, token string) {
	applied, err := w.schema.Applied(ctx, t.DBName)
	if err != nil || applied == "" || schemacompat.Compare(applied, t.SchemaVersion.String) <= 0 || w.set.Ahead(applied) {
		return
	}
	if err := w.pf.SetTeamSchema(ctx, t.ID, t.DBName, token, applied, ""); err != nil {
		w.log.WarnContext(ctx, "provision: record partial migration", slog.String("slug", t.Slug), slog.String("err", err.Error()))
	}
}

// checkTeamOne fingerprints team #1 and compares it with the reference (warn only, never fatal).
func (w *Worker) checkTeamOne(ctx context.Context, latest string) {
	if w.cfg.Team1DB == "" {
		return
	}
	w.mu.Lock()
	if !w.teamOneAt.IsZero() && w.now().Sub(w.teamOneAt) < teamOneCheckEvery {
		w.mu.Unlock()
		return
	}
	w.teamOneAt = w.now()
	w.mu.Unlock()
	lines, err := w.schema.Lines(ctx, w.cfg.Team1DB)
	if err != nil {
		w.log.WarnContext(ctx, "provision: fingerprint team #1", slog.String("err", err.Error()))
		return
	}
	w.compare(ctx, "grbpwr", latest, lines)
}

// compare is G8: the database's schema lines against the reference spare at that version. A
// mismatch is a WARN schema_drift with the first differing lines; no reference yet = no verdict.
func (w *Worker) compare(ctx context.Context, slug, version string, lines []string) {
	refDB, refFP, ok, err := w.pf.ReferenceSpare(ctx, version)
	if err != nil {
		w.log.WarnContext(ctx, "provision: reference fingerprint", slog.String("err", err.Error()))
		return
	}
	if !ok || refFP == store.FingerprintOf(lines) {
		return
	}
	attrs := []any{slog.String("slug", slug), slog.String("version", version), slog.String("reference_db", refDB)}
	if err := w.allowed(refDB); err == nil {
		if want, err := w.schema.Lines(ctx, refDB); err == nil {
			attrs = append(attrs, slog.Any("first_diff", store.FirstSchemaDiff(want, lines, driftLinesShown)))
		}
	}
	w.log.WarnContext(ctx, "schema_drift", attrs...)
}

// FleetReport is the outcome of MigrateFleet (the pre-deploy migrate-fleet command).
type FleetReport struct {
	Latest   string
	Migrated []string // migrated in this run
	Failed   []string // failed in this run and still behind (each reported once)
	Behind   int      // teams still below Latest when the run ended (failed, locked elsewhere, out of time)
	Passes   int
}

// MigrateFleet runs fleet passes until a pass migrates nothing new (every team current, failed and
// waiting for its backoff, or held by another worker) or ctx ends, then counts the teams still
// behind. Same canary, parallelism, breaker and leases as the background fleet: it is meant to run
// with the NEW binary before a deploy, so teams are current when the new code starts serving. A
// failed team never stops the others; err is only for a run that could not happen (control plane
// unreadable, no embedded migrations).
func (w *Worker) MigrateFleet(ctx context.Context) (FleetReport, error) {
	rep := FleetReport{Latest: w.schema.Latest()}
	if rep.Latest == "" {
		return rep, errors.New("provision: no embedded migrations")
	}
	failed := map[string]bool{}
	for ctx.Err() == nil {
		pass, err := w.fleetPass(ctx)
		rep.Passes++
		rep.Migrated = append(rep.Migrated, pass.Migrated...)
		for _, s := range pass.Migrated {
			delete(failed, s)
		}
		for _, s := range pass.Failed {
			failed[s] = true
		}
		if len(pass.Migrated) == 0 && len(pass.Failed) == 0 {
			if err != nil && pass.Skipped == 0 && ctx.Err() == nil {
				// Nothing started and the pass errored: the control plane itself (no team to blame).
				if _, lerr := w.pf.SchemaTeams(ctx); lerr != nil {
					return rep, lerr
				}
			}
			break
		}
		if len(pass.Migrated) == 0 && pass.Skipped == 0 {
			break // only failures: the rest wait for their backoff
		}
	}
	for s := range failed {
		rep.Failed = append(rep.Failed, s)
	}
	sort.Strings(rep.Failed)
	// Teams still behind: a fresh read (the fleet state above is this process's view only).
	rctx := context.WithoutCancel(ctx)
	teams, err := w.pf.SchemaTeams(rctx)
	if err != nil {
		return rep, err
	}
	for _, t := range teams {
		if v := t.SchemaVersion.String; v != rep.Latest && !w.set.Ahead(v) {
			rep.Behind++
		}
	}
	return rep, nil
}
