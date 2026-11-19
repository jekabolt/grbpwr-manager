package admin

import (
	"context"
	"log/slog"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
)

// patternObjectSourcer is the read-only-host side of the file store (bucket.Bucket): where the
// object behind a pattern url lives, "" = our own bucket. A store without it has no read-only hosts.
type patternObjectSourcer interface {
	PatternObjectSource(ctx context.Context, rawURL string) (string, bool)
}

// deleteOrphanedPatternObjects removes de-referenced production-pattern PDFs after their DB mutation
// committed. Object storage is external to MySQL: failure can only leak bytes, so it is loud but
// best-effort and never changes the already-successful RPC result.
func (s *Server) deleteOrphanedPatternObjects(ctx context.Context, owner string, ownerID int, urls []string) {
	if len(urls) == 0 {
		return
	}
	cleanupCtx := context.WithoutCancel(ctx)
	if err := s.bucket.DeleteObjects(cleanupCtx, urls...); err != nil {
		slog.Default().ErrorContext(cleanupCtx, "pattern references removed but bucket objects may be orphaned",
			slog.String("owner", owner),
			slog.Int("owner_id", ownerID),
			slog.Int("url_count", len(urls)),
			slog.String("err", err.Error()))
		// The objects may still exist, so their access rows must stay: dropping them
		// would reset the revocation epoch, and a surviving (or re-referenced) object
		// would come back reachable through tokens an operator had already revoked.
		return
	}
	// Drop the objects' access rows in the same pass (design R9): a row without its
	// object would 404 correctly but sit as dead weight forever. Same best-effort
	// contract as the object deletion above.
	//
	// Only objects DeleteObjects really removed from OUR bucket lose their row: a url on a
	// read-only host (lane B8) was refused there, its object still exists, and so does its row.
	// The row is named by key AND host, so an own-bucket row sharing the key is never hit either.
	ids := make([]entity.PatternObjectID, 0, len(urls))
	for _, u := range urls {
		k, ok := storeutil.PatternObjectKey(u)
		if !ok {
			continue
		}
		if src, isSrc := s.bucket.(patternObjectSourcer); isSrc {
			if host, ok := src.PatternObjectSource(cleanupCtx, u); !ok || host != "" {
				continue
			}
		}
		ids = append(ids, entity.PatternObjectID{Key: k})
	}
	if err := s.repo.PatternObjects().DeleteByKeys(cleanupCtx, ids); err != nil {
		slog.Default().ErrorContext(cleanupCtx, "orphaned pattern access rows not cleaned",
			slog.String("owner", owner),
			slog.Int("owner_id", ownerID),
			slog.String("err", err.Error()))
	}
}
