package store

import (
	"context"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/stretchr/testify/require"
)

// TestMaterialOptimisticLock is the acceptance test for the material catalog optimistic lock
// (WS3 / S25, migration 0156): UpdateMaterial requires the caller to echo the lock_version it
// read, bumps it on success, and rejects a stale echo with entity.ErrMaterialConflict — a
// concurrent editor can no longer silently overwrite another's change.
func TestMaterialOptimisticLock(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	cfg := *testCfg
	cfg.Automigrate = true
	s, err := NewForTest(ctx, cfg)
	require.NoError(t, err)
	defer s.Close()
	tc := s.TechCards()

	id, err := tc.CreateMaterial(ctx, &entity.MaterialInsert{Name: "Lock Fabric", Section: "fabric"})
	require.NoError(t, err)

	// A fresh material starts at lock_version 0.
	m, err := tc.GetMaterial(ctx, id)
	require.NoError(t, err)
	require.Equal(t, 0, m.LockVersion)

	// Update with the version we read succeeds and bumps the version.
	require.NoError(t, tc.UpdateMaterial(ctx, id, &entity.MaterialInsert{Name: "Lock Fabric v2", Section: "fabric"}, 0))
	m, err = tc.GetMaterial(ctx, id)
	require.NoError(t, err)
	require.Equal(t, 1, m.LockVersion)
	require.Equal(t, "Lock Fabric v2", m.Name)

	// A stale echo (0, when the row is now at 1) is rejected as a conflict and mutates nothing.
	err = tc.UpdateMaterial(ctx, id, &entity.MaterialInsert{Name: "stale write", Section: "fabric"}, 0)
	require.ErrorIs(t, err, entity.ErrMaterialConflict)
	m, err = tc.GetMaterial(ctx, id)
	require.NoError(t, err)
	require.Equal(t, "Lock Fabric v2", m.Name, "rejected conflict must not overwrite")
	require.Equal(t, 1, m.LockVersion)

	// Echoing the current version succeeds again.
	require.NoError(t, tc.UpdateMaterial(ctx, id, &entity.MaterialInsert{Name: "Lock Fabric v3", Section: "fabric"}, 1))

	// A non-existent material reports NotFound, not a raw 500.
	err = tc.UpdateMaterial(ctx, 0x7FFFFFF0, &entity.MaterialInsert{Name: "ghost", Section: "fabric"}, 0)
	require.ErrorIs(t, err, entity.ErrMaterialNotFound)
	_, err = tc.GetMaterial(ctx, 0x7FFFFFF0)
	require.ErrorIs(t, err, entity.ErrMaterialNotFound)
}
