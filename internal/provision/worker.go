// Package provision turns a confirmed sign-up into a working, empty workspace and keeps every team
// database at the binary's migration level (02-DESIGN §8, §17 G8).
//
// Four loops, all in teams mode only:
//   - spare keeper (30 s): keeps Spares ready spare databases at the latest migration, at most
//     PerHour created per hour; a failed spare stays failed for an operator, never dropped;
//   - claimer (2 s): a team in provisioning gets a ready spare (or, with none, one made on the
//     spot inside the worker — never in a request), is seeded and activated, all under the team's
//     lease (one owner per team: only the holder may scrub or activate);
//   - fleet migrator (10 s after boot, then a tick every minute, X3): ready spares, then teams
//     behind the binary — a canary team alone, the rest at most fleetParallel at once — each
//     under its database's lease (one migrator per database across instances); a failed team is
//     retried with backoff and reported once; teams ahead of the binary (a rollback) are never
//     touched; schema fingerprints compared with the reference (G8, warn only); team #1 is
//     migrated synchronously at boot by store.New and only fingerprinted here;
//   - pending sweep (10 min): sign-ups never confirmed are removed, their slug freed.
//
// Every spare database, the keeper's and the cold path's alike, is reserved through
// Platform.ReserveSpare: one control-plane transaction that enforces the hourly cap across all
// workers, failed databases included, and stops creating for a schema version whose last three
// spares failed (breaker).
//
// Leases in the control plane only schedule work. The fence on a team database is its MySQL named
// lock (Schema.Lock), held on a dedicated connection to that database for the whole scrub + seed +
// activation or migration: a holder whose lease expired while it paused still holds the lock, so no
// second worker starts on that database until the first one's connection releases it or dies.
//
// Every database name read from the control plane or generated here passes the allowlist
// (store.NewTeamDBName: TENANT_DB_PREFIX + 8 [a-z0-9], never team #1's database) before it reaches
// CREATE DATABASE, a DSN, a migration or the routing pool.
package provision

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/platformstore"
	"github.com/jekabolt/grbpwr-manager/internal/schemacompat"
	"github.com/jekabolt/grbpwr-manager/internal/store"
	"github.com/jekabolt/grbpwr-manager/internal/tenant"
)

// Defaults of TENANT_SPARES / TENANT_PROVISION_PER_HOUR when unset.
const (
	DefaultSpares   = 2
	DefaultPerHour  = 10
	claimBatch      = 20
	sweepBatch      = 100
	maxBackoff      = 5 * time.Minute
	firstBackoff    = 2 * time.Second
	driftLinesShown = 5
	// DefaultLeaseTTL is how long a lease outlives a holder that stopped renewing it.
	DefaultLeaseTTL = 2 * time.Minute
)

// Intervals of the loops; tests shorten them.
type Intervals struct {
	Keeper, Claim, Fleet, Sweep time.Duration
	// FleetDelay is how long after start the first fleet pass runs.
	FleetDelay time.Duration
}

// DefaultIntervals are the production cadences (B4 spec).
func DefaultIntervals() Intervals {
	return Intervals{
		Keeper:     30 * time.Second,
		Claim:      2 * time.Second,
		Fleet:      time.Minute,
		Sweep:      10 * time.Minute,
		FleetDelay: 10 * time.Second,
	}
}

// Platform is the control-plane surface the worker uses (*platformstore.Store).
type Platform interface {
	ReserveSpare(ctx context.Context, dbName, version string, perHour, keep int) error
	MarkSpareReady(ctx context.Context, dbName, version, fingerprint string) error
	MarkSpareFailed(ctx context.Context, dbName, reason string) error
	SetSpareSchema(ctx context.Context, dbName, token, version, fingerprint string) error
	ReadySpares(ctx context.Context) ([]platformstore.Spare, error)
	ProvisioningTeams(ctx context.Context, limit int) ([]platformstore.ProvisioningTeam, error)
	ClaimSpare(ctx context.Context, teamID int64, version string) (dbName string, claimed bool, err error)
	ActivateTeam(ctx context.Context, teamID int64, token, version, fingerprint string) error
	SetTeamSchema(ctx context.Context, teamID int64, dbName, token, version, fingerprint string) error
	RaiseTeamMinCode(ctx context.Context, teamID int64, minCode string) error
	AcquireLease(ctx context.Context, key string, ttl time.Duration) (token string, ok bool, err error)
	ExtendLease(ctx context.Context, key, token string, ttl time.Duration) error
	ReleaseLease(ctx context.Context, key, token string) error
	CheckProvisionLease(ctx context.Context, teamID int64, dbName, token string) error
	SchemaTeams(ctx context.Context) ([]platformstore.SchemaTeam, error)
	ReferenceSpare(ctx context.Context, version string) (dbName, fingerprint string, ok bool, err error)
	SweepPendingTeams(ctx context.Context, now time.Time, limit int) (int, error)
	Directory() tenant.Directory
}

var _ Platform = (*platformstore.Store)(nil)

// Schema creates, migrates and fingerprints team databases. Names reaching it already passed the
// allowlist.
type Schema interface {
	Create(ctx context.Context, dbName string) error
	// Migrate migrates a database nobody else can reach yet (a spare being created). Every other
	// migration runs through Lock(...).Migrate.
	Migrate(ctx context.Context, dbName string) (version string, err error)
	Lines(ctx context.Context, dbName string) ([]string, error)
	Latest() string
	// Compat is the binary's migration declarations (X3), read once when the worker is built.
	Compat() schemacompat.Set
	// Applied is the newest migration recorded in the database's gorp_migrations ("" = none).
	Applied(ctx context.Context, dbName string) (string, error)
	// Lock takes the database's fence (store.TeamDBLock) without waiting; ok false = held by
	// another worker, nothing held.
	Lock(ctx context.Context, dbName string) (lock DBLock, ok bool, err error)
}

// DBLock is a held fence on one team database (*store.TeamDBLock): scrub, seed and migration of a
// database that a team or the fleet can reach run only under it, the scrub on its own connection.
type DBLock interface {
	Execer
	Migrate(ctx context.Context) (version string, err error)
	// Alive fails once the lock is no longer held.
	Alive(ctx context.Context) error
	Release(ctx context.Context)
}

// StoreSchema is Schema over the store package on the server of BaseDSN (MYSQL_DSN).
type StoreSchema struct {
	BaseDSN, Prefix string
}

func (s StoreSchema) Create(ctx context.Context, dbName string) error {
	return store.CreateTeamDB(ctx, s.BaseDSN, s.Prefix, dbName)
}
func (s StoreSchema) Migrate(ctx context.Context, dbName string) (string, error) {
	return store.MigrateTeam(ctx, s.BaseDSN, dbName)
}
func (s StoreSchema) Lines(ctx context.Context, dbName string) ([]string, error) {
	return store.SchemaLines(ctx, s.BaseDSN, dbName)
}
func (s StoreSchema) Latest() string { return store.LatestMigrationID() }

// Compat reads the declarations; unreadable (the lint refuses that before a build) falls back to
// strict equality: every migration needed, none breaking.
func (s StoreSchema) Compat() schemacompat.Set {
	set, err := store.LoadSchemaSet()
	if err != nil {
		slog.Default().Error("provision: migration declarations unreadable; strict schema gate", slog.String("err", err.Error()))
		l := store.LatestMigrationID()
		return schemacompat.Set{Latest: l, Need: l}
	}
	return set
}
func (s StoreSchema) Applied(ctx context.Context, dbName string) (string, error) {
	return store.AppliedVersion(ctx, s.BaseDSN, dbName)
}
func (s StoreSchema) Lock(ctx context.Context, dbName string) (DBLock, bool, error) {
	l, ok, err := store.LockTeamDB(ctx, s.BaseDSN, s.Prefix, dbName)
	if err != nil || !ok {
		return nil, false, err
	}
	return l, true, nil
}

// Config configures the worker.
type Config struct {
	Prefix  string // TENANT_DB_PREFIX
	Team1DB string // team #1's database (only fingerprinted, never created, migrated or seeded here)
	Spares  int    // TENANT_SPARES (0 = DefaultSpares)
	PerHour int    // TENANT_PROVISION_PER_HOUR (0 = DefaultPerHour)
	Every   Intervals
	// LeaseTTL is the provisioning / migration lease lifetime (0 = DefaultLeaseTTL); the holder
	// renews it every third of that while it works.
	LeaseTTL time.Duration
}

// Worker is the provisioner.
type Worker struct {
	cfg    Config
	pf     Platform
	schema Schema
	db     dependency.DB
	now    func() time.Time
	log    *slog.Logger
	// seed seeds the team in ctx through exec (the database lock's session) under guard; Seed
	// unless a test replaces it.
	seed func(ctx context.Context, exec Execer, ownerHandle string, guard func(context.Context) error) error

	mu      sync.Mutex
	backoff map[int64]retry  // team id → next attempt
	set     schemacompat.Set // the binary's migration declarations (X3)

	// fleet (X3): per-team retry after a failed migration, the target version whose failure was
	// already reported as an error (one event per team per version), the last team #1 fingerprint.
	fleetRetry  map[int64]retry
	fleetFailed map[int64]string
	teamOneAt   time.Time
	capLog      time.Time // last "cap reached" log
	tripLog     time.Time // last "spare breaker open" log
}

type retry struct {
	next  time.Time
	delay time.Duration
}

// CheckPrefix refuses a TENANT_DB_PREFIX that is empty, outside [a-z0-9_]{1,56}, or whose new-team
// pattern could match team #1's database.
func CheckPrefix(prefix, team1DB string) error {
	if err := store.NewTeamDBName(prefix, team1DB, prefix+"aaaaaaaa"); err != nil {
		return fmt.Errorf("provision: TENANT_DB_PREFIX: %w", err)
	}
	if strings.HasPrefix(team1DB, prefix) && len(team1DB) == len(prefix)+8 {
		// Team #1's database would match the new-team pattern: refuse rather than risk it.
		return fmt.Errorf("provision: team #1's database %q looks like a new-team database of prefix %q", team1DB, prefix)
	}
	return nil
}

// New builds the worker. db is the routing pool (team in ctx); every write to a team database
// being provisioned goes through that database's lock instead.
func New(cfg Config, pf Platform, schema Schema, db dependency.DB) (*Worker, error) {
	if err := CheckPrefix(cfg.Prefix, cfg.Team1DB); err != nil {
		return nil, err
	}
	if cfg.Spares <= 0 {
		cfg.Spares = DefaultSpares
	}
	if cfg.PerHour <= 0 {
		cfg.PerHour = DefaultPerHour
	}
	if cfg.Every == (Intervals{}) {
		cfg.Every = DefaultIntervals()
	}
	if cfg.LeaseTTL <= 0 {
		cfg.LeaseTTL = DefaultLeaseTTL
	}
	if pf == nil || schema == nil || db == nil {
		return nil, errors.New("provision: missing dependency")
	}
	w := &Worker{
		cfg:     cfg,
		pf:      pf,
		schema:  schema,
		db:      db,
		now:     time.Now,
		log:     slog.Default(),
		backoff: map[int64]retry{},
		set:     schema.Compat(),

		fleetRetry:  map[int64]retry{},
		fleetFailed: map[int64]string{},
	}
	w.seed = func(ctx context.Context, exec Execer, owner string, guard func(context.Context) error) error {
		return Seed(ctx, exec, owner, guard)
	}
	return w, nil
}

// Start runs the loops until stop is called or ctx ends. stop waits for them to return.
func (w *Worker) Start(ctx context.Context) (stop func()) {
	ctx, cancel := context.WithCancel(ctx)
	var wg sync.WaitGroup
	loop := func(name string, first, every time.Duration, fn func(context.Context)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			t := time.NewTimer(first)
			defer t.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-t.C:
				}
				w.safe(ctx, name, fn)
				t.Reset(every)
			}
		}()
	}
	loop("claim", 0, w.cfg.Every.Claim, func(ctx context.Context) { _ = w.ClaimOnce(ctx) })
	loop("keeper", 0, w.cfg.Every.Keeper, func(ctx context.Context) { _ = w.KeepSparesOnce(ctx) })
	loop("fleet", w.cfg.Every.FleetDelay, w.cfg.Every.Fleet, func(ctx context.Context) { _ = w.MigrateFleetOnce(ctx) })
	loop("sweep", w.cfg.Every.Sweep, w.cfg.Every.Sweep, func(ctx context.Context) { _ = w.SweepOnce(ctx) })
	return func() {
		cancel()
		wg.Wait()
	}
}

// safe runs one tick; a panic is logged, never kills the process.
func (w *Worker) safe(ctx context.Context, name string, fn func(context.Context)) {
	defer func() {
		if r := recover(); r != nil {
			w.log.ErrorContext(ctx, "provision: loop panicked", slog.String("loop", name), slog.Any("panic", r))
		}
	}()
	fn(ctx)
}

// allowed is the allowlist every team / spare database name passes before any use.
func (w *Worker) allowed(dbName string) error {
	return store.NewTeamDBName(w.cfg.Prefix, w.cfg.Team1DB, dbName)
}

// ─── spare keeper ──────────────────────────────────────────────────────────────────────────────

// KeepSparesOnce creates at most one spare when fewer than Spares are ready at the latest
// migration (or being created) and the shared hourly cap allows.
func (w *Worker) KeepSparesOnce(ctx context.Context) error {
	_, err := w.makeSpare(ctx, w.cfg.Spares)
	switch {
	case errors.Is(err, platformstore.ErrEnoughSpares):
		return nil
	case errors.Is(err, platformstore.ErrSpareQuota):
		w.capReached(ctx)
		return nil
	case errors.Is(err, platformstore.ErrSpareBreaker):
		w.breakerOpen(ctx)
		return nil
	}
	return err
}

// breakerOpen logs the open spare breaker at most every 10 minutes.
func (w *Worker) breakerOpen(ctx context.Context) {
	w.mu.Lock()
	quiet := !w.tripLog.IsZero() && w.now().Sub(w.tripLog) < 10*time.Minute
	if !quiet {
		w.tripLog = w.now()
	}
	w.mu.Unlock()
	if !quiet {
		w.log.WarnContext(ctx, "provision: the last spare databases of this version all failed; no new database until a new version or the failed spares are cleared",
			slog.String("version", w.schema.Latest()), slog.Int("failed_in_a_row", platformstore.SpareBreakerFailures))
	}
}

// capReached logs the hourly cap at most every 10 minutes.
func (w *Worker) capReached(ctx context.Context) {
	w.mu.Lock()
	quiet := !w.capLog.IsZero() && w.now().Sub(w.capLog) < 10*time.Minute
	if !quiet {
		w.capLog = w.now()
	}
	w.mu.Unlock()
	if !quiet {
		w.log.WarnContext(ctx, "provision: hourly database creation cap reached; spares wait", slog.Int("cap", w.cfg.PerHour))
	}
}

// makeSpare reserves (under the shared hourly cap and the failure breaker; keep > 0 = only while
// fewer than keep spares are ready or being created), creates, migrates and fingerprints one spare
// database. ErrSpareQuota / ErrEnoughSpares / ErrSpareBreaker come back unwrapped.
func (w *Worker) makeSpare(ctx context.Context, keep int) (string, error) {
	latest := w.schema.Latest()
	if latest == "" {
		return "", errors.New("provision: no embedded migrations")
	}
	var name string
	var err error
	for range 3 { // a random-name collision with an existing row is retried with a new name
		if name, err = NewDBName(w.cfg.Prefix, w.cfg.Team1DB); err != nil {
			return "", err
		}
		err = w.pf.ReserveSpare(ctx, name, latest, w.cfg.PerHour, keep)
		if err == nil || reserveRefused(err) {
			break
		}
	}
	if err != nil {
		if !reserveRefused(err) {
			w.log.ErrorContext(ctx, "provision: reserve spare", slog.String("err", err.Error()))
		}
		return "", err
	}
	fail := func(stage string, err error) (string, error) {
		w.log.ErrorContext(ctx, "provision: spare failed; left for an operator",
			slog.String("db", name), slog.String("stage", stage), slog.String("err", err.Error()))
		// The spare row must say failed even when ctx ended (shutdown mid-create).
		mctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		if merr := w.pf.MarkSpareFailed(mctx, name, stage+": "+err.Error()); merr != nil {
			w.log.ErrorContext(ctx, "provision: mark spare failed", slog.String("db", name), slog.String("err", merr.Error()))
		}
		return "", fmt.Errorf("provision: spare %s %s: %w", name, stage, err)
	}
	if err := w.allowed(name); err != nil {
		return fail("allowlist", err)
	}
	start := w.now()
	if err := w.schema.Create(ctx, name); err != nil {
		return fail("create", err)
	}
	version, err := w.schema.Migrate(ctx, name)
	if err != nil {
		return fail("migrate", err)
	}
	lines, err := w.schema.Lines(ctx, name)
	if err != nil {
		return fail("fingerprint", err)
	}
	if err := w.pf.MarkSpareReady(ctx, name, version, store.FingerprintOf(lines)); err != nil {
		return fail("ready", err)
	}
	w.log.InfoContext(ctx, "provision: spare ready",
		slog.String("db", name), slog.String("version", version), slog.Duration("took", w.now().Sub(start)))
	return name, nil
}

// reserveRefused: ReserveSpare said no (cap, enough spares, breaker), not a failure.
func reserveRefused(err error) bool {
	return errors.Is(err, platformstore.ErrSpareQuota) || errors.Is(err, platformstore.ErrEnoughSpares) ||
		errors.Is(err, platformstore.ErrSpareBreaker)
}

// ─── leases ────────────────────────────────────────────────────────────────────────────────────

// withLease runs fn while this worker holds key. held is false (fn not run) when another worker
// holds it. The lease is renewed every LeaseTTL/3; if it is lost (or cannot be renewed for a whole
// TTL) fn's ctx is cancelled with platformstore.ErrLeaseLost, and the guarded writes refuse anyway.
// The lease is released when fn returns.
func (w *Worker) withLease(ctx context.Context, key string, fn func(ctx context.Context, token string) error) (held bool, err error) {
	ttl := w.cfg.LeaseTTL
	token, ok, err := w.pf.AcquireLease(ctx, key, ttl)
	if err != nil {
		return false, err
	}
	if !ok {
		return false, nil
	}
	lctx, cancel := context.WithCancelCause(ctx)
	stopped := make(chan struct{})
	var renewer sync.WaitGroup
	renewer.Add(1)
	go func() {
		defer renewer.Done()
		tick := time.NewTicker(ttl / 3)
		defer tick.Stop()
		lastOK := time.Now()
		for {
			select {
			case <-stopped:
				return
			case <-lctx.Done():
				return
			case <-tick.C:
			}
			err := w.pf.ExtendLease(lctx, key, token, ttl)
			switch {
			case err == nil:
				lastOK = time.Now()
			case errors.Is(err, platformstore.ErrLeaseLost) || time.Since(lastOK) >= ttl:
				w.log.ErrorContext(ctx, "provision: lease lost; stopping", slog.String("lease", key), slog.String("err", err.Error()))
				cancel(platformstore.ErrLeaseLost)
				return
			default:
				w.log.WarnContext(ctx, "provision: lease renewal failed; retrying", slog.String("lease", key), slog.String("err", err.Error()))
			}
		}
	}()
	defer func() {
		close(stopped)
		renewer.Wait()
		cancel(nil)
		rctx, rcancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer rcancel()
		if rerr := w.pf.ReleaseLease(rctx, key, token); rerr != nil {
			w.log.WarnContext(ctx, "provision: release lease", slog.String("lease", key), slog.String("err", rerr.Error()))
		}
	}()
	err = fn(lctx, token)
	if err != nil && context.Cause(lctx) == platformstore.ErrLeaseLost {
		err = fmt.Errorf("%w: %w", platformstore.ErrLeaseLost, err)
	}
	return true, err
}

// ─── claim + seed ──────────────────────────────────────────────────────────────────────────────

// ClaimOnce provisions every team waiting in provisioning whose retry time has come.
func (w *Worker) ClaimOnce(ctx context.Context) error {
	teams, err := w.pf.ProvisioningTeams(ctx, claimBatch)
	if err != nil {
		w.log.ErrorContext(ctx, "provision: list provisioning teams", slog.String("err", err.Error()))
		return err
	}
	var errs []error
	for _, t := range teams {
		if ctx.Err() != nil {
			break
		}
		if !w.due(t.ID) {
			continue
		}
		if err := w.ProvisionTeam(ctx, t); err != nil {
			delay := w.fail(t.ID)
			w.log.ErrorContext(ctx, "provision: team not ready yet; will retry",
				slog.Int64("tid", t.ID), slog.String("slug", t.Slug),
				slog.Duration("retry_in", delay), slog.String("err", err.Error()))
			errs = append(errs, err)
			continue
		}
		w.clear(t.ID)
	}
	return errors.Join(errs...)
}

// ProvisionTeam claims (or makes) the team's database, seeds it and activates the team. Safe to
// rerun at any step. Everything runs under the team's lease: a team another worker is provisioning
// is skipped (nil). t comes from a listing and may be stale: only t.ID, t.Slug and t.OwnerHandle
// are used; the database comes from a fresh, locked read of the team row (ClaimSpare).
func (w *Worker) ProvisionTeam(ctx context.Context, t platformstore.ProvisioningTeam) error {
	if t.ID == tenant.TeamOneID {
		return errors.New("provision: team #1 is never provisioned")
	}
	held, err := w.withLease(ctx, platformstore.TeamLeaseKey(t.ID), func(ctx context.Context, token string) error {
		return w.provision(ctx, t, token)
	})
	if err == nil && !held {
		w.log.DebugContext(ctx, "provision: team held by another worker", slog.Int64("tid", t.ID))
	}
	return err
}

// provision runs under the team lease token.
func (w *Worker) provision(ctx context.Context, t platformstore.ProvisioningTeam, token string) error {
	latest := w.schema.Latest()
	dbName, claimed, err := w.pf.ClaimSpare(ctx, t.ID, latest)
	if errors.Is(err, platformstore.ErrNoSpare) {
		// Cold path, here in the worker: make one spare now (under the shared hourly cap), then
		// claim (whichever ready spare the claim hands out — they are all equal).
		w.log.WarnContext(ctx, "provision: no ready spare; creating a database for the team",
			slog.Int64("tid", t.ID), slog.String("slug", t.Slug))
		if _, err = w.makeSpare(ctx, 0); err != nil {
			if errors.Is(err, platformstore.ErrSpareQuota) {
				w.capReached(ctx)
			}
			if errors.Is(err, platformstore.ErrSpareBreaker) {
				w.breakerOpen(ctx)
			}
			return fmt.Errorf("cold path: %w", err)
		}
		dbName, claimed, err = w.pf.ClaimSpare(ctx, t.ID, latest)
	}
	if errors.Is(err, platformstore.ErrNotProvisioning) {
		return nil // activated, suspended or gone meanwhile: nothing was touched
	}
	if err != nil {
		return fmt.Errorf("claim: %w", err)
	}
	if err := w.allowed(dbName); err != nil {
		return fmt.Errorf("team database refused: %w", err)
	}
	held, err := w.withLease(ctx, platformstore.DBLeaseKey(dbName), func(ctx context.Context, _ string) error {
		return w.seedAndActivate(ctx, t, token, dbName, claimed)
	})
	if err == nil && !held {
		return fmt.Errorf("database %s is being migrated by another worker", dbName)
	}
	return err
}

// seedAndActivate brings the team's database forward (unless just claimed at latest), seeds it and
// activates the team; it holds the team lease token and the database's lease. All of it runs under
// the database's fence (Schema.Lock): the control row is re-read inside the lock before anything
// touches the database, the scrub runs on the lock's own connection, and the team is activated
// before the lock is released. A worker whose lease expired while it paused can therefore never
// scrub a team another worker activated meanwhile: that worker needed the lock to seed, and when
// the paused one gets it back, the re-read finds the team no longer provisioning.
func (w *Worker) seedAndActivate(ctx context.Context, t platformstore.ProvisioningTeam, token, dbName string, claimed bool) error {
	lock, ok, err := w.schema.Lock(ctx, dbName)
	if err != nil {
		return fmt.Errorf("lock database: %w", err)
	}
	if !ok {
		return fmt.Errorf("database %s is locked by another worker", dbName)
	}
	defer lock.Release(ctx)
	guard := func(ctx context.Context) error {
		if err := w.pf.CheckProvisionLease(ctx, t.ID, dbName, token); err != nil {
			return err
		}
		return lock.Alive(ctx)
	}
	if err := guard(ctx); err != nil {
		if errors.Is(err, platformstore.ErrNotProvisioning) {
			return nil // moved on before the lock was ours: nothing touched
		}
		return err
	}
	version := w.schema.Latest()
	if !claimed {
		// A rerun after a crash, possibly by an older binary: bring the database forward first.
		if version, err = lock.Migrate(ctx); err != nil {
			return fmt.Errorf("migrate: %w", err)
		}
	}
	dir := w.pf.Directory()
	dir.Invalidate(t.ID)
	tctx, err := tenant.Pinned(ctx, dir, t.ID)
	if err != nil {
		return err
	}
	if got, _ := tenant.From(tctx); got.DBName != dbName {
		return fmt.Errorf("directory says database %q, claim said %q", got.DBName, dbName)
	}
	if err := w.seed(tctx, lock, t.OwnerHandle, guard); err != nil {
		return fmt.Errorf("seed: %w", err)
	}
	lines, err := w.schema.Lines(ctx, dbName)
	if err != nil {
		return fmt.Errorf("fingerprint: %w", err)
	}
	if err := lock.Alive(ctx); err != nil {
		return err
	}
	// X3: record the newest breaking migration the database carries before the team is served, so
	// an older binary never serves a schema it predates (dormant until a breaking migration ships).
	if err := w.pf.RaiseTeamMinCode(ctx, t.ID, w.set.MinCodeAt(version)); err != nil {
		return fmt.Errorf("min code: %w", err)
	}
	if err := w.pf.ActivateTeam(ctx, t.ID, token, version, store.FingerprintOf(lines)); err != nil {
		return fmt.Errorf("activate: %w", err)
	}
	dir.Invalidate(t.ID)
	w.log.InfoContext(ctx, "provision: team active",
		slog.Int64("tid", t.ID), slog.String("slug", t.Slug), slog.String("version", version))
	return nil
}

func (w *Worker) due(id int64) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	r, ok := w.backoff[id]
	return !ok || !w.now().Before(r.next)
}

func (w *Worker) fail(id int64) time.Duration {
	w.mu.Lock()
	defer w.mu.Unlock()
	r := w.backoff[id]
	if r.delay == 0 {
		r.delay = firstBackoff
	} else if r.delay *= 2; r.delay > maxBackoff {
		r.delay = maxBackoff
	}
	r.next = w.now().Add(r.delay)
	w.backoff[id] = r
	return r.delay
}

func (w *Worker) clear(id int64) {
	w.mu.Lock()
	defer w.mu.Unlock()
	delete(w.backoff, id)
}

// ─── pending sweep ─────────────────────────────────────────────────────────────────────────────

// SweepOnce removes expired unconfirmed sign-ups.
func (w *Worker) SweepOnce(ctx context.Context) error {
	n, err := w.pf.SweepPendingTeams(ctx, w.now(), sweepBatch)
	if err != nil {
		w.log.ErrorContext(ctx, "provision: pending sweep", slog.String("err", err.Error()))
		return err
	}
	if n > 0 {
		w.log.InfoContext(ctx, "provision: expired sign-ups removed", slog.Int("teams", n))
	}
	return nil
}
