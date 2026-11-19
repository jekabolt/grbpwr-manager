package admin

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/jekabolt/grbpwr-manager/internal/tenant"
)

// Two teams, the same local tech card and the same admin name: neither joins the other's flight nor
// reads the other's cached answer, so each team's call is paid — and recorded — in that team.
func TestAIFlightsAndCachesAreKeyedByTeam(t *testing.T) {
	inTeam := func(id int64) context.Context {
		return tenant.With(adminCtx("alice"), tenant.Team{ID: id, Slug: "t", Status: tenant.StatusActive})
	}

	// Concurrent: team 2's call is parked; team 3's identical press must not join it.
	entered, release := make(chan struct{}, 4), make(chan struct{})
	rig := newSKRig(t, func() { entered <- struct{}{}; <-release }, ppReply(skeletonAIGoodAnswer, 0.01))
	var wg sync.WaitGroup
	errs := make([]error, 2)
	press := func(i int, ctx context.Context) {
		defer wg.Done()
		_, errs[i] = rig.s.SuggestAssemblySkeleton(ctx, skeletonAITestRequest())
	}
	wg.Add(2)
	go press(0, inTeam(2))
	<-entered
	go press(1, inTeam(3))
	joined := false
	select {
	case <-entered: // team 3 reached the provider on its own flight
	case <-time.After(2 * time.Second):
		joined = true
	}
	close(release)
	wg.Wait()
	require.False(t, joined, "team 3 joined team 2's flight")
	require.NoError(t, errs[0])
	require.NoError(t, errs[1])
	require.Equal(t, 2, rig.providerCalls(), "each team pays its own call")

	// Cache: team 4 does not get the answer teams 2 and 3 cached; team 2 does.
	rig2 := newSKRig(t, nil, ppReply(skeletonAIGoodAnswer, 0.01))
	for _, id := range []int64{2, 4, 2} {
		_, err := rig2.s.SuggestAssemblySkeleton(inTeam(id), skeletonAITestRequest())
		require.NoError(t, err)
	}
	require.Equal(t, 2, rig2.providerCalls(), "team 4 misses team 2's cache; team 2's second press hits it")

	// Teams mode off (team #1 or no team) keeps the old keys: they share one cache entry.
	rig3 := newSKRig(t, nil, ppReply(skeletonAIGoodAnswer, 0.01))
	for _, ctx := range []context.Context{adminCtx("alice"), tenant.With(adminCtx("alice"), tenant.TeamOne("db"))} {
		_, err := rig3.s.SuggestAssemblySkeleton(ctx, skeletonAITestRequest())
		require.NoError(t, err)
	}
	require.Equal(t, 1, rig3.providerCalls())
}
