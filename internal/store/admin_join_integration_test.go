package store

import (
	"context"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/stretchr/testify/require"
)

// TestAdminJoinMarker: a team invite's staged admins row (join_pending) is finished only by the
// compare-and-set, and any explicit disable clears the marker in the same write, so a late or
// repeated finish never re-enables a row someone disabled.
func TestAdminJoinMarker(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cfg := *testCfg
	cfg.Automigrate = true
	s, err := NewForTest(ctx, cfg)
	require.NoError(t, err)
	defer s.Close()

	joins, ok := s.Admin().(interface {
		StageJoin(ctx context.Context, username, pwHash string, isSuper bool, perms []entity.AdminPermission) error
		FinishJoin(ctx context.Context, username string) (bool, error)
	})
	require.True(t, ok, "the admin store stages team joins")
	const user = "join-marker-test"
	t.Cleanup(func() { _ = s.Admin().DeleteAdmin(context.Background(), user) })
	read := func() *entity.AdminAccount {
		t.Helper()
		acc, err := s.Admin().GetAccountWithPermissions(ctx, user)
		require.NoError(t, err)
		return acc
	}
	write := []entity.AdminPermission{{Section: "products", Access: entity.AccessWrite}}

	// A new row is staged disabled with the marker and the invite's rights.
	require.NoError(t, joins.StageJoin(ctx, user, "unusable-hash", false, write))
	acc := read()
	require.True(t, acc.Disabled)
	require.True(t, acc.JoinPending)
	require.Equal(t, write, acc.Permissions)

	// The finish enables it once; a second finish changes nothing.
	done, err := joins.FinishJoin(ctx, user)
	require.NoError(t, err)
	require.True(t, done)
	acc = read()
	require.False(t, acc.Disabled)
	require.False(t, acc.JoinPending)
	done, err = joins.FinishJoin(ctx, user)
	require.NoError(t, err)
	require.False(t, done)

	// Re-staged (a later invite after a removal): disabled again, rights replaced.
	read2 := []entity.AdminPermission{{Section: "orders", Access: entity.AccessRead}}
	require.NoError(t, joins.StageJoin(ctx, user, "ignored", false, read2))
	acc = read()
	require.True(t, acc.Disabled)
	require.True(t, acc.JoinPending)
	require.Equal(t, read2, acc.Permissions)
	require.Equal(t, "unusable-hash", acc.PasswordHash)

	// An admin (or a removal) disables the staged row: the marker goes, the finish refuses.
	require.NoError(t, s.Admin().SetAccountDisabled(ctx, user, true))
	acc = read()
	require.True(t, acc.Disabled)
	require.False(t, acc.JoinPending)
	done, err = joins.FinishJoin(ctx, user)
	require.NoError(t, err)
	require.False(t, done)
	require.True(t, read().Disabled)
}
