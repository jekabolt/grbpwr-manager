package bucket

import "context"

// OwnsObjectKey answers TeamOwnsKey for this bucket's config: does object key belong to the team in
// ctx. The public link services (patternaccess) and the pattern-url write check (apisrv/admin) find
// it by an optional interface, so their narrow dependencies and fakes stay unchanged.
func (b *Bucket) OwnsObjectKey(ctx context.Context, key string) bool {
	if b == nil {
		return false
	}
	return TeamOwnsKey(ctx, b.Config, key)
}
