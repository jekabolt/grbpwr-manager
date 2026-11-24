package stockreserve

import (
	"context"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/tenant"
	"github.com/shopspring/decimal"
)

// Team databases reuse product, size, order and session ids: one shop's holds never take another
// shop's stock, never overwrite its hold, and a release in one team never frees the other's.
func TestHoldsAreTeamScoped(t *testing.T) {
	mgr := NewManager(time.Minute, time.Minute, testLimits())
	defer mgr.Stop()
	shopA := tenant.With(context.Background(), tenant.Team{ID: 7})
	shopB := tenant.With(context.Background(), tenant.Team{ID: 9})
	one := decimal.NewFromInt(1)

	if avail, err := mgr.ReserveIfAvailable(shopA, one, "s", 1, 1, one); err != nil || !avail.Equal(one) {
		t.Fatalf("shop A hold: %v %v", avail, err)
	}
	// Shop B's product 1 / size 1 is another garment: its one piece is still available.
	if avail, err := mgr.ReserveIfAvailable(shopB, one, "s2", 1, 1, one); err != nil || !avail.Equal(one) {
		t.Fatalf("shop B sees shop A's hold: %v %v", avail, err)
	}
	// Same session id in both shops: separate holds.
	if err := mgr.Reserve(shopB, "s", 1, 1, decimal.NewFromInt(2)); err != nil {
		t.Fatal(err)
	}
	if got := mgr.GetReservedQuantity(shopA, 1, 1, ""); !got.Equal(one) {
		t.Fatalf("shop A held %v after shop B's same-session hold", got)
	}
	if got := mgr.GetReservedQuantity(shopB, 1, 1, ""); !got.Equal(decimal.NewFromInt(3)) {
		t.Fatalf("shop B held %v", got)
	}

	// Same order uuid committed in both: releasing B's leaves A's.
	mgr.Commit(shopA, "s", "order-1")
	mgr.Commit(shopB, "s", "order-1")
	mgr.Release(shopB, "order-1")
	if got := mgr.GetReservedQuantity(shopA, 1, 1, ""); !got.Equal(one) {
		t.Fatalf("shop B's release freed shop A: %v", got)
	}
	mgr.ReleaseSession(shopB, "s2")
	if got := mgr.GetReservedQuantity(shopB, 1, 1, ""); !got.IsZero() {
		t.Fatalf("shop B still holds %v", got)
	}

	// An unbound ctx is team #1 (grbpwr): its holds are team #1's.
	grbpwr := tenant.With(context.Background(), tenant.Team{ID: tenant.TeamOneID})
	if err := mgr.Reserve(context.Background(), "g", 1, 1, one); err != nil {
		t.Fatal(err)
	}
	if got := mgr.GetReservedQuantity(grbpwr, 1, 1, ""); !got.Equal(one) {
		t.Fatalf("team #1 held %v", got)
	}
	mgr.Commit(grbpwr, "g", "order-g")
	mgr.Release(context.Background(), "order-g")
	if got := mgr.GetReservedQuantity(grbpwr, 1, 1, ""); !got.IsZero() {
		t.Fatalf("unbound release left team #1 holding %v", got)
	}
}
