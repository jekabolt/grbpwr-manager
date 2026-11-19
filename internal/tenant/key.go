package tenant

import (
	"context"
	"crypto/sha256"
	"strconv"
)

// Key scopes a process-wide key (a singleflight key, an in-memory cache key, per-card state) to the
// team in ctx: two teams may both own tech card 42, and neither may join the other's flight, read
// the other's cached answer or have its spend recorded under the other team.
//
// Team #1 and a ctx with no team keep the key byte for byte, so teams mode OFF (one team, #1)
// behaves exactly as before. Every other team gets a "t<id>\x00" prefix, which no unscoped key
// starts with.
func Key(ctx context.Context, local string) string {
	t, ok := From(ctx)
	if !ok || t.ID == TeamOneID {
		return local
	}
	return "t" + strconv.FormatInt(t.ID, 10) + "\x00" + local
}

// KeyHash is Key for caches keyed by a SHA-256 digest: team #1 and no team keep the digest,
// any other team gets the digest of its scoped key.
func KeyHash(ctx context.Context, digest [sha256.Size]byte) [sha256.Size]byte {
	t, ok := From(ctx)
	if !ok || t.ID == TeamOneID {
		return digest
	}
	return sha256.Sum256([]byte(Key(ctx, string(digest[:]))))
}
