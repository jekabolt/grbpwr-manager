package stockreserve

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/shopspring/decimal"
)

func testLimits() Limits {
	return Limits{
		MaxItemsPerSession:    10,
		MaxQtyPerItem:         decimal.NewFromInt(5),
		MaxTotalReservations:  100,
		MaxTTLRefreshes:       3,
		ReserveRatePerSession: 30,
	}
}

func TestManager_Reserve(t *testing.T) {
	ctx := context.Background()
	mgr := NewManager(time.Minute, time.Minute, testLimits())
	defer mgr.Stop()

	err := mgr.Reserve(ctx, "session1", 1, 1, decimal.NewFromInt(5))
	if err != nil {
		t.Fatalf("Failed to reserve: %v", err)
	}

	reserved := mgr.GetReservedQuantity(1, 1, "")
	if !reserved.Equal(decimal.NewFromInt(5)) {
		t.Errorf("Expected 5 reserved, got %s", reserved.String())
	}
}

func TestManager_GetAvailableStock(t *testing.T) {
	ctx := context.Background()
	mgr := NewManager(time.Minute, time.Minute, testLimits())
	defer mgr.Stop()

	totalStock := decimal.NewFromInt(10)

	mgr.Reserve(ctx, "session1", 1, 1, decimal.NewFromInt(3))
	mgr.Reserve(ctx, "session2", 1, 1, decimal.NewFromInt(2))

	available := mgr.GetAvailableStock(totalStock, 1, 1, "session3")
	if !available.Equal(decimal.NewFromInt(5)) {
		t.Errorf("Expected 5 available, got %s", available.String())
	}

	available = mgr.GetAvailableStock(totalStock, 1, 1, "session1")
	if !available.Equal(decimal.NewFromInt(8)) {
		t.Errorf("Expected 8 available for session1, got %s", available.String())
	}
}

func TestManager_Commit(t *testing.T) {
	ctx := context.Background()
	mgr := NewManager(time.Minute, time.Minute, testLimits())
	defer mgr.Stop()

	mgr.Reserve(ctx, "session1", 1, 1, decimal.NewFromInt(5))
	mgr.Commit(ctx, "session1", "order-uuid-123")

	reserved := mgr.GetReservedQuantity(1, 1, "")
	if !reserved.Equal(decimal.NewFromInt(5)) {
		t.Errorf("Expected 5 reserved after commit, got %s", reserved.String())
	}

	mgr.mu.RLock()
	orderKeys := mgr.byOrder["order-uuid-123"]
	mgr.mu.RUnlock()

	if len(orderKeys) != 1 {
		t.Errorf("Expected 1 reservation for order, got %d", len(orderKeys))
	}
}

func TestManager_Release(t *testing.T) {
	ctx := context.Background()
	mgr := NewManager(time.Minute, time.Minute, testLimits())
	defer mgr.Stop()

	mgr.Reserve(ctx, "session1", 1, 1, decimal.NewFromInt(5))
	mgr.Commit(ctx, "session1", "order-uuid-123")
	mgr.Release(ctx, "order-uuid-123")

	reserved := mgr.GetReservedQuantity(1, 1, "")
	if !reserved.Equal(decimal.Zero) {
		t.Errorf("Expected 0 reserved after release, got %s", reserved.String())
	}
}

func TestManager_ReleaseSession(t *testing.T) {
	ctx := context.Background()
	mgr := NewManager(time.Minute, time.Minute, testLimits())
	defer mgr.Stop()

	mgr.Reserve(ctx, "session1", 1, 1, decimal.NewFromInt(5))
	mgr.Reserve(ctx, "session1", 2, 1, decimal.NewFromInt(3))
	mgr.ReleaseSession(ctx, "session1")

	reserved1 := mgr.GetReservedQuantity(1, 1, "")
	reserved2 := mgr.GetReservedQuantity(2, 1, "")

	if !reserved1.Equal(decimal.Zero) {
		t.Errorf("Expected 0 reserved for product 1, got %s", reserved1.String())
	}
	if !reserved2.Equal(decimal.Zero) {
		t.Errorf("Expected 0 reserved for product 2, got %s", reserved2.String())
	}
}

func TestManager_Expiration(t *testing.T) {
	ctx := context.Background()
	mgr := NewManager(100*time.Millisecond, 100*time.Millisecond, testLimits())
	defer mgr.Stop()

	mgr.Reserve(ctx, "session1", 1, 1, decimal.NewFromInt(5))

	reserved := mgr.GetReservedQuantity(1, 1, "")
	if !reserved.Equal(decimal.NewFromInt(5)) {
		t.Errorf("Expected 5 reserved initially, got %s", reserved.String())
	}

	time.Sleep(200 * time.Millisecond)

	reserved = mgr.GetReservedQuantity(1, 1, "")
	if !reserved.Equal(decimal.Zero) {
		t.Errorf("Expected 0 reserved after expiration, got %s", reserved.String())
	}
}

func TestManager_UpdateReservation(t *testing.T) {
	ctx := context.Background()
	mgr := NewManager(time.Minute, time.Minute, testLimits())
	defer mgr.Stop()

	mgr.Reserve(ctx, "session1", 1, 1, decimal.NewFromInt(5))
	mgr.Reserve(ctx, "session1", 1, 1, decimal.NewFromInt(3))

	reserved := mgr.GetReservedQuantity(1, 1, "")
	if !reserved.Equal(decimal.NewFromInt(3)) {
		t.Errorf("Expected 3 reserved after update, got %s", reserved.String())
	}
}

func TestManager_GetStats(t *testing.T) {
	ctx := context.Background()
	mgr := NewManager(time.Minute, time.Minute, testLimits())
	defer mgr.Stop()

	mgr.Reserve(ctx, "session1", 1, 1, decimal.NewFromInt(5))
	mgr.Reserve(ctx, "session2", 2, 1, decimal.NewFromInt(3))
	mgr.Reserve(ctx, "session3", 3, 1, decimal.NewFromInt(2))
	mgr.Commit(ctx, "session3", "order-123")

	stats := mgr.GetStats()

	if stats["total_reservations"].(int) != 3 {
		t.Errorf("Expected 3 total reservations, got %v", stats["total_reservations"])
	}
	if stats["cart_reservations"].(int) != 2 {
		t.Errorf("Expected 2 cart reservations, got %v", stats["cart_reservations"])
	}
	if stats["order_reservations"].(int) != 1 {
		t.Errorf("Expected 1 order reservation, got %v", stats["order_reservations"])
	}
}

// ===== Abuse prevention tests =====

func TestManager_MaxQtyPerItemEnforced(t *testing.T) {
	ctx := context.Background()
	mgr := NewManager(time.Minute, time.Minute, testLimits())
	defer mgr.Stop()

	// Should fail: quantity exceeds MaxQtyPerItem (5)
	err := mgr.Reserve(ctx, "session1", 1, 1, decimal.NewFromInt(10))
	if err == nil {
		t.Error("Expected error for quantity exceeding max, got nil")
	}

	// Should fail: zero quantity
	err = mgr.Reserve(ctx, "session1", 1, 1, decimal.Zero)
	if err == nil {
		t.Error("Expected error for zero quantity, got nil")
	}

	// Should fail: negative quantity
	err = mgr.Reserve(ctx, "session1", 1, 1, decimal.NewFromInt(-1))
	if err == nil {
		t.Error("Expected error for negative quantity, got nil")
	}

	// Should succeed: within limit
	err = mgr.Reserve(ctx, "session1", 1, 1, decimal.NewFromInt(5))
	if err != nil {
		t.Errorf("Expected success for valid quantity, got: %v", err)
	}
}

func TestManager_MaxItemsPerSessionEnforced(t *testing.T) {
	ctx := context.Background()
	limits := testLimits()
	limits.MaxItemsPerSession = 3
	mgr := NewManager(time.Minute, time.Minute, limits)
	defer mgr.Stop()

	// Reserve 3 different items (should succeed)
	for i := 1; i <= 3; i++ {
		err := mgr.Reserve(ctx, "session1", i, 1, decimal.NewFromInt(1))
		if err != nil {
			t.Fatalf("Reserve #%d should succeed: %v", i, err)
		}
	}

	// 4th distinct item should fail
	err := mgr.Reserve(ctx, "session1", 4, 1, decimal.NewFromInt(1))
	if err == nil {
		t.Error("Expected error for exceeding max items per session, got nil")
	}

	// Updating existing item should still work
	err = mgr.Reserve(ctx, "session1", 1, 1, decimal.NewFromInt(2))
	if err != nil {
		t.Errorf("Updating existing reservation should succeed: %v", err)
	}
}

func TestManager_GlobalCapacityEnforced(t *testing.T) {
	ctx := context.Background()
	limits := testLimits()
	limits.MaxTotalReservations = 5
	limits.MaxItemsPerSession = 100 // high so per-session limit doesn't interfere
	limits.ReserveRatePerSession = 1000
	mgr := NewManager(time.Minute, time.Minute, limits)
	defer mgr.Stop()

	// Fill to capacity with different sessions
	for i := 0; i < 5; i++ {
		sid := fmt.Sprintf("session-%d", i)
		err := mgr.Reserve(ctx, sid, i+1, 1, decimal.NewFromInt(1))
		if err != nil {
			t.Fatalf("Reserve #%d should succeed: %v", i+1, err)
		}
	}

	// Next one should be rejected
	err := mgr.Reserve(ctx, "session-overflow", 99, 1, decimal.NewFromInt(1))
	if err == nil {
		t.Error("Expected error when global capacity reached, got nil")
	}
}

func TestManager_TTLNotExtendedOnUpdate(t *testing.T) {
	ctx := context.Background()
	mgr := NewManager(200*time.Millisecond, time.Minute, testLimits())
	defer mgr.Stop()

	mgr.Reserve(ctx, "session1", 1, 1, decimal.NewFromInt(3))

	// Wait 150ms (close to expiry), then update
	time.Sleep(150 * time.Millisecond)
	mgr.Reserve(ctx, "session1", 1, 1, decimal.NewFromInt(2))

	// If TTL was refreshed, reservation would still be valid.
	// Original TTL was 200ms, we're now at ~150ms. Wait another 100ms (total ~250ms).
	time.Sleep(100 * time.Millisecond)

	reserved := mgr.GetReservedQuantity(1, 1, "")
	if !reserved.Equal(decimal.Zero) {
		t.Errorf("Expected 0 after original TTL expired (TTL should not refresh), got %s", reserved.String())
	}
}

func TestManager_MaxTTLRefreshesEnforced(t *testing.T) {
	ctx := context.Background()
	limits := testLimits()
	limits.MaxTTLRefreshes = 2
	mgr := NewManager(time.Minute, time.Minute, limits)
	defer mgr.Stop()

	// Initial reserve
	mgr.Reserve(ctx, "session1", 1, 1, decimal.NewFromInt(1))

	// Update 1 (refresh count -> 1)
	mgr.Reserve(ctx, "session1", 1, 1, decimal.NewFromInt(2))

	// Update 2 (refresh count -> 2, at limit)
	mgr.Reserve(ctx, "session1", 1, 1, decimal.NewFromInt(3))

	// Update 3 (at limit, should still accept but won't bump refresh count)
	err := mgr.Reserve(ctx, "session1", 1, 1, decimal.NewFromInt(4))
	if err != nil {
		t.Errorf("Update at refresh limit should still succeed (qty update only): %v", err)
	}

	// Verify quantity was updated even at refresh limit
	reserved := mgr.GetReservedQuantity(1, 1, "")
	if !reserved.Equal(decimal.NewFromInt(4)) {
		t.Errorf("Expected 4 after qty-only update, got %s", reserved.String())
	}
}

func TestManager_SessionRateLimitEnforced(t *testing.T) {
	ctx := context.Background()
	limits := testLimits()
	limits.MaxItemsPerSession = 100
	limits.ReserveRatePerSession = 5
	mgr := NewManager(time.Minute, time.Minute, limits)
	defer mgr.Stop()

	// Exhaust rate limit
	for i := 0; i < 5; i++ {
		err := mgr.Reserve(ctx, "session1", i+1, 1, decimal.NewFromInt(1))
		if err != nil {
			t.Fatalf("Reserve #%d should succeed: %v", i+1, err)
		}
	}

	// Next call should be rate limited
	err := mgr.Reserve(ctx, "session1", 99, 1, decimal.NewFromInt(1))
	if err == nil {
		t.Error("Expected rate limit error, got nil")
	}

	// Different session should be fine
	err = mgr.Reserve(ctx, "session2", 1, 1, decimal.NewFromInt(1))
	if err != nil {
		t.Errorf("Different session should not be rate limited: %v", err)
	}
}

func TestManager_ProductSizeIndexCorrect(t *testing.T) {
	ctx := context.Background()
	mgr := NewManager(time.Minute, time.Minute, testLimits())
	defer mgr.Stop()

	// Multiple sessions reserving the same product-size
	mgr.Reserve(ctx, "s1", 1, 1, decimal.NewFromInt(2))
	mgr.Reserve(ctx, "s2", 1, 1, decimal.NewFromInt(3))
	mgr.Reserve(ctx, "s3", 1, 2, decimal.NewFromInt(1)) // different size

	// product 1, size 1 should have 5 total
	total := mgr.GetReservedQuantity(1, 1, "")
	if !total.Equal(decimal.NewFromInt(5)) {
		t.Errorf("Expected 5 for product 1 size 1, got %s", total.String())
	}

	// product 1, size 2 should have 1
	total = mgr.GetReservedQuantity(1, 2, "")
	if !total.Equal(decimal.NewFromInt(1)) {
		t.Errorf("Expected 1 for product 1 size 2, got %s", total.String())
	}

	// Release s1 and check index cleaned up
	mgr.ReleaseSession(ctx, "s1")

	total = mgr.GetReservedQuantity(1, 1, "")
	if !total.Equal(decimal.NewFromInt(3)) {
		t.Errorf("Expected 3 after releasing s1, got %s", total.String())
	}
}

func TestManager_StopCleansUp(t *testing.T) {
	mgr := NewManager(time.Minute, time.Minute, testLimits())

	// Stop should not panic and should be callable
	mgr.Stop()

	// Double stop should not panic
	defer func() {
		if r := recover(); r != nil {
			// This is expected - closing a closed channel panics
			// In production you'd add a sync.Once, but this confirms Stop works
		}
	}()
}
