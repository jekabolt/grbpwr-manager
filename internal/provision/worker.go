// Package provision turns a confirmed sign-up into a working, empty workspace and keeps every team
// database at the binary's migration level (02-DESIGN §8, §17 G8).
//
// Four loops, all in teams mode only:
//   - spare keeper (30 s): keeps Spares ready spare databases at the latest migration, at most
//     PerHour created per hour; a failed spare stays failed for an operator, never dropped;
//   - claimer (2 s): a team in provisioning gets a ready spare (or, with none, one made on the
//     spot inside the worker — never in a request), is seeded and activated;
//   - fleet migrator (after boot, then every 30 min): active and suspended teams and ready spares
//     behind the binary are migrated one at a time, their schema fingerprint compared with the
//     reference (G8, warn only); team #1 is migrated synchronously at boot by store.New and only
//     fingerprinted here;
//   - pending sweep (10 min): sign-ups never confirmed are removed, their slug freed.
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
		Fleet:      30 * time.Minute,
		Sweep:      10 * time.Minute,
		FleetDelay: 10 * time.Second,
	}
}

// Platform is the control-plane surface the worker uses (*platformstore.Store).
type Platform interface {
	CountSpares(ctx context.Context, version string, since time.Time) (platformstore.SpareCounts, error)
	InsertSpare(ctx context.Context, dbName string) error
	MarkSpareReady(ctx context.Context, dbName, version, fingerprint string) error
	MarkSpareFailed(ctx context.Context, dbName, reason string) error
	SetSpareSchema(ctx context.Context, dbName, version, fingerprint string) error
	ReadySpares(ctx context.Context) ([]platformstore.Spare, error)
	ProvisioningTeams(ctx context.Context, limit int) ([]platformstore.ProvisioningTeam, error)
	ClaimSpare(ctx context.Context, teamID int64, version string) (string, error)
	ActivateTeam(ctx context.Context, teamID int64, version, fingerprint string) error
	SetTeamSchema(ctx context.Context, teamID int64, version, fingerprint string) error
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
	Migrate(ctx context.Context, dbName string) (version string, err error)
	Lines(ctx context.Context, dbName string) ([]string, error)
	Latest() string
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

// Config configures the worker.
type Config struct {
	Prefix  string // TENANT_DB_PREFIX
	Team1DB string // team #1's database (only fingerprinted, never created, migrated or seeded here)
	Spares  int    // TENANT_SPARES (0 = DefaultSpares)
	PerHour int    // TENANT_PROVISION_PER_HOUR (0 = DefaultPerHour)
	Every   Intervals
}

// Worker is the provisioner.
type Worker struct {
	cfg    Config
	pf     Platform
	schema Schema
	db     dependency.DB
	admins dependency.Admin
	now    func() time.Time
	log    *slog.Logger
	// seed seeds the team in ctx; Seed over db / admins unless a test replaces it.
	seed func(ctx context.Context, ownerHandle string) error

	mu      sync.Mutex
	backoff map[int64]retry // team id → next attempt
	capLog  time.Time       // last "cap reached" log
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

// New builds the worker. db / admins are the routing pool and the admins store (team in ctx).
func New(cfg Config, pf Platform, schema Schema, db dependency.DB, admins dependency.Admin) (*Worker, error) {
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
	if pf == nil || schema == nil || db == nil || admins == nil {
		return nil, errors.New("provision: missing dependency")
	}
	w := &Worker{
		cfg:     cfg,
		pf:      pf,
		schema:  schema,
		db:      db,
		admins:  admins,
		now:     time.Now,
		log:     slog.Default(),
		backoff: map[int64]retry{},
	}
	w.seed = func(ctx context.Context, owner string) error { return Seed(ctx, w.db, w.admins, owner) }
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
// migration and the hourly cap allows.
func (w *Worker) KeepSparesOnce(ctx context.Context) error {
	latest := w.schema.Latest()
	if latest == "" {
		return errors.New("provision: no embedded migrations")
	}
	c, err := w.pf.CountSpares(ctx, latest, w.now().Add(-time.Hour))
	if err != nil {
		w.log.ErrorContext(ctx, "provision: count spares", slog.String("err", err.Error()))
		return err
	}
	if c.Ready+c.Creating >= w.cfg.Spares {
		return nil
	}
	if c.CreatedSince >= w.cfg.PerHour {
		w.mu.Lock()
		quiet := w.now().Sub(w.capLog) < 10*time.Minute
		if !quiet {
			w.capLog = w.now()
		}
		w.mu.Unlock()
		if !quiet {
			w.log.WarnContext(ctx, "provision: hourly database creation cap reached; spares wait",
				slog.Int("created_last_hour", c.CreatedSince), slog.Int("cap", w.cfg.PerHour))
		}
		return nil
	}
	_, err = w.makeSpare(ctx)
	return err
}

// makeSpare records, creates, migrates and fingerprints one spare database.
func (w *Worker) makeSpare(ctx context.Context) (string, error) {
	var name string
	var err error
	for range 3 { // a random-name collision with an existing row is retried with a new name
		if name, err = NewDBName(w.cfg.Prefix, w.cfg.Team1DB); err != nil {
			return "", err
		}
		if err = w.pf.InsertSpare(ctx, name); err == nil {
			break
		}
	}
	if err != nil {
		w.log.ErrorContext(ctx, "provision: record spare", slog.String("err", err.Error()))
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

// ProvisionTeam claims (or makes) the team's database, seeds it and activates the team.
// Safe to rerun at any step.
func (w *Worker) ProvisionTeam(ctx context.Context, t platformstore.ProvisioningTeam) error {
	if t.ID == tenant.TeamOneID {
		return errors.New("provision: team #1 is never provisioned")
	}
	latest := w.schema.Latest()
	dbName := t.DBName.String
	fresh := false
	if !t.DBName.Valid || dbName == "" {
		var err error
		dbName, err = w.pf.ClaimSpare(ctx, t.ID, latest)
		if errors.Is(err, platformstore.ErrNoSpare) {
			// Cold path, here in the worker: make one spare now, then claim (whichever ready
			// spare the claim hands out — they are all equal).
			w.log.WarnContext(ctx, "provision: no ready spare; creating a database for the team",
				slog.Int64("tid", t.ID), slog.String("slug", t.Slug))
			if _, err = w.makeSpare(ctx); err != nil {
				return err
			}
			dbName, err = w.pf.ClaimSpare(ctx, t.ID, latest)
		}
		if errors.Is(err, platformstore.ErrNotProvisioning) {
			return nil // confirmed elsewhere or gone: nothing to do
		}
		if err != nil {
			return fmt.Errorf("claim: %w", err)
		}
		fresh = true
	}
	if err := w.allowed(dbName); err != nil {
		return fmt.Errorf("team database refused: %w", err)
	}
	version := latest
	if !fresh {
		// A rerun after a crash, possibly by an older binary: bring the database forward first.
		var err error
		if version, err = w.schema.Migrate(ctx, dbName); err != nil {
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
	if err := w.seed(tctx, t.OwnerHandle); err != nil {
		return fmt.Errorf("seed: %w", err)
	}
	lines, err := w.schema.Lines(ctx, dbName)
	if err != nil {
		return fmt.Errorf("fingerprint: %w", err)
	}
	if err := w.pf.ActivateTeam(ctx, t.ID, version, store.FingerprintOf(lines)); err != nil {
		if errors.Is(err, platformstore.ErrNotProvisioning) {
			return nil
		}
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
