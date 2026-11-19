package tenantbind

import (
	"context"
	"testing"
)

// LegacyCredGen caches a handle's credential generation for the TTL; InvalidateCredentials (a
// reset or change on this instance) makes the next call read the control plane again, and so does
// a token carrying a newer generation than the cached one (minted after a change elsewhere).
func TestLegacyCredGenCacheAndInvalidate(t *testing.T) {
	ctx := context.Background()
	if g, err := newBinder(t).LegacyCredGen(ctx, "u", 0); err != nil || g != 0 {
		t.Fatalf("mode off = %d, %v; want 0", g, err)
	}
	b, p := teamsBinder(t)
	p.gens = map[string]int64{"u": 3}
	if g, err := b.LegacyCredGen(ctx, " U ", 0); err != nil || g != 3 {
		t.Fatalf("first = %d, %v", g, err)
	}
	p.gens["u"] = 4
	if g, _ := b.LegacyCredGen(ctx, "u", 3); g != 3 {
		t.Fatalf("cached = %d; want 3", g)
	}
	b.InvalidateCredentials("U")
	if g, _ := b.LegacyCredGen(ctx, "u", 0); g != 4 {
		t.Fatalf("after invalidate = %d; want 4", g)
	}
	p.gens["u"] = 5
	if g, _ := b.LegacyCredGen(ctx, "u", 5); g != 5 {
		t.Fatalf("token newer than the cache = %d; want a fresh read (5)", g)
	}
	if g, err := b.LegacyCredGen(ctx, "nobody", 0); err != nil || g != 0 {
		t.Fatalf("no linked user = %d, %v", g, err)
	}
}
