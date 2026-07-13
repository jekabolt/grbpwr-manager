package stockreserve

import (
	"context"
	"testing"
	"time"

	"github.com/shopspring/decimal"
)

// TestReserveIfAvailable_LastUnitRace verifies the atomic path closes the
// read-then-reserve TOCTOU: once one session holds the last unit, a second
// session sees zero availability under the same lock discipline.
func TestReserveIfAvailable_LastUnitRace(t *testing.T) {
	rm := NewManager(15*time.Minute, 30*time.Minute, testLimits())
	defer rm.Stop()
	ctx := context.Background()
	total := decimal.NewFromInt(1)

	availA, err := rm.ReserveIfAvailable(ctx, total, "sessionA", 1, 2, decimal.NewFromInt(1))
	if err != nil {
		t.Fatalf("session A reserve: unexpected err: %v", err)
	}
	if !availA.Equal(decimal.NewFromInt(1)) {
		t.Fatalf("session A available = %s, want 1", availA)
	}

	availB, err := rm.ReserveIfAvailable(ctx, total, "sessionB", 1, 2, decimal.NewFromInt(1))
	if err != nil {
		t.Fatalf("session B reserve: unexpected err: %v", err)
	}
	if !availB.IsZero() {
		t.Fatalf("session B available = %s, want 0 (A holds the last unit)", availB)
	}
}

// TestReserveIfAvailable_ClampsToAvailable verifies a request larger than what is
// left after other sessions' holds is clamped to availability, not the full ask.
func TestReserveIfAvailable_ClampsToAvailable(t *testing.T) {
	rm := NewManager(15*time.Minute, 30*time.Minute, testLimits())
	defer rm.Stop()
	ctx := context.Background()
	total := decimal.NewFromInt(3)

	if _, err := rm.ReserveIfAvailable(ctx, total, "sessionA", 1, 2, decimal.NewFromInt(2)); err != nil {
		t.Fatalf("session A reserve: unexpected err: %v", err)
	}

	// B asks for 5 but only 1 is left (3 total - 2 held by A).
	availB, err := rm.ReserveIfAvailable(ctx, total, "sessionB", 1, 2, decimal.NewFromInt(5))
	if err != nil {
		t.Fatalf("session B reserve: unexpected err: %v", err)
	}
	if !availB.Equal(decimal.NewFromInt(1)) {
		t.Fatalf("session B available = %s, want 1", availB)
	}
	// A's own hold must not count against A on a re-reserve of the same key.
	availAAgain, err := rm.ReserveIfAvailable(ctx, total, "sessionA", 1, 2, decimal.NewFromInt(1))
	if err != nil {
		t.Fatalf("session A re-reserve: unexpected err: %v", err)
	}
	// After B holds 1, A sees 3 - 1 = 2 available to itself.
	if !availAAgain.Equal(decimal.NewFromInt(2)) {
		t.Fatalf("session A re-reserve available = %s, want 2", availAAgain)
	}
}

// TestReserveIfAvailable_AbuseLimitStillReportsAvailability verifies that when the
// in-memory hold is rejected by an abuse limit, the computed availability is still
// returned (the DB decrement is the real guard, so the order path must proceed).
func TestReserveIfAvailable_AbuseLimitStillReportsAvailability(t *testing.T) {
	rm := NewManager(15*time.Minute, 30*time.Minute, testLimits()) // MaxQtyPerItem = 5
	defer rm.Stop()
	ctx := context.Background()
	total := decimal.NewFromInt(10)

	// Ask for 6: clamped to available (10) is still 6, which exceeds MaxQtyPerItem,
	// so reserveLocked rejects the hold — but availability must still be reported.
	avail, err := rm.ReserveIfAvailable(ctx, total, "sessionA", 1, 2, decimal.NewFromInt(6))
	if err == nil {
		t.Fatalf("expected abuse-limit error for qty > MaxQtyPerItem")
	}
	if !avail.Equal(decimal.NewFromInt(10)) {
		t.Fatalf("available = %s, want 10 despite rejected hold", avail)
	}
	// Nothing was actually held.
	if held := rm.GetReservedQuantity(1, 2, ""); !held.IsZero() {
		t.Fatalf("reserved = %s, want 0 (hold was rejected)", held)
	}
}
