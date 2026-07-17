package store

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/stretchr/testify/require"
)

// TestFiberDictionaryCRUD is the acceptance test for the fibre vocabulary CRUD (S17/P0.4, migrations
// 0167/0177/0180): CreateFiber normalises the code and surfaces the entry in the dictionary read,
// ArchiveFiber flips it to archived (dropped from the active list, still present-and-flagged in the
// full read), and the optimistic expected_version guards concurrent writers.
func TestFiberDictionaryCRUD(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	cfg := *testCfg
	cfg.Automigrate = true
	s, err := NewForTest(ctx, cfg)
	require.NoError(t, err)
	defer s.Close()
	d := s.Dictionary()

	// Unique upper-case alnum code (<=8 chars) so reruns against a persistent DB don't collide on PK.
	code := fmt.Sprintf("Z%d", time.Now().UnixNano()%10000000)
	t.Cleanup(func() { _, _ = testDB.ExecContext(ctx, `DELETE FROM fiber WHERE code = ?`, code) })

	// Create: a lower-case input is normalised to the canonical upper-case code, revision advances.
	f, rev, err := d.CreateFiber(ctx, "z"+code[1:], "Test Fibre", 0)
	require.NoError(t, err)
	require.Equal(t, code, f.Code, "code is upper-cased/trimmed")
	require.Equal(t, "Test Fibre", f.Name)
	require.Greater(t, rev, int64(0))

	// The active list contains it, un-archived.
	fibers, err := d.ListFibers(ctx, false)
	require.NoError(t, err)
	require.True(t, containsFiber(fibers, code, false), "new fibre is in the active list, not archived")

	// GetDictionaryInfo (the payload behind admin GetDictionary) contains it too.
	di, err := s.Cache().GetDictionaryInfo(ctx)
	require.NoError(t, err)
	require.True(t, containsFiber(di.Fibers, code, false), "new fibre is in the dictionary payload")

	// A stale expected_version is rejected (optimistic concurrency on the namespace revision).
	_, _, err = d.CreateFiber(ctx, code+"X", "Other", 999999)
	require.Error(t, err, "stale expected_version must be rejected")

	// Archive: revision advances again.
	rev2, err := d.ArchiveFiber(ctx, code, 0)
	require.NoError(t, err)
	require.Greater(t, rev2, rev)

	// The active list no longer contains it; the full list carries it flagged archived.
	active, err := d.ListFibers(ctx, false)
	require.NoError(t, err)
	require.False(t, containsFiber(active, code, false), "archived fibre is dropped from the active list")
	all, err := d.ListFibers(ctx, true)
	require.NoError(t, err)
	require.True(t, containsFiber(all, code, true), "archived fibre is present-and-flagged in the full list")

	// The dictionary payload keeps it, now flagged archived (client filters for pickers).
	di2, err := s.Cache().GetDictionaryInfo(ctx)
	require.NoError(t, err)
	require.True(t, containsFiber(di2.Fibers, code, true), "dictionary payload flags the fibre archived")

	// Archiving an already-archived fibre is a no-op error (nothing to flip).
	_, err = d.ArchiveFiber(ctx, code, 0)
	require.Error(t, err, "re-archiving an archived fibre must fail")
}

func containsFiber(fibers []entity.Fiber, code string, wantArchived bool) bool {
	for _, f := range fibers {
		if f.Code == code {
			return f.ArchivedAt.Valid == wantArchived
		}
	}
	return false
}
