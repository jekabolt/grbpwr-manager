// Package teamwipe is the day-30 loop (teams D3 + S4c, lanes/D3-design.md): it purges deleted
// accounts whose 30 days passed, then wipes deleted teams whose 30 days passed — their bucket files
// (every version), their database (DROP DATABASE on the provisioner's named lock) and their
// control-plane rows. Team #1 is never wiped: every store call refuses it again.
//
// PLATFORM_PURGE_MODE: "off" (the loop never starts), "dry" (default: log what is due and what it
// would erase, write nothing), "on" (purge and wipe). An unknown value is read as dry.
package teamwipe

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/bucket"
	"github.com/jekabolt/grbpwr-manager/internal/platformstore"
	"github.com/jekabolt/grbpwr-manager/internal/store"
	"github.com/jekabolt/grbpwr-manager/internal/tenant"
)

// Mode is PLATFORM_PURGE_MODE.
type Mode string

const (
	ModeOff Mode = "off"
	ModeDry Mode = "dry"
	ModeOn  Mode = "on"
)

// ParseMode reads PLATFORM_PURGE_MODE: off / dry / on (any case, spaces trimmed); empty or unknown
// is dry, so a typo never erases anything.
func ParseMode(s string) Mode {
	switch Mode(strings.ToLower(strings.TrimSpace(s))) {
	case ModeOff:
		return ModeOff
	case ModeOn:
		return ModeOn
	}
	return ModeDry
}

const (
	// LeaseKey is the provision_lease row one instance holds while it wipes teams.
	LeaseKey = "team-wipe"
	// leaseTTL: the lease is extended before every step; a dead instance frees it within this.
	leaseTTL = 2 * time.Minute
	// teamsPerTick bounds the teams claimed in one pass.
	teamsPerTick = 20
	// accountRoundsMax bounds the account purge rounds of one pass (PurgeBatch each).
	accountRoundsMax = 20
	// fileBatchesPerCall / fileCallsPerStep: one file step deletes at most 5 × 10 × 1000 objects,
	// extending the lease between calls; a bigger prefix goes on next pass.
	fileBatchesPerCall = 5
	fileCallsPerStep   = 10
	// StuckAfter: from this many failed attempts a wipe logs ERROR "teams: wipe stuck".
	StuckAfter = 6
	// tickTimeout bounds one pass.
	tickTimeout = 30 * time.Minute
)

// Store is the control plane the loop uses (*platformstore.Store).
type Store interface {
	DueAccountPurges(ctx context.Context, limit int) ([]platformstore.DueAccount, error)
	PurgeDeletedAccounts(ctx context.Context, batch int, unusable func() (string, error)) (purged, due int, err error)

	AcquireLease(ctx context.Context, key string, ttl time.Duration) (string, bool, error)
	ExtendLease(ctx context.Context, key, token string, ttl time.Duration) error
	ReleaseLease(ctx context.Context, key, token string) error

	DueTeamWipes(ctx context.Context, limit int) ([]int64, error)
	OpenTeamWipes(ctx context.Context, limit int) ([]platformstore.TeamWipe, error)
	ClaimTeamWipe(ctx context.Context, teamID int64, now time.Time) (platformstore.TeamWipe, bool, error)
	MarkWipeFilesDone(ctx context.Context, teamID, objects int64, now time.Time) error
	AddWipeObjects(ctx context.Context, teamID, objects int64) error
	MarkWipeDBDropped(ctx context.Context, teamID int64, now time.Time) error
	MarkWipeFailed(ctx context.Context, teamID int64, reason string, count bool, now time.Time) (int, error)
	DBNameReferenced(ctx context.Context, dbName string) (bool, error)
	FileKeyReferenced(ctx context.Context, fileKey string) (bool, error)
	TeamWipeTombstone(ctx context.Context, teamID int64) (bool, error)
	FinishTeamWipe(ctx context.Context, teamID int64, now time.Time) (platformstore.WipedTeam, error)
	PreviewTeamWipe(ctx context.Context, teamID int64) (platformstore.TeamWipePreview, error)
}

// Files is the bucket side (*bucket.Bucket). nil: a team with files fails its file step.
type Files interface {
	WipeTeamFiles(ctx context.Context, fileKey string, maxBatches int) (bucket.WipeStats, error)
	PreviewTeamFiles(ctx context.Context, fileKey string) (bucket.WipeStats, error)
	// PreflightTeamFiles runs the prefix guard and the bucket call a wipe would fail on; nothing is
	// listed or deleted.
	PreflightTeamFiles(ctx context.Context, fileKey string) error
}

// Databases drops and measures team databases (store.DropTeamDB / store.TeamDBSize bound to the
// base DSN, the prefix and the control plane's name).
type Databases interface {
	Drop(ctx context.Context, dbName string) (store.DropResult, error)
	Size(ctx context.Context, dbName string) (exists bool, bytes int64, err error)
	// CheckDrop is the allow-list Drop applies (store.CheckDropName), without touching the server.
	CheckDrop(dbName string) error
}

// Worker runs the day-30 pass now and then.
type Worker struct {
	mode     Mode
	st       Store
	files    Files
	dbs      Databases
	unusable func() (string, error)
	first    time.Duration
	every    time.Duration
	now      func() time.Time

	stop chan struct{}
	done chan struct{}
	once sync.Once
}

// New builds the worker. unusable returns a fresh password hash nobody can match (for purged accounts).
func New(mode Mode, st Store, files Files, dbs Databases, unusable func() (string, error), first, every time.Duration) *Worker {
	return &Worker{mode: mode, st: st, files: files, dbs: dbs, unusable: unusable, first: first, every: every,
		now:  func() time.Time { return time.Now().UTC().Truncate(time.Millisecond) },
		stop: make(chan struct{}), done: make(chan struct{})}
}

// Start runs the loop in the background (nothing in mode off).
func (w *Worker) Start(ctx context.Context) {
	if w.mode == ModeOff {
		close(w.done)
		return
	}
	go func() {
		defer close(w.done)
		t := time.NewTimer(w.first)
		defer t.Stop()
		for {
			select {
			case <-w.stop:
				return
			case <-ctx.Done():
				return
			case <-t.C:
			}
			w.Tick(ctx)
			t.Reset(w.every)
		}
	}()
}

// Stop ends the loop and waits for a running pass.
func (w *Worker) Stop() {
	w.once.Do(func() { close(w.stop) })
	<-w.done
}

// Tick is one pass: accounts first (a team closed with an account waits for its purge), then teams.
func (w *Worker) Tick(ctx context.Context) {
	if w.mode == ModeOff {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, tickTimeout)
	defer cancel()
	stopCh := make(chan struct{})
	defer close(stopCh)
	go func() { // Stop cuts a running pass short; every step is resumable
		select {
		case <-w.stop:
			cancel()
		case <-stopCh:
		}
	}()
	if w.mode == ModeDry {
		w.dryAccounts(ctx)
		w.dryTeams(ctx)
		return
	}
	w.purgeAccounts(ctx)
	w.wipeTeams(ctx)
}

func logErr(ctx context.Context, msg string, err error, attrs ...any) {
	if ctx.Err() != nil {
		return
	}
	slog.Default().ErrorContext(ctx, msg, append(attrs, slog.String("err", err.Error()))...)
}

// ---- dry ----

func (w *Worker) dryAccounts(ctx context.Context) {
	due, err := w.st.DueAccountPurges(ctx, platformstore.PurgeBatch*accountRoundsMax)
	if err != nil {
		logErr(ctx, "teams: purge (dry): due accounts", err)
		return
	}
	for _, a := range due {
		slog.Default().InfoContext(ctx, "teams: purge (dry): account would be purged",
			slog.String("handle", a.Handle), slog.Time("purge_at", a.PurgeAt))
	}
	slog.Default().InfoContext(ctx, "teams: purge (dry): accounts due", slog.Int("count", len(due)))
}

func (w *Worker) dryTeams(ctx context.Context) {
	ids, err := w.st.DueTeamWipes(ctx, teamsPerTick)
	if err != nil {
		logErr(ctx, "teams: wipe (dry): due teams", err)
		return
	}
	for _, id := range ids {
		if id == tenant.TeamOneID {
			continue
		}
		p, err := w.st.PreviewTeamWipe(ctx, id)
		if err != nil {
			logErr(ctx, "teams: wipe (dry): preview", err)
			continue
		}
		attrs := []any{slog.String("team", p.Slug), slog.Time("purge_at", p.PurgeAt),
			slog.Bool("waits_for_account", p.ClosedWithPending), slog.Any("rows", p.Rows)}
		if p.DBName != "" {
			if w.dbs == nil {
				attrs = append(attrs, slog.String("db", p.DBName), slog.String("db_size", "no database access"))
			} else if exists, size, err := w.dbs.Size(ctx, p.DBName); err != nil {
				attrs = append(attrs, slog.String("db", p.DBName), slog.String("db_err", err.Error()))
			} else {
				attrs = append(attrs, slog.String("db", p.DBName), slog.Bool("db_exists", exists), slog.Int64("db_bytes", size))
			}
		}
		if p.FileKey != "" {
			if w.files == nil {
				attrs = append(attrs, slog.String("files", "no file store"))
			} else if fs, err := w.files.PreviewTeamFiles(ctx, p.FileKey); err != nil {
				attrs = append(attrs, slog.String("files_err", err.Error()))
			} else {
				attrs = append(attrs, slog.Int64("objects", fs.Objects), slog.Int64("bytes", fs.Bytes))
			}
		}
		if err := w.preflight(ctx, p.DBName, p.FileKey); err != nil {
			attrs = append(attrs, slog.String("preflight_err", err.Error()))
		}
		slog.Default().InfoContext(ctx, "teams: wipe (dry): team would be wiped", attrs...)
	}
	slog.Default().InfoContext(ctx, "teams: wipe (dry): teams due", slog.Int("count", len(ids)))
}

// ---- on ----

func (w *Worker) purgeAccounts(ctx context.Context) {
	total := 0
	for range accountRoundsMax {
		purged, due, err := w.st.PurgeDeletedAccounts(ctx, platformstore.PurgeBatch, w.unusable)
		total += purged
		if err != nil {
			logErr(ctx, "teams: account purge", err)
		}
		if err != nil && purged == 0 || due < platformstore.PurgeBatch || ctx.Err() != nil {
			break
		}
	}
	if total > 0 {
		slog.Default().InfoContext(ctx, "teams: accounts purged", slog.Int("count", total))
	}
}

// errLease: this instance lost the wipe lease mid-pass; the pass stops, the holder resumes.
var errLease = errors.New("teamwipe: lease lost")

func (w *Worker) wipeTeams(ctx context.Context) {
	token, ok, err := w.st.AcquireLease(ctx, LeaseKey, leaseTTL)
	if err != nil {
		logErr(ctx, "teams: wipe: lease", err)
		return
	}
	if !ok {
		slog.Default().InfoContext(ctx, "teams: wipe skipped: another instance holds it")
		return
	}
	defer func() {
		if err := w.st.ReleaseLease(context.WithoutCancel(ctx), LeaseKey, token); err != nil {
			logErr(context.WithoutCancel(ctx), "teams: wipe: release lease", err)
		}
	}()
	extend := func() error {
		if err := w.st.ExtendLease(ctx, LeaseKey, token, leaseTTL); err != nil {
			if errors.Is(err, platformstore.ErrLeaseLost) {
				return errLease
			}
			return err
		}
		return nil
	}

	// Resume claimed wipes first (a crash or a failed step), then claim the due ones.
	open, err := w.st.OpenTeamWipes(ctx, teamsPerTick)
	if err != nil {
		logErr(ctx, "teams: wipe: open wipes", err)
		return
	}
	for _, tw := range open {
		if err := w.runWipe(ctx, tw, extend); errors.Is(err, errLease) {
			slog.Default().WarnContext(ctx, "teams: wipe: lease lost, pass stopped")
			return
		}
	}
	ids, err := w.st.DueTeamWipes(ctx, teamsPerTick)
	if err != nil {
		logErr(ctx, "teams: wipe: due teams", err)
		return
	}
	for _, id := range ids {
		if ctx.Err() != nil {
			return
		}
		if id == tenant.TeamOneID {
			continue
		}
		// The claim cannot be undone: a team whose steps would fail on its own values (a file key
		// the prefix guard refuses, a bucket we cannot read, a database name the drop guard
		// refuses) is not claimed and stays restorable.
		p, err := w.st.PreviewTeamWipe(ctx, id)
		if err != nil {
			if !errors.Is(err, platformstore.ErrTeamNotFound) { // restored meanwhile: nothing to say
				logErr(ctx, "teams: wipe: preflight read", err, slog.Int64("team_id", id))
			}
			continue
		}
		if err := w.preflight(ctx, p.DBName, p.FileKey); err != nil {
			if ctx.Err() != nil {
				return
			}
			slog.Default().WarnContext(ctx, "teams: wipe not claimed: preflight failed", slog.String("team", p.Slug),
				slog.String("err", err.Error()))
			continue
		}
		if err := extend(); err != nil {
			if errors.Is(err, errLease) {
				slog.Default().WarnContext(ctx, "teams: wipe: lease lost, pass stopped")
			} else {
				logErr(ctx, "teams: wipe: extend lease", err)
			}
			return
		}
		tw, claimed, err := w.st.ClaimTeamWipe(ctx, id, w.now())
		if err != nil {
			logErr(ctx, "teams: wipe: claim", err)
			continue
		}
		if !claimed {
			continue
		}
		slog.Default().InfoContext(ctx, "teams: wipe started", slog.String("team", tw.Slug))
		if err := w.runWipe(ctx, tw, extend); errors.Is(err, errLease) {
			slog.Default().WarnContext(ctx, "teams: wipe: lease lost, pass stopped")
			return
		}
	}
}

// preflight runs, on a due team's values, the checks its wipe steps would fail on, without writing
// anything: the file prefix guard and the bucket's versioning call, and the database drop guard.
func (w *Worker) preflight(ctx context.Context, dbName, fileKey string) error {
	if fileKey != "" {
		if w.files == nil {
			return errors.New("files: no file store")
		}
		if err := w.files.PreflightTeamFiles(ctx, fileKey); err != nil {
			return fmt.Errorf("files: %w", err)
		}
	}
	if dbName != "" {
		if w.dbs == nil {
			return errors.New("database: no database access")
		}
		if err := w.dbs.CheckDrop(dbName); err != nil {
			return fmt.Errorf("database: %w", err)
		}
	}
	return nil
}

// errFilesFenced: the file step found the team row changed or its file key named again; nothing is
// deleted.
var errFilesFenced = errors.New("files fenced")

// filesFence is checked before every delete call of the file step, as DBNameReferenced is before the
// drop: the team row must still be the claimed tombstone and no team row may name the file key.
func (w *Worker) filesFence(ctx context.Context, tw platformstore.TeamWipe) error {
	tomb, err := w.st.TeamWipeTombstone(ctx, tw.TeamID)
	if err != nil {
		return err
	}
	if !tomb {
		return fmt.Errorf("%w: team row is not a claimed tombstone", errFilesFenced)
	}
	held, err := w.st.FileKeyReferenced(ctx, tw.FileKey.String)
	if err != nil {
		return err
	}
	if held {
		return fmt.Errorf("%w: file key is still named by a team", errFilesFenced)
	}
	return nil
}

// runWipe runs the remaining steps of one claimed wipe: files → database → finalize. A failed step
// is recorded on the row (retried after the backoff) and ends this team's turn; errLease ends the pass.
func (w *Worker) runWipe(ctx context.Context, tw platformstore.TeamWipe, extend func() error) error {
	if tw.TeamID == tenant.TeamOneID {
		logErr(ctx, "teams: wipe: refused", platformstore.ErrTeamOne)
		return nil
	}
	fail := func(step string, err error, count bool) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		reason := step + ": " + err.Error()
		n, merr := w.st.MarkWipeFailed(ctx, tw.TeamID, reason, count, w.now())
		if merr != nil {
			logErr(ctx, "teams: wipe: record failure", merr, slog.String("team", tw.Slug))
		}
		if n >= StuckAfter {
			slog.Default().ErrorContext(ctx, "teams: wipe stuck", slog.String("team", tw.Slug), slog.String("step", step),
				slog.Int("attempts", n), slog.String("err", err.Error()))
		} else {
			slog.Default().WarnContext(ctx, "teams: wipe step failed", slog.String("team", tw.Slug), slog.String("step", step),
				slog.Int("attempts", n), slog.String("err", err.Error()))
		}
		return err
	}
	if err := extend(); err != nil {
		if errors.Is(err, errLease) {
			return err
		}
		logErr(ctx, "teams: wipe: extend lease", err)
		return err
	}

	if tw.FilesPending() {
		if w.files == nil {
			return fail("files", errors.New("no file store"), true)
		}
		var deleted int64
		done := false
		for i := 0; i < fileCallsPerStep && !done; i++ {
			if i > 0 {
				if err := extend(); err != nil {
					_ = w.st.AddWipeObjects(ctx, tw.TeamID, deleted)
					return err
				}
			}
			if err := w.filesFence(ctx, tw); err != nil {
				_ = w.st.AddWipeObjects(ctx, tw.TeamID, deleted)
				if errors.Is(err, errFilesFenced) && ctx.Err() == nil {
					slog.Default().ErrorContext(ctx, "teams: wipe files refused", slog.String("team", tw.Slug),
						slog.String("err", err.Error()))
				}
				return fail("files", err, true)
			}
			st, err := w.files.WipeTeamFiles(ctx, tw.FileKey.String, fileBatchesPerCall)
			deleted += st.Objects
			if err != nil {
				_ = w.st.AddWipeObjects(ctx, tw.TeamID, deleted)
				return fail("files", err, true)
			}
			done = st.Done
		}
		if !done { // a big prefix: progress kept, the next pass goes on
			if err := w.st.AddWipeObjects(ctx, tw.TeamID, deleted); err != nil {
				logErr(ctx, "teams: wipe: objects", err, slog.String("team", tw.Slug))
			}
			slog.Default().InfoContext(ctx, "teams: wipe files in progress", slog.String("team", tw.Slug), slog.Int64("objects", deleted))
			return nil
		}
		if err := w.st.MarkWipeFilesDone(ctx, tw.TeamID, deleted, w.now()); err != nil {
			return fail("files", err, true)
		}
		slog.Default().InfoContext(ctx, "teams: wipe files deleted", slog.String("team", tw.Slug), slog.Int64("objects", deleted))
		if err := extend(); err != nil {
			return err
		}
	}

	if tw.DBPending() {
		if w.dbs == nil {
			return fail("database", errors.New("no database access"), true)
		}
		name := tw.DBName.String
		held, err := w.st.DBNameReferenced(ctx, name)
		if err != nil {
			return fail("database", err, true)
		}
		if held {
			return fail("database", fmt.Errorf("database %s is still named by a team or spare", name), true)
		}
		res, err := w.dbs.Drop(ctx, name)
		if err != nil {
			return fail("database", err, true)
		}
		if res == store.DropBusy {
			return fail("database", errors.New("named lock busy (a migration or seed)"), false)
		}
		if err := w.st.MarkWipeDBDropped(ctx, tw.TeamID, w.now()); err != nil {
			return fail("database", err, true)
		}
		slog.Default().InfoContext(ctx, "teams: wipe database dropped", slog.String("team", tw.Slug),
			slog.String("db", name), slog.String("result", res.String()))
		if err := extend(); err != nil {
			return err
		}
	}

	wt, err := w.st.FinishTeamWipe(ctx, tw.TeamID, w.now())
	if err != nil {
		return fail("finalize", err, true)
	}
	if wt.Finished {
		slog.Default().InfoContext(ctx, "teams: team wiped", slog.String("team", wt.Slug), slog.Any("rows", wt.Rows))
	}
	return nil
}
