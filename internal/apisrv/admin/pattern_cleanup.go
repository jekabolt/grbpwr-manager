package admin

import (
	"context"
	"log/slog"
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
}
