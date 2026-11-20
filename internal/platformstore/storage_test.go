package platformstore_test

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestTeamStorageReserveAndCap(t *testing.T) {
	st := openFresh(t)
	ctx := context.Background()

	// the migration leaves team #1 counted with no cap
	got, err := st.StorageFor(ctx, []int64{1})
	if err != nil {
		t.Fatal(err)
	}
	if one := got[1]; !one.Cap.Valid || one.Cap.Int64 != 0 || one.EffectiveCap(100) != 0 {
		t.Fatalf("team #1 = %+v", one)
	}

	// team 2 has no row: the default cap (100) applies, the row is made on the first reserve
	if ok, err := st.ReserveStorage(ctx, 2, 60, 100); err != nil || !ok {
		t.Fatalf("reserve 60: %v %v", ok, err)
	}
	if ok, err := st.ReserveStorage(ctx, 2, 41, 100); err != nil || ok {
		t.Fatalf("reserve 41 over 100 must be refused: %v %v", ok, err)
	}
	if ok, err := st.ReserveStorage(ctx, 2, 40, 100); err != nil || !ok {
		t.Fatalf("reserve 40 up to the cap: %v %v", ok, err)
	}
	if ok, err := st.ReserveStorage(ctx, 2, 0, 100); err != nil || ok {
		t.Fatalf("a stream at the cap must be refused: %v %v", ok, err)
	}
	// a delete frees room; the count never goes below zero
	if err := st.AddStorage(ctx, 2, -30); err != nil {
		t.Fatal(err)
	}
	if ok, err := st.ReserveStorage(ctx, 2, 0, 100); err != nil || !ok {
		t.Fatalf("a stream under the cap: %v %v", ok, err)
	}
	if err := st.AddStorage(ctx, 2, -1000); err != nil {
		t.Fatal(err)
	}
	if got, _ := st.StorageFor(ctx, []int64{2}); got[2].Used != 0 {
		t.Fatalf("floor: %+v", got[2])
	}

	// the operator's cap wins over the default; 0 = no cap
	if err := st.SetStorageCap(ctx, 2, 10); err != nil {
		t.Fatal(err)
	}
	if ok, _ := st.ReserveStorage(ctx, 2, 11, 100); ok {
		t.Fatal("own cap 10 must refuse 11")
	}
	if err := st.SetStorageCap(ctx, 2, 0); err != nil {
		t.Fatal(err)
	}
	if ok, _ := st.ReserveStorage(ctx, 2, 1<<40, 100); !ok {
		t.Fatal("no cap must admit anything")
	}

	// recount overwrites the number
	if err := st.SetRecountedStorage(ctx, map[int64]int64{2: 5, 3: 9}, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	got, _ = st.StorageFor(ctx, []int64{2, 3, 4})
	if got[2].Used != 5 || got[3].Used != 9 {
		t.Fatalf("recount: %+v", got)
	}
	if _, ok := got[4]; ok {
		t.Fatal("a team without a row must be absent")
	}
}

// Parallel uploads never pass the last free bytes together.
func TestTeamStorageReserveIsAtomic(t *testing.T) {
	st := openFresh(t)
	ctx := context.Background()
	var admitted atomic.Int64
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if ok, err := st.ReserveStorage(ctx, 7, 10, 55); err == nil && ok {
				admitted.Add(1)
			}
		}()
	}
	wg.Wait()
	if n := admitted.Load(); n != 5 {
		t.Fatalf("admitted %d × 10 bytes under a 55-byte cap, want 5", n)
	}
	if got, _ := st.StorageFor(ctx, []int64{7}); got[7].Used != 50 {
		t.Fatalf("used = %d", got[7].Used)
	}
}

func TestStorageRecountLockOneHolder(t *testing.T) {
	st := openFresh(t)
	ctx := context.Background()
	release, ok, err := st.LockStorageRecount(ctx)
	if err != nil || !ok {
		t.Fatalf("first: %v %v", ok, err)
	}
	if _, ok2, err := st.LockStorageRecount(ctx); err != nil || ok2 {
		t.Fatalf("second holder must be refused: %v %v", ok2, err)
	}
	release()
	release2, ok, err := st.LockStorageRecount(ctx)
	if err != nil || !ok {
		t.Fatalf("after release: %v %v", ok, err)
	}
	release2()
}

func TestStorageCapBackToDefault(t *testing.T) {
	st := openFresh(t)
	ctx := context.Background()
	if err := st.SetStorageCap(ctx, 2, 10); err != nil {
		t.Fatal(err)
	}
	if err := st.ClearStorageCap(ctx, 2); err != nil {
		t.Fatal(err)
	}
	got, _ := st.StorageFor(ctx, []int64{2})
	if got[2].Cap.Valid || got[2].EffectiveCap(77) != 77 {
		t.Fatalf("cap = %+v", got[2])
	}
}
