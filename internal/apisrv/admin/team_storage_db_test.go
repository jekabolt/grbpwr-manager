package admin

import (
	"context"
	"testing"

	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	"github.com/stretchr/testify/require"
)

// GetTeamStorage against a real control plane: any member reads the team's own used / cap.
func TestGetTeamStorage(t *testing.T) {
	// teams mode off: nothing counted, nothing capped
	res, err := (&Server{}).GetTeamStorage(context.Background(), &pb_admin.GetTeamStorageRequest{})
	require.NoError(t, err)
	require.Zero(t, res.GetUsedBytes())
	require.Zero(t, res.GetCapBytes())

	w := newWorkspaceWorld(t)
	bob := w.as("bob", 102, "member")
	ctx := context.Background()

	res, err = w.s.GetTeamStorage(bob, &pb_admin.GetTeamStorageRequest{})
	require.NoError(t, err)
	require.Equal(t, int64(20<<30), res.GetCapBytes(), "default cap")
	require.False(t, res.GetFull())

	require.NoError(t, w.st.SetStorageCap(ctx, 2, 100))
	require.NoError(t, w.st.AddStorage(ctx, 2, 100))
	res, err = w.s.GetTeamStorage(bob, &pb_admin.GetTeamStorageRequest{})
	require.NoError(t, err)
	require.Equal(t, int64(100), res.GetUsedBytes())
	require.Equal(t, int64(100), res.GetCapBytes())
	require.True(t, res.GetFull())

	require.NoError(t, w.st.SetStorageCap(ctx, 2, 0))
	res, err = w.s.GetTeamStorage(bob, &pb_admin.GetTeamStorageRequest{})
	require.NoError(t, err)
	require.Zero(t, res.GetCapBytes())
	require.False(t, res.GetFull(), "no cap is never full")
}
