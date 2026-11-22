package fxsync

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/tenant"
	"github.com/shopspring/decimal"
)

type countingSource struct {
	calls int
	rates []entity.CostingFxRate
}

func (s *countingSource) RatesToBase(context.Context, string) ([]entity.CostingFxRate, error) {
	s.calls++
	return s.rates, nil
}

type teamRecordingStore struct {
	mu    sync.Mutex
	teams []int64
}

func (s *teamRecordingStore) UpsertCostingFxRates(ctx context.Context, _ []entity.CostingFxRate) error {
	team, ok := tenant.From(ctx)
	if !ok {
		return tenant.ErrNoTenant
	}
	s.mu.Lock()
	s.teams = append(s.teams, team.ID)
	s.mu.Unlock()
	return nil
}

func TestRunOnceFetchesOnceAndWritesEveryTeam(t *testing.T) {
	source := &countingSource{rates: []entity.CostingFxRate{{
		Currency: "USD", RateToBase: decimal.NewFromInt(1), ValidFrom: time.Now().UTC(),
	}}}
	store := &teamRecordingStore{}
	w := New(nil, store)
	w.src = source
	w.SetTeamLoop(func(ctx context.Context, tick func(context.Context) bool) bool {
		ok := true
		for _, team := range []tenant.Team{
			{ID: 1, Slug: "one", DBName: "one", Status: tenant.StatusActive},
			{ID: 2, Slug: "two", DBName: "two", Status: tenant.StatusActive},
		} {
			if !tick(tenant.With(ctx, team)) {
				ok = false
			}
		}
		return ok
	})

	if ok := w.runOnce(context.Background()); !ok {
		t.Fatal("fx tick failed")
	}
	if source.calls != 1 {
		t.Fatalf("provider fetches = %d, want 1", source.calls)
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.teams) != 2 || store.teams[0] != 1 || store.teams[1] != 2 {
		t.Fatalf("database writes = %v, want [1 2]", store.teams)
	}
}

type deadlineStore struct{ had bool }

func (s *deadlineStore) UpsertCostingFxRates(ctx context.Context, _ []entity.CostingFxRate) error {
	_, s.had = ctx.Deadline()
	return nil
}

// X15: waiting for a shared background slot must not eat the tick budget. The team loop gets the
// caller's context (no tick deadline); each team's upsert gets its own timeout once it runs.
func TestTeamLoopWaitIsNotTimed(t *testing.T) {
	source := &countingSource{rates: []entity.CostingFxRate{{
		Currency: "USD", RateToBase: decimal.NewFromInt(1), ValidFrom: time.Now().UTC(),
	}}}
	store := &deadlineStore{}
	w := New(nil, store)
	w.src = source
	loopHadDeadline := true
	w.SetTeamLoop(func(ctx context.Context, tick func(context.Context) bool) bool {
		_, loopHadDeadline = ctx.Deadline()
		return tick(tenant.With(ctx, tenant.Team{ID: 2, Slug: "two", DBName: "two", Status: tenant.StatusActive}))
	})
	if ok := w.runOnce(context.Background()); !ok {
		t.Fatal("fx tick failed")
	}
	if loopHadDeadline {
		t.Fatal("the team loop (slot waits) runs under the tick timeout")
	}
	if !store.had {
		t.Fatal("a team's upsert has no timeout")
	}
}
