package storagemeter

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/bucket"
	"github.com/jekabolt/grbpwr-manager/internal/platformstore"
)

type fakeStore struct {
	ok       bool
	err      error
	defCap   int64
	stream   int64
	teams    []platformstore.StorageTeam
	written  map[int64]int64
	busy     bool
	locked   int
	released int
}

func (f *fakeStore) ReserveStorage(_ context.Context, _ int64, n, defCap int64) (bool, error) {
	f.stream, f.defCap = n, defCap
	return f.ok, f.err
}
func (f *fakeStore) AddStorage(context.Context, int64, int64) error { return nil }
func (f *fakeStore) StorageTeams(context.Context) ([]platformstore.StorageTeam, error) {
	return f.teams, nil
}
func (f *fakeStore) SetRecountedStorage(_ context.Context, used map[int64]int64, _ time.Time) error {
	f.written = used
	return nil
}

func (f *fakeStore) LockStorageRecount(context.Context) (func(), bool, error) {
	if f.busy {
		return nil, false, nil
	}
	f.locked++
	return func() { f.released++ }, true, nil
}

type fakeLister struct{ t bucket.StorageTotals }

func (f fakeLister) SumByTeam(context.Context) (bucket.StorageTotals, error) { return f.t, nil }

func TestMeterReserve(t *testing.T) {
	st := &fakeStore{ok: true}
	m := New(st, 42)
	if err := m.Reserve(context.Background(), 2, 10, true); err != nil || st.stream != 0 || st.defCap != 42 {
		t.Fatalf("stream reserve: %v n=%d cap=%d", err, st.stream, st.defCap)
	}
	st.ok = false
	if err := m.Reserve(context.Background(), 2, 10, false); !errors.Is(err, bucket.ErrStorageFull) {
		t.Fatalf("full = %v", err)
	}
	st.err = errors.New("db")
	if err := m.Reserve(context.Background(), 2, 10, false); err == nil || errors.Is(err, bucket.ErrStorageFull) {
		t.Fatalf("db error must stay a db error: %v", err)
	}
}

func TestRecount(t *testing.T) {
	st := &fakeStore{teams: []platformstore.StorageTeam{
		{ID: 1},
		{ID: 2, FileKey: sql.NullString{String: "aa", Valid: true}},
		{ID: 3, FileKey: sql.NullString{String: "bb", Valid: true}},
		{ID: 4}, // waiting for approval: no file key yet
	}}
	ls := fakeLister{bucket.StorageTotals{TeamOne: 100, ByFileKey: map[string]int64{"aa": 7, "zz": 9}}}
	if _, err := Recount(context.Background(), st, ls, time.Now()); err != nil {
		t.Fatal(err)
	}
	want := map[int64]int64{1: 100, 2: 7, 3: 0, 4: 0}
	for id, n := range want {
		if st.written[id] != n {
			t.Fatalf("team %d = %d, want %d (%v)", id, st.written[id], n, st.written)
		}
	}
	if len(st.written) != len(want) {
		t.Fatalf("written %v", st.written)
	}
	if st.locked != 1 || st.released != 1 {
		t.Fatalf("lock taken %d, released %d", st.locked, st.released)
	}
	// another instance holds the lock: nothing is listed or written
	st2 := &fakeStore{busy: true}
	if _, err := Recount(context.Background(), st2, ls, time.Now()); !errors.Is(err, ErrRecountBusy) || st2.written != nil {
		t.Fatalf("busy: %v %v", err, st2.written)
	}
}
