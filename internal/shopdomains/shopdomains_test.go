package shopdomains

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/platformstore"
	"github.com/jekabolt/grbpwr-manager/internal/shop"
	"github.com/jekabolt/grbpwr-manager/internal/tenant"
)

type memStore struct {
	mu    sync.Mutex
	hosts map[int64][]platformstore.ShopHost
	sets  int
}

func (m *memStore) ShopHosts(_ context.Context, teamID int64) ([]platformstore.ShopHost, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]platformstore.ShopHost(nil), m.hosts[teamID]...), nil
}

func (m *memStore) SetShopHostStatus(_ context.Context, teamID int64, host, status string, recs []platformstore.ShopRecord, now time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i, h := range m.hosts[teamID] {
		if h.Host == host {
			m.sets++
			h.Status, h.Records, h.CheckedAt = status, recs, &now
			m.hosts[teamID][i] = h
			return nil
		}
	}
	return platformstore.ErrShopHostNotFound
}

type countingDomains struct {
	shop.FakeDomains
	checks int
	err    error
}

func (c *countingDomains) Check(ctx context.Context, host string) (shop.DomainState, error) {
	c.checks++
	if c.err != nil {
		return shop.DomainState{}, c.err
	}
	return c.FakeDomains.Check(ctx, host)
}

func teamLoop(ids ...int64) func(context.Context, func(context.Context) bool) bool {
	return func(ctx context.Context, tick func(context.Context) bool) bool {
		ok := true
		for _, id := range ids {
			if !tick(tenant.With(ctx, tenant.Team{ID: id, Slug: "t", Status: tenant.StatusActive})) {
				ok = false
			}
		}
		return ok
	}
}

func setup(t *testing.T, hosts ...platformstore.ShopHost) (*memStore, *countingDomains, *Worker, *[]int64, *time.Time) {
	t.Helper()
	st := &memStore{hosts: map[int64][]platformstore.ShopHost{7: hosts}}
	dp := &countingDomains{FakeDomains: shop.FakeDomains{Live: map[string]bool{}}}
	var changed []int64
	w := New(st, dp, teamLoop(7), func(_ context.Context, id int64) { changed = append(changed, id) })
	now := time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC)
	w.now = func() time.Time { return now }
	return st, dp, w, &changed, &now
}

func custom(host, status string, created time.Time) platformstore.ShopHost {
	return platformstore.ShopHost{Host: host, TeamID: 7, Kind: platformstore.ShopHostCustom, Status: status, CreatedAt: created}
}

func TestPendingDomainGoesLive(t *testing.T) {
	base := time.Date(2026, 11, 23, 11, 50, 0, 0, time.UTC)
	st, dp, w, changed, now := setup(t,
		custom("brand.com", platformstore.ShopHostRecordsNeeded, base),
		platformstore.ShopHost{Host: "acme.shop.lekalo.studio", TeamID: 7, Kind: platformstore.ShopHostSubdomain, Status: platformstore.ShopHostLive, CreatedAt: base},
	)
	ctx := context.Background()
	if !w.RunOnce(ctx) {
		t.Fatal("tick failed")
	}
	if dp.checks != 1 || len(*changed) != 0 || st.hosts[7][0].Status != platformstore.ShopHostRecordsNeeded {
		t.Fatalf("first tick: checks=%d changed=%v status=%s", dp.checks, *changed, st.hosts[7][0].Status)
	}
	// Not due again within the minute.
	*now = now.Add(20 * time.Second)
	w.RunOnce(ctx)
	if dp.checks != 1 {
		t.Fatalf("checked before due: %d", dp.checks)
	}
	dp.Live["brand.com"] = true
	*now = now.Add(time.Minute)
	if !w.RunOnce(ctx) {
		t.Fatal("tick failed")
	}
	if st.hosts[7][0].Status != platformstore.ShopHostLive || len(*changed) != 1 || (*changed)[0] != 7 {
		t.Fatalf("not live: status=%s changed=%v", st.hosts[7][0].Status, *changed)
	}
	// Live hosts are not polled.
	*now = now.Add(time.Hour)
	w.RunOnce(ctx)
	if dp.checks != 2 {
		t.Fatalf("live host polled: %d", dp.checks)
	}
}

func TestDueSchedule(t *testing.T) {
	created := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	at := func(d time.Duration) *time.Time { x := created.Add(d); return &x }
	h := custom("b.com", platformstore.ShopHostRecordsNeeded, created)
	if due, _ := Due(h, created.Add(time.Second)); !due {
		t.Fatal("never checked must be due")
	}
	h.CheckedAt = at(10 * time.Minute)
	if due, _ := Due(h, created.Add(10*time.Minute+30*time.Second)); due {
		t.Fatal("fast phase: due after 30s")
	}
	if due, _ := Due(h, created.Add(11*time.Minute)); !due {
		t.Fatal("fast phase: not due after 1m")
	}
	h.CheckedAt = at(2 * time.Hour)
	if due, _ := Due(h, created.Add(2*time.Hour+10*time.Minute)); due {
		t.Fatal("slow phase: due after 10m")
	}
	if due, giveUp := Due(h, created.Add(2*time.Hour+30*time.Minute)); !due || giveUp {
		t.Fatal("slow phase: not due after 30m")
	}
	h.CheckedAt = at(GiveUpAfter - time.Hour)
	if due, giveUp := Due(h, created.Add(GiveUpAfter)); !due || !giveUp {
		t.Fatal("past 7 days must give up")
	}
}

func TestGiveUpAfterSevenDays(t *testing.T) {
	created := time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC)
	st, _, w, changed, _ := setup(t, custom("brand.com", platformstore.ShopHostVerifying, created))
	w.RunOnce(context.Background())
	if st.hosts[7][0].Status != platformstore.ShopHostFailed || len(*changed) != 1 {
		t.Fatalf("status=%s changed=%v", st.hosts[7][0].Status, *changed)
	}
	// Failed hosts are left to the «check» button.
	sets := st.sets
	w.RunOnce(context.Background())
	if st.sets != sets {
		t.Fatal("failed host re-checked by the worker")
	}
}

func TestProviderErrorFailsTickAndKeepsState(t *testing.T) {
	st, dp, w, _, _ := setup(t, custom("brand.com", platformstore.ShopHostRecordsNeeded, time.Date(2026, 11, 23, 11, 0, 0, 0, time.UTC)))
	dp.err = errors.New("vercel down")
	if w.RunOnce(context.Background()) {
		t.Fatal("tick must fail")
	}
	if st.sets != 0 {
		t.Fatal("state written on provider error")
	}
}

func TestBackoff(t *testing.T) {
	if backoff(1) != time.Minute || backoff(2) != 2*time.Minute || backoff(10) != 30*time.Minute {
		t.Fatal("backoff")
	}
}
