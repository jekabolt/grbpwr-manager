package admin

import (
	"context"
	"testing"

	authsrv "github.com/jekabolt/grbpwr-manager/internal/apisrv/auth"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/stretchr/testify/require"
)

func TestStampColorwayDevelopmentActor(t *testing.T) {
	ctx := authsrv.PutAdminUsername(context.Background(), "authenticated-admin")
	patch := &entity.ColorwayDevelopmentPatch{}

	require.Same(t, patch, stampColorwayDevelopmentActor(ctx, patch))
	require.Equal(t, "authenticated-admin", patch.Actor)
	require.Nil(t, stampColorwayDevelopmentActor(ctx, nil))
}
