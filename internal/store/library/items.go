package library

import (
	"context"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
)

// The UNION ALL list and its counts (02-DESIGN §3). Filled by lane B2.

// ListItems answers one page of the unified list and the total under the filter.
func (s *Store) ListItems(ctx context.Context, b entity.LibraryBranches, f entity.LibraryItemFilter, sort entity.LibrarySort, limit, offset int) ([]entity.LibraryItemRow, int, error) {
	return nil, 0, entity.ErrNotImplemented
}

// Facets answers the counts for the filter: months, kinds, used/free, ratios, total.
func (s *Store) Facets(ctx context.Context, b entity.LibraryBranches, f entity.LibraryItemFilter) (*entity.LibraryFacets, error) {
	return nil, entity.ErrNotImplemented
}

// FileUsedIDs answers which of the given library file ids are in use (task_file, labels).
func (s *Store) FileUsedIDs(ctx context.Context, ids []int) (map[int]bool, error) {
	return nil, entity.ErrNotImplemented
}
