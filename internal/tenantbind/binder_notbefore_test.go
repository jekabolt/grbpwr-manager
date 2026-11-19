package tenantbind

import (
	"context"
	"testing"
	"time"
)

// LegacyNotBefore caches a handle's password_changed_at for the TTL; InvalidateCredentials (a
// reset or change on this instance) makes the next call read the control plane again.
func TestLegacyNotBeforeCacheAndInvalidate(t *testing.T) {
	ctx := context.Background()
	if at, err := newBinder(t).LegacyNotBefore(ctx, "u"); err != nil || !at.IsZero() {
		t.Fatalf("mode off = %v, %v; want zero", at, err)
	}
	b, p := teamsBinder(t)
	first := time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)
	p.changed = map[string]time.Time{"u": first}
	if at, err := b.LegacyNotBefore(ctx, " U "); err != nil || !at.Equal(first) {
		t.Fatalf("first = %v, %v", at, err)
	}
	second := first.Add(time.Hour)
	p.changed["u"] = second
	if at, _ := b.LegacyNotBefore(ctx, "u"); !at.Equal(first) {
		t.Fatalf("cached = %v; want %v", at, first)
	}
	b.InvalidateCredentials("U")
	if at, _ := b.LegacyNotBefore(ctx, "u"); !at.Equal(second) {
		t.Fatalf("after invalidate = %v; want %v", at, second)
	}
	if at, err := b.LegacyNotBefore(ctx, "nobody"); err != nil || !at.IsZero() {
		t.Fatalf("no change recorded = %v, %v", at, err)
	}
}
