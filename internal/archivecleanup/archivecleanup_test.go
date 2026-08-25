package archivecleanup

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/bucket"
	"github.com/jekabolt/grbpwr-manager/internal/dependency"
)

// ─────────────────────────────────────────────────────────────────────────────
// FAKES
//
// Both embed their interface as a NIL value on purpose: every method this worker is not supposed
// to touch is present (so the fake satisfies the interface) and panics if called (so "the sweeper
// only ever calls these two things" is enforced by the test rather than asserted in prose).
//
// fakeFiles does not stub ListObjectsOlderThan with a canned answer — it IMPLEMENTS the documented
// selection (an object counts as expired when its last-modified is strictly before now-age, and an
// object with no date never counts) over a fixture of real timestamps. That is what makes the age
// tests below mean anything: the fixture supplies the objects, the WORKER supplies the age, and a
// worker that passed a shorter window would start eating fresh uploads exactly as the real bucket
// would.
// ─────────────────────────────────────────────────────────────────────────────

type fakeObject struct {
	key          string
	lastModified time.Time
}

type fakeFiles struct {
	dependency.FileStore

	mu      sync.Mutex
	now     time.Time
	objects []fakeObject

	listErr map[string]error

	askedSegments []string
	askedAges     []time.Duration
	removed       []string
	removeErr     error
	removeCalls   int

	// entered/release turn one listing call into a barrier, so a test can hold the worker
	// inside a tick while it checks what Stop does.
	entered chan struct{}
	release chan struct{}
}

func (f *fakeFiles) ListObjectsOlderThan(ctx context.Context, segment string, age time.Duration) ([]string, error) {
	f.mu.Lock()
	f.askedSegments = append(f.askedSegments, segment)
	f.askedAges = append(f.askedAges, age)
	entered, release := f.entered, f.release
	err := f.listErr[segment]
	now, objects := f.now, append([]fakeObject(nil), f.objects...)
	f.mu.Unlock()

	if entered != nil {
		select {
		case entered <- struct{}{}:
		default:
		}
		<-release
	}
	if err != nil {
		return nil, err
	}

	cutoff := now.Add(-age)
	var keys []string
	for _, o := range objects {
		if !inSegment(o.key, segment) {
			continue
		}
		// "Age unknown" is not "old": the real listing skips an object without a date, and
		// deletion is irreversible.
		if o.lastModified.IsZero() || !o.lastModified.Before(cutoff) {
			continue
		}
		keys = append(keys, o.key)
	}
	return keys, nil
}

func (f *fakeFiles) RemoveObjectsByKeys(ctx context.Context, keys ...string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.removeCalls++
	if f.removeErr != nil {
		return f.removeErr
	}
	f.removed = append(f.removed, keys...)
	return nil
}

func inSegment(key, segment string) bool {
	return len(key) > len(segment)+1 && key[:len(segment)+1] == segment+"/"
}

func (f *fakeFiles) survivors() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	gone := make(map[string]bool, len(f.removed))
	for _, k := range f.removed {
		gone[k] = true
	}
	var left []string
	for _, o := range f.objects {
		if !gone[o.key] {
			left = append(left, o.key)
		}
	}
	sort.Strings(left)
	return left
}

func (f *fakeFiles) snapshot() (segments []string, ages []time.Duration, removed []string, removeCalls int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.askedSegments...),
		append([]time.Duration(nil), f.askedAges...),
		append([]string(nil), f.removed...),
		f.removeCalls
}

type fakeTechCards struct {
	dependency.TechCards

	mu      sync.Mutex
	cutoffs []time.Time
	rows    []time.Time // created_at of the still-'uploaded' import rows
	expired []time.Time // the ones the UPDATE would have moved
	err     error
	calls   int
}

func (t *fakeTechCards) ExpireStaleTechCardImports(ctx context.Context, olderThan time.Time) (int64, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.calls++
	t.cutoffs = append(t.cutoffs, olderThan)
	if t.err != nil {
		return 0, t.err
	}
	var n int64
	for _, createdAt := range t.rows {
		if createdAt.Before(olderThan) {
			t.expired = append(t.expired, createdAt)
			n++
		}
	}
	return n, nil
}

type fakeRepo struct {
	dependency.Repository
	cards *fakeTechCards
}

func (r *fakeRepo) TechCards() dependency.TechCards { return r.cards }

func newFixture(now time.Time, objects []fakeObject, rows []time.Time) (*fakeRepo, *fakeFiles) {
	return &fakeRepo{cards: &fakeTechCards{rows: rows}},
		&fakeFiles{now: now, objects: objects, listErr: map[string]error{}}
}

// runOneTick drives exactly one cleanup pass without the ticker, so nothing in these tests
// depends on wall-clock timing.
func runOneTick(t *testing.T, repo dependency.Repository, files dependency.FileStore) bool {
	t.Helper()
	return New(nil, repo, files).runCleanup(context.Background())
}

// ─────────────────────────────────────────────────────────────────────────────
// THE AGE THRESHOLD — the one that costs an operator a card if it is wrong.
// ─────────────────────────────────────────────────────────────────────────────

// TestCleanupSparesObjectsYoungerThanRetention is the trap this worker exists to walk past: an
// import uploaded this morning and not yet committed still has an operator standing in front of
// it, and deleting its bytes hands them a 404 for a card they were about to create. Nothing but
// the age keeps it alive, so the age is what is measured here — at today, at yesterday, and on
// both sides of the boundary itself.
func TestCleanupSparesObjectsYoungerThanRetention(t *testing.T) {
	now := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	objects := []fakeObject{
		{key: bucket.ImportSegment + "/today.zip", lastModified: now.Add(-2 * time.Hour)},
		{key: bucket.ImportSegment + "/yesterday.zip", lastModified: now.Add(-24 * time.Hour)},
		// One minute on the young side of seven days: still an import somebody may commit.
		{key: bucket.ImportSegment + "/just-inside.zip", lastModified: now.Add(-bucket.ArchiveRetention + time.Minute)},
		// One minute past it: gone.
		{key: bucket.ImportSegment + "/just-outside.zip", lastModified: now.Add(-bucket.ArchiveRetention - time.Minute)},
		{key: bucket.ArchiveSegment + "/fresh-export.zip", lastModified: now.Add(-time.Hour)},
		{key: bucket.ArchiveSegment + "/old-export.zip", lastModified: now.Add(-30 * 24 * time.Hour)},
		// No last-modified date at all: "unknown age" must not read as "old".
		{key: bucket.ArchiveSegment + "/undated.zip"},
	}
	repo, files := newFixture(now, objects, nil)

	if ok := runOneTick(t, repo, files); !ok {
		t.Fatal("clean tick reported failure")
	}

	want := []string{
		bucket.ArchiveSegment + "/fresh-export.zip",
		bucket.ArchiveSegment + "/undated.zip",
		bucket.ImportSegment + "/just-inside.zip",
		bucket.ImportSegment + "/today.zip",
		bucket.ImportSegment + "/yesterday.zip",
	}
	if got := files.survivors(); !equalStrings(got, want) {
		t.Fatalf("wrong objects survived the sweep:\n got %v\nwant %v", got, want)
	}

	_, ages, removed, _ := files.snapshot()
	sort.Strings(removed)
	wantRemoved := []string{
		bucket.ArchiveSegment + "/old-export.zip",
		bucket.ImportSegment + "/just-outside.zip",
	}
	if !equalStrings(removed, wantRemoved) {
		t.Fatalf("wrong objects removed:\n got %v\nwant %v", removed, wantRemoved)
	}
	for _, age := range ages {
		if age != bucket.ArchiveRetention {
			t.Fatalf("listing asked for age %s, want the retention window %s", age, bucket.ArchiveRetention)
		}
	}
	if bucket.ArchiveRetention != 168*time.Hour {
		t.Fatalf("retention is %s, the owner's decision was 168h", bucket.ArchiveRetention)
	}
}

// TestExpireCutoffMatchesRetentionAndSparesYesterday pins the row half to the same boundary. A
// yesterday's upload is the case that matters: its bytes are still in the bucket (proved above),
// so a row saying "expired" would be a lie an operator reads before they press commit.
func TestExpireCutoffMatchesRetentionAndSparesYesterday(t *testing.T) {
	now := time.Now().UTC()
	rows := []time.Time{
		now.Add(-time.Hour),                             // uploaded this morning
		now.Add(-24 * time.Hour),                        // yesterday
		now.Add(-bucket.ArchiveRetention + time.Minute), // one minute inside the window
		now.Add(-bucket.ArchiveRetention - time.Minute), // one minute past it
		now.Add(-30 * 24 * time.Hour),                   // a month old
	}
	repo, files := newFixture(now, nil, rows)

	if ok := runOneTick(t, repo, files); !ok {
		t.Fatal("clean tick reported failure")
	}

	cards := repo.cards
	if cards.calls != 1 {
		t.Fatalf("expiry ran %d times in one tick, want exactly 1", cards.calls)
	}
	cutoff := cards.cutoffs[0]
	// The cutoff is computed from the tick's own "now", so compare the window rather than the
	// instant; anything but seven days back is the defect.
	if drift := now.Add(-bucket.ArchiveRetention).Sub(cutoff); drift < -time.Minute || drift > time.Minute {
		t.Fatalf("cutoff %s is %s away from now-%s", cutoff, drift, bucket.ArchiveRetention)
	}
	if len(cards.expired) != 2 {
		t.Fatalf("expired %d rows, want the 2 past the window: %v", len(cards.expired), cards.expired)
	}
	for _, createdAt := range cards.expired {
		if now.Sub(createdAt) < bucket.ArchiveRetention {
			t.Fatalf("expired a row created %s ago, inside the %s window", now.Sub(createdAt), bucket.ArchiveRetention)
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// SEGMENTS — what the sweeper is allowed to name.
// ─────────────────────────────────────────────────────────────────────────────

// TestCleanupNamesOnlyTheTwoArchiveSegments: the bucket refuses a foreign segment, but the worker
// must not be the thing that asks. It is also the only place that decides the order and the count,
// so the whole call log is compared, not just its membership.
func TestCleanupNamesOnlyTheTwoArchiveSegments(t *testing.T) {
	now := time.Now().UTC()
	old := now.Add(-30 * 24 * time.Hour)
	// Aged-out objects in folders this worker must never sweep: media, patterns, the file
	// library. The fake would happily hand them over — only the segments the worker names keep
	// them out of the selection.
	objects := []fakeObject{
		{key: bucket.ArchiveSegment + "/mine.zip", lastModified: old},
		{key: bucket.ImportSegment + "/mine.zip", lastModified: old},
		{key: "grbpwr-com/2020/01/photo.jpg", lastModified: old},
		{key: "tech-card-patterns/9/front.dxf", lastModified: old},
		{key: "files-library/notes.pdf", lastModified: old},
	}
	repo, files := newFixture(now, objects, nil)

	if ok := runOneTick(t, repo, files); !ok {
		t.Fatal("clean tick reported failure")
	}

	segments, _, removed, _ := files.snapshot()
	want := []string{bucket.ArchiveSegment, bucket.ImportSegment}
	if !equalStrings(segments, want) {
		t.Fatalf("segments listed: %v, want exactly %v", segments, want)
	}
	sort.Strings(removed)
	wantRemoved := []string{bucket.ArchiveSegment + "/mine.zip", bucket.ImportSegment + "/mine.zip"}
	if !equalStrings(removed, wantRemoved) {
		t.Fatalf("removed %v, want only the feature's own objects %v", removed, wantRemoved)
	}
	survivors := files.survivors()
	for _, k := range survivors {
		if inSegment(k, bucket.ArchiveSegment) || inSegment(k, bucket.ImportSegment) {
			t.Fatalf("an aged-out object of ours survived: %s", k)
		}
	}
	if len(survivors) != 3 {
		t.Fatalf("survivors %v, want the 3 foreign-segment objects untouched", survivors)
	}
}

// TestCleanupSkipsRemovalWhenNothingExpired: an empty selection is not a delete call with an empty
// list. RemoveObjectsByKeys with no keys is harmless today, but "the sweeper called delete" should
// remain a thing that only happens when something was actually selected.
func TestCleanupSkipsRemovalWhenNothingExpired(t *testing.T) {
	now := time.Now().UTC()
	repo, files := newFixture(now, []fakeObject{
		{key: bucket.ImportSegment + "/today.zip", lastModified: now.Add(-time.Hour)},
	}, nil)

	if ok := runOneTick(t, repo, files); !ok {
		t.Fatal("clean tick reported failure")
	}
	if _, _, _, calls := files.snapshot(); calls != 0 {
		t.Fatalf("removal called %d times with nothing expired, want 0", calls)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// FAILURE — one half must not gate the other, and a partial tick must not read as healthy.
// ─────────────────────────────────────────────────────────────────────────────

func TestCleanupHalvesDoNotGateEachOther(t *testing.T) {
	now := time.Now().UTC()
	old := now.Add(-30 * 24 * time.Hour)

	t.Run("a broken bucket still lets the rows expire", func(t *testing.T) {
		repo, files := newFixture(now, nil, []time.Time{old})
		files.listErr[bucket.ArchiveSegment] = errors.New("spaces is down")
		files.listErr[bucket.ImportSegment] = errors.New("spaces is down")

		if ok := runOneTick(t, repo, files); ok {
			t.Fatal("a tick that could not list anything reported success")
		}
		if len(repo.cards.expired) != 1 {
			t.Fatalf("expired %d rows, want 1 — the DB half must not wait on the bucket", len(repo.cards.expired))
		}
	})

	t.Run("a broken DB still lets the objects go", func(t *testing.T) {
		repo, files := newFixture(now, []fakeObject{
			{key: bucket.ArchiveSegment + "/old.zip", lastModified: old},
		}, nil)
		repo.cards.err = errors.New("pool is closed")

		if ok := runOneTick(t, repo, files); ok {
			t.Fatal("a tick whose UPDATE failed reported success")
		}
		if _, _, removed, _ := files.snapshot(); len(removed) != 1 {
			t.Fatalf("removed %v, want the aged-out object gone regardless of the DB", removed)
		}
	})

	t.Run("one bad segment does not skip the other", func(t *testing.T) {
		repo, files := newFixture(now, []fakeObject{
			{key: bucket.ImportSegment + "/old.zip", lastModified: old},
		}, nil)
		files.listErr[bucket.ArchiveSegment] = errors.New("spaces is down")

		if ok := runOneTick(t, repo, files); ok {
			t.Fatal("a tick with a failed segment reported success")
		}
		if _, _, removed, _ := files.snapshot(); len(removed) != 1 {
			t.Fatalf("removed %v, want the healthy segment swept anyway", removed)
		}
	})
}

// TestFailedTickDoesNotMarkSuccess: /statusz reads LastSuccess, and a sweeper that keeps stamping
// "fine" while it fails is worse than no reporter at all.
func TestFailedTickDoesNotMarkSuccess(t *testing.T) {
	now := time.Now().UTC()
	repo, files := newFixture(now, nil, nil)
	repo.cards.err = errors.New("pool is closed")

	w := New(nil, repo, files)
	if w.runCleanup(context.Background()) {
		t.Fatal("failed tick reported success")
	}
	if !w.LastSuccess().IsZero() {
		t.Fatalf("failed tick stamped a success at %s", w.LastSuccess())
	}

	repo.cards.err = nil
	if !w.runCleanup(context.Background()) {
		t.Fatal("clean tick reported failure")
	}
	if w.LastSuccess().IsZero() {
		t.Fatal("clean tick left LastSuccess unset")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// LIFECYCLE — Stop must finish before the caller closes the DB.
// ─────────────────────────────────────────────────────────────────────────────

// TestStopWaitsForAnInFlightTick is the shutdown-order trap in executable form. app.Stop calls
// this Stop inside the workers block and closes the connection pool a few lines later; if Stop
// returned while a tick were still running, the expiry UPDATE would land on a closed pool. The
// test holds the worker inside a tick, calls Stop, and requires Stop to still be blocked.
func TestStopWaitsForAnInFlightTick(t *testing.T) {
	now := time.Now().UTC()
	repo, files := newFixture(now, nil, nil)
	files.entered = make(chan struct{}, 1)
	files.release = make(chan struct{})

	w := New(&Config{WorkerInterval: time.Millisecond}, repo, files)
	if err := w.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}

	select {
	case <-files.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the worker never reached its first tick")
	}

	stopped := make(chan struct{})
	go func() {
		_ = w.Stop()
		close(stopped)
	}()

	select {
	case <-stopped:
		t.Fatal("Stop returned while a tick was still running — app.Stop would close the DB under it")
	case <-time.After(100 * time.Millisecond):
	}

	close(files.release)

	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("Stop did not return after the tick finished")
	}

	// After Stop returns the caller is entitled to close the DB. Anything landing on the fakes
	// from here on is a call against a closed pool.
	repo.cards.mu.Lock()
	repo.cards.err = errors.New("the pool is closed now")
	callsAtStop := repo.cards.calls
	repo.cards.mu.Unlock()
	files.mu.Lock()
	segmentsAtStop := len(files.askedSegments)
	files.mu.Unlock()

	time.Sleep(50 * time.Millisecond)

	repo.cards.mu.Lock()
	callsAfter := repo.cards.calls
	repo.cards.mu.Unlock()
	files.mu.Lock()
	segmentsAfter := len(files.askedSegments)
	files.mu.Unlock()

	if callsAfter != callsAtStop || segmentsAfter != segmentsAtStop {
		t.Fatalf("the worker was still working after Stop returned: db calls %d→%d, listings %d→%d",
			callsAtStop, callsAfter, segmentsAtStop, segmentsAfter)
	}
}

// TestAppStopsThisWorkerBeforeClosingTheDB guards the OTHER half of the shutdown trap. The test
// above proves Stop waits for the goroutine; it can say nothing about WHERE app.Stop calls it, and
// a Stop moved below a.db.Close() would be just as fatal and just as silent — the worker would
// drain honestly, against a pool that is already gone.
//
// There is no way to observe that ordering from inside this package except by reading the wiring,
// so that is what this does. It is a source assertion and it is deliberately narrow: four call
// sites, two questions, and it stays true through any amount of editing around them.
//
// COMMENT LINES ARE DROPPED FIRST, and not as tidiness. The first draft of this test matched raw
// text and went red on a correct app.go, because the comment ABOVE the Stop call names a.db.Close()
// to explain why the call sits where it does. A guard that reads prose as code fails on the very
// documentation that makes it comprehensible.
func TestAppStopsThisWorkerBeforeClosingTheDB(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("..", "..", "app", "app.go"))
	if err != nil {
		t.Fatalf("read app wiring: %v", err)
	}
	// firstStatement returns the line number of the first non-comment line containing needle,
	// and how many such lines there are in total.
	firstStatement := func(needle string) (line, count int) {
		line = -1
		for i, raw := range strings.Split(string(src), "\n") {
			if strings.HasPrefix(strings.TrimSpace(raw), "//") || !strings.Contains(raw, needle) {
				continue
			}
			if line < 0 {
				line = i
			}
			count++
		}
		return line, count
	}

	stopAt, stopCount := firstStatement("a.acw.Stop()")
	if stopAt < 0 {
		t.Fatal("app.Stop never stops the archive cleanup worker: its tick would outlive shutdown")
	}
	if stopCount != 1 {
		t.Fatalf("app.go stops the archive cleanup worker %d times, want exactly 1", stopCount)
	}
	closeAt, _ := firstStatement("a.db.Close()")
	if closeAt < 0 {
		t.Fatal("app.go no longer closes the DB by that name — this guard needs rewriting, not deleting")
	}
	if stopAt > closeAt {
		t.Fatalf("app.Stop closes the DB (line %d) before stopping the archive cleanup worker "+
			"(line %d): the expiry UPDATE would run against a closed pool", closeAt+1, stopAt+1)
	}

	startAt, _ := firstStatement("a.acw.Start(ctx)")
	if startAt < 0 {
		t.Fatal("app.Start never starts the archive cleanup worker: nothing would ever expire")
	}
	// The worker needs the bucket as well as the DB, so it cannot be constructed before
	// bucket.New has run — a nil FileStore would panic on the first tick, an hour after boot.
	bucketAt, _ := firstStatement("a.b, err = bucket.New(")
	if bucketAt < 0 {
		t.Fatal("app.go no longer creates the bucket by that name — this guard needs rewriting")
	}
	if startAt < bucketAt {
		t.Fatalf("the archive cleanup worker is wired (line %d) before the bucket exists (line %d)",
			startAt+1, bucketAt+1)
	}
}

func TestStopIsRefusedTwice(t *testing.T) {
	repo, files := newFixture(time.Now().UTC(), nil, nil)
	w := New(nil, repo, files)

	if err := w.Stop(); err == nil {
		t.Fatal("Stop on a worker that never started returned nil")
	}
	if err := w.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	if err := w.Start(context.Background()); err == nil {
		t.Fatal("a second Start returned nil")
	}
	if err := w.Stop(); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if err := w.Stop(); err == nil {
		t.Fatal("a second Stop returned nil")
	}
}

// ─────────────────────────────────────────────────────────────────────────────

func TestDefaultsAreTheOwnersDecision(t *testing.T) {
	if got := DefaultConfig().WorkerInterval; got != time.Hour {
		t.Fatalf("worker interval %s, want hourly", got)
	}
	repo, files := newFixture(time.Now().UTC(), nil, nil)
	if got := New(&Config{}, repo, files).c.WorkerInterval; got != time.Hour {
		t.Fatalf("a zero interval became %s, want the default hour", got)
	}
	if got := New(nil, repo, files).c.WorkerInterval; got != time.Hour {
		t.Fatalf("a nil config gave %s, want the default hour", got)
	}
	if want := []string{bucket.ArchiveSegment, bucket.ImportSegment}; !equalStrings(cleanableSegments, want) {
		t.Fatalf("cleanable segments %v, want %v", cleanableSegments, want)
	}
}

func TestBackoffGrowsAndIsCapped(t *testing.T) {
	if got := backoffDelay(1); got != backoffBase {
		t.Fatalf("first backoff %s, want %s", got, backoffBase)
	}
	if got := backoffDelay(2); got != 2*backoffBase {
		t.Fatalf("second backoff %s, want %s", got, 2*backoffBase)
	}
	if got := backoffDelay(50); got != backoffMax {
		t.Fatalf("backoff after 50 failures %s, want the cap %s", got, backoffMax)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
