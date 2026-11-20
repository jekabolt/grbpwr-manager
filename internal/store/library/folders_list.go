package library

import (
	"context"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
)

// The folder rail (02-DESIGN §3.5). Filled by lane B2.

// ListFolders answers every folder, flat, ordered by parent then name, with the direct item count
// visible to the caller under the filter (its folder fields ignored).
func (s *Store) ListFolders(ctx context.Context, b entity.LibraryBranches, f entity.LibraryItemFilter) ([]entity.LibraryFolder, error) {
	return nil, entity.ErrNotImplemented
}
