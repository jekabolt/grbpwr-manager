package admin

import (
	"context"
	"log/slog"

	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
)

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
	}
	// Drop the objects' access rows in the same pass (design R9): a row without its
	// object would 404 correctly but sit as dead weight forever. Same best-effort
	// contract as the object deletion above.
	keys := make([]string, 0, len(urls))
	for _, u := range urls {
		if k, ok := storeutil.PatternObjectKey(u); ok {
			keys = append(keys, k)
		}
	}
	if err := s.repo.PatternObjects().DeleteByKeys(cleanupCtx, keys); err != nil {
		slog.Default().ErrorContext(cleanupCtx, "orphaned pattern access rows not cleaned",
			slog.String("owner", owner),
			slog.Int("owner_id", ownerID),
			slog.String("err", err.Error()))
	}
}
