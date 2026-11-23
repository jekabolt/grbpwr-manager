package teamwipe

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/bucket"
	"github.com/jekabolt/grbpwr-manager/internal/platformstore"
	"github.com/jekabolt/grbpwr-manager/internal/store"
)

func TestParseMode(t *testing.T) {
	for in, want := range map[string]Mode{"": ModeDry, "dry": ModeDry, " ON ": ModeOn, "off": ModeOff, "Off": ModeOff,
		"yes": ModeDry, "true": ModeDry, "1": ModeDry} {
		if got := ParseMode(in); got != want {
			t.Errorf("ParseMode(%q) = %s, want %s", in, got, want)
		}
	}
}

// fake is the control plane, the bucket and the database server in one; calls logs every call.
type fake struct {
	calls     []string
	due       []int64
	open      []platformstore.TeamWipe
	claimable map[int64]platformstore.TeamWipe
	leaseOK   bool
	loseAfter int // ExtendLease fails with ErrLeaseLost from this call on (0 = never)
	extends   int
	filesDone bool
	dropRes   store.DropResult
	attempts  int
	refs      bool
	fkRefs    bool
	notTomb   bool
	fileErr   error // PreflightTeamFiles
	dropErr   error // CheckDrop
}

func (f *fake) log(s string) { f.calls = append(f.calls, s) }

func (f *fake) DueAccountPurges(context.Context, int) ([]platformstore.DueAccount, error) {
	f.log("dueAccounts")
	return []platformstore.DueAccount{{ID: 5, Handle: "cid"}}, nil
}

func (f *fake) PurgeDeletedAccounts(_ context.Context, _ int, unusable func() (string, error)) (int, int, error) {
	f.log("purgeAccounts")
	if _, err := unusable(); err != nil {
		return 0, 0, err
	}
	return 1, 1, nil
}

func (f *fake) AcquireLease(context.Context, string, time.Duration) (string, bool, error) {
	f.log("acquire")
	return "tok", f.leaseOK, nil
}

func (f *fake) ExtendLease(context.Context, string, string, time.Duration) error {
	f.extends++
	if f.loseAfter > 0 && f.extends >= f.loseAfter {
		return platformstore.ErrLeaseLost
	}
	return nil
}

func (f *fake) ReleaseLease(context.Context, string, string) error { f.log("release"); return nil }

func (f *fake) DueTeamWipes(context.Context, int) ([]int64, error) {
	f.log("dueTeams")
	return f.due, nil
}

func (f *fake) OpenTeamWipes(context.Context, int) ([]platformstore.TeamWipe, error) {
	f.log("open")
	return f.open, nil
}

func (f *fake) ClaimTeamWipe(_ context.Context, id int64, _ time.Time) (platformstore.TeamWipe, bool, error) {
	f.log("claim")
	if id == 1 {
		return platformstore.TeamWipe{}, false, platformstore.ErrTeamOne
	}
	w, ok := f.claimable[id]
	return w, ok, nil
}

func (f *fake) MarkWipeFilesDone(context.Context, int64, int64, time.Time) error {
	f.log("filesDone")
	return nil
}

func (f *fake) AddWipeObjects(context.Context, int64, int64) error { f.log("addObjects"); return nil }

func (f *fake) MarkWipeDBDropped(context.Context, int64, time.Time) error {
	f.log("dbDropped")
	return nil
}

func (f *fake) MarkWipeFailed(_ context.Context, _ int64, reason string, count bool, _ time.Time) (int, error) {
	if count {
		f.attempts++
	}
	f.log("failed:" + strings.SplitN(reason, ":", 2)[0])
	return f.attempts, nil
}

func (f *fake) DBNameReferenced(context.Context, string) (bool, error) { return f.refs, nil }

func (f *fake) FileKeyReferenced(context.Context, string) (bool, error) {
	f.log("fkRefs")
	return f.fkRefs, nil
}

func (f *fake) TeamWipeTombstone(context.Context, int64) (bool, error) {
	f.log("tomb")
	return !f.notTomb, nil
}

func (f *fake) PreflightTeamFiles(context.Context, string) error {
	f.log("preflightFiles")
	return f.fileErr
}

func (f *fake) CheckDrop(string) error {
	f.log("checkDrop")
	return f.dropErr
}

func (f *fake) FinishTeamWipe(context.Context, int64, time.Time) (platformstore.WipedTeam, error) {
	f.log("finish")
	return platformstore.WipedTeam{Slug: "xd3-test", Finished: true}, nil
}

func (f *fake) PreviewTeamWipe(_ context.Context, id int64) (platformstore.TeamWipePreview, error) {
	f.log("preview")
	return platformstore.TeamWipePreview{TeamID: id, Slug: "xd3-test", DBName: "lkb_t_abcd1234", FileKey: "fk"}, nil
}

func (f *fake) WipeTeamFiles(context.Context, string, int) (bucket.WipeStats, error) {
	f.log("wipeFiles")
	return bucket.WipeStats{Objects: 2, Done: f.filesDone}, nil
}

func (f *fake) PreviewTeamFiles(context.Context, string) (bucket.WipeStats, error) {
	f.log("previewFiles")
	return bucket.WipeStats{Objects: 2}, nil
}

func (f *fake) Drop(context.Context, string) (store.DropResult, error) {
	f.log("drop")
	return f.dropRes, nil
}

func (f *fake) Size(context.Context, string) (bool, int64, error) { f.log("size"); return true, 1, nil }

func wipeOf(id int64) platformstore.TeamWipe {
	return platformstore.TeamWipe{TeamID: id, Slug: "xd3-test",
		DBName: sql.NullString{String: "lkb_t_abcd1234", Valid: true}, FileKey: sql.NullString{String: "fk", Valid: true}}
}

func newFake() *fake {
	return &fake{leaseOK: true, filesDone: true, due: []int64{1, 8}, claimable: map[int64]platformstore.TeamWipe{8: wipeOf(8)}}
}

func run(mode Mode, f *fake) {
	w := New(mode, f, f, f, func() (string, error) { return "!", nil }, time.Hour, time.Hour)
	w.Tick(context.Background())
}

func joined(f *fake) string { return strings.Join(f.calls, ",") }

func TestTickOnOrder(t *testing.T) {
	f := newFake()
	run(ModeOn, f)
	want := "purgeAccounts,acquire,open,dueTeams,preview,preflightFiles,checkDrop,claim,tomb,fkRefs,wipeFiles,filesDone,drop,dbDropped,finish,release"
	if got := joined(f); got != want {
		t.Fatalf("calls\n got %s\nwant %s", got, want)
	}
}

func TestTickOffAndDryWriteNothing(t *testing.T) {
	f := newFake()
	run(ModeOff, f)
	if len(f.calls) != 0 {
		t.Fatalf("off made calls: %v", f.calls)
	}
	w := New(ModeOff, f, f, f, nil, time.Millisecond, time.Millisecond)
	w.Start(context.Background())
	time.Sleep(20 * time.Millisecond)
	w.Stop()
	if len(f.calls) != 0 {
		t.Fatalf("off loop ran: %v", f.calls)
	}

	f = newFake()
	run(ModeDry, f)
	for _, c := range f.calls {
		switch c {
		case "dueAccounts", "dueTeams", "preview", "size", "previewFiles", "preflightFiles", "checkDrop":
		default:
			t.Fatalf("dry made a writing call %q (%s)", c, joined(f))
		}
	}
	if !strings.Contains(joined(f), "dueAccounts,dueTeams") {
		t.Fatalf("dry order %s", joined(f))
	}
	if strings.Count(joined(f), "preview,") != 1 {
		t.Fatalf("dry previewed team #1: %s", joined(f))
	}
}

func TestTickLease(t *testing.T) {
	f := newFake()
	f.leaseOK = false
	run(ModeOn, f)
	if strings.Contains(joined(f), "claim") || strings.Contains(joined(f), "release") {
		t.Fatalf("worked without the lease: %s", joined(f))
	}
	// Lost before the database step: the pass stops, nothing dropped, nothing finished.
	f = newFake()
	f.loseAfter = 3 // 1: before claim, 2: runWipe start, 3: after the file step
	run(ModeOn, f)
	if got := joined(f); strings.Contains(got, "drop") || strings.Contains(got, "finish") || !strings.HasSuffix(got, "release") {
		t.Fatalf("lease lost: %s", got)
	}
}

func TestTickResumeAndRetry(t *testing.T) {
	// Resume after a crash: files were done, the database not; claim nothing new.
	f := newFake()
	w := wipeOf(8)
	w.FilesDoneAt = sql.NullTime{Time: time.Now(), Valid: true}
	f.open, f.due = []platformstore.TeamWipe{w}, nil
	run(ModeOn, f)
	if got := joined(f); got != "purgeAccounts,acquire,open,drop,dbDropped,finish,dueTeams,release" {
		t.Fatalf("resume: %s", got)
	}
	// A busy named lock is retried without counting an attempt.
	f = newFake()
	f.dropRes = store.DropBusy
	run(ModeOn, f)
	if got := joined(f); !strings.Contains(got, "drop,failed:database") || strings.Contains(got, "finish") || f.attempts != 0 {
		t.Fatalf("busy: %s attempts %d", got, f.attempts)
	}
	// A database still named somewhere is never dropped.
	f = newFake()
	f.refs = true
	run(ModeOn, f)
	if got := joined(f); strings.Contains(got, "drop") || !strings.Contains(got, "failed:database") || f.attempts != 1 {
		t.Fatalf("referenced: %s", got)
	}
	// A big prefix: progress recorded, the database waits for the next pass.
	f = newFake()
	f.filesDone = false
	run(ModeOn, f)
	if got := joined(f); strings.Count(got, "wipeFiles") != fileCallsPerStep || !strings.Contains(got, "addObjects") ||
		strings.Contains(got, "drop") || strings.Contains(got, "failed") {
		t.Fatalf("big prefix: %s", got)
	}
}

func TestTickNoFileStore(t *testing.T) {
	// A wipe claimed before (resumed) fails its file step; a due team is not claimed (preflight).
	f := newFake()
	f.open = []platformstore.TeamWipe{wipeOf(8)}
	w := New(ModeOn, f, nil, f, func() (string, error) { return "!", nil }, time.Hour, time.Hour)
	w.Tick(context.Background())
	if got := joined(f); !strings.Contains(got, "failed:files") || strings.Contains(got, "drop") {
		t.Fatalf("no file store: %s", got)
	}
	if got := joined(f); strings.Contains(got, "claim") {
		t.Fatalf("no file store: claimed: %s", got)
	}
}

// A due team whose steps would fail on its own values is never claimed: it stays restorable.
func TestTickPreflightSkipsClaim(t *testing.T) {
	for name, set := range map[string]func(*fake){
		"files":    func(f *fake) { f.fileErr = bucket.ErrWipePrefix },
		"database": func(f *fake) { f.dropErr = errors.New("platform database") },
	} {
		f := newFake()
		set(f)
		run(ModeOn, f)
		if got := joined(f); strings.Contains(got, "claim") || strings.Contains(got, "wipeFiles") ||
			strings.Contains(got, "drop") || strings.Contains(got, "failed") {
			t.Fatalf("%s: claimed after a failed preflight: %s", name, got)
		}
	}
	// No file store or no database access: the same, before any claim.
	f := newFake()
	New(ModeOn, f, nil, f, func() (string, error) { return "!", nil }, time.Hour, time.Hour).Tick(context.Background())
	if got := joined(f); strings.Contains(got, "claim") {
		t.Fatalf("claimed without a file store: %s", got)
	}
	f = newFake()
	New(ModeOn, f, f, nil, func() (string, error) { return "!", nil }, time.Hour, time.Hour).Tick(context.Background())
	if got := joined(f); strings.Contains(got, "claim") {
		t.Fatalf("claimed without database access: %s", got)
	}
}

// The file step deletes nothing when the team row is no longer the claimed tombstone or a team row
// names the file key again; the step fails and counts an attempt, the database waits.
func TestTickFilesFence(t *testing.T) {
	for name, set := range map[string]func(*fake){
		"not a tombstone": func(f *fake) { f.notTomb = true },
		"key referenced":  func(f *fake) { f.fkRefs = true },
	} {
		f := newFake()
		set(f)
		run(ModeOn, f)
		if got := joined(f); strings.Contains(got, "wipeFiles") || strings.Contains(got, "drop") ||
			strings.Contains(got, "finish") || !strings.Contains(got, "failed:files") || f.attempts != 1 {
			t.Fatalf("%s: %s attempts %d", name, got, f.attempts)
		}
	}
	// Checked before every delete call, not only the first.
	f := newFake()
	f.filesDone = false
	run(ModeOn, f)
	if got := joined(f); strings.Count(got, "tomb,fkRefs,wipeFiles") != fileCallsPerStep {
		t.Fatalf("fence per call: %s", got)
	}
}
