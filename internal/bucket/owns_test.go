package bucket

import (
	"context"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/tenant"
	"github.com/stretchr/testify/require"
)

// The signed-out /api/p route presigns a key named by a client-saved card: a key the link's team does
// not own is never signed. Team #1's legacy keys (no team prefix) still sign, byte for byte as before.
func TestPresignPatternObjectRefusesForeignKey(t *testing.T) {
	b := offlinePresignBucket(t)
	b.Config.BaseFolder = "probe"
	legacy := "probe/tech-card-patterns/2026/jan/a-x.pdf"
	teamTwo := "probe/t/" + fileKeyA + "/tech-card-patterns/2026/jan/b-x.pdf"
	two := teamCtx(2, fileKeyA)
	one := tenant.With(context.Background(), tenant.TeamOne("grbpwr"))

	// Teams mode off: today's behaviour, every key signs.
	_, _, err := b.PresignPatternObject(context.Background(), legacy, false, "")
	require.NoError(t, err)

	b.Config.TeamsMode = true
	_, _, err = b.PresignPatternObject(one, legacy, false, "")
	require.NoError(t, err, "team #1 legacy key still signs")
	_, _, err = b.PresignPatternObject(two, teamTwo, false, "")
	require.NoError(t, err, "a team signs its own key")

	_, _, err = b.PresignPatternObject(two, legacy, false, "")
	require.ErrorIs(t, err, ErrNotTeamObject, "team 2 must not sign team #1's key")
	_, _, err = b.PresignPatternObject(one, teamTwo, false, "")
	require.ErrorIs(t, err, ErrNotTeamObject, "team #1 must not sign team 2's key")
	_, _, err = b.PresignPatternObject(teamCtx(3, "fedcba9876543210fedcba9876543210"), teamTwo, false, "")
	require.ErrorIs(t, err, ErrNotTeamObject, "team 3 must not sign team 2's key")

	require.True(t, b.OwnsObjectKey(two, teamTwo))
	require.False(t, b.OwnsObjectKey(two, legacy))
	require.False(t, (*Bucket)(nil).OwnsObjectKey(two, teamTwo))
}
