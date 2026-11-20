package library

import (
	"context"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
)

// Folder structure edits (02-DESIGN §2.4). Filled by lane B1.

// CreateFolder makes a folder under parentID (0 = root).
func (s *Store) CreateFolder(ctx context.Context, parentID int, name string, createdByID int) (*entity.LibraryFolder, error) {
	return nil, entity.ErrNotImplemented
}

// RenameFolder renames a folder; the name stays unique among its siblings.
func (s *Store) RenameFolder(ctx context.Context, id int, name string) (*entity.LibraryFolder, error) {
	return nil, entity.ErrNotImplemented
}

// MoveFolder moves a folder under newParentID (0 = root); no cycles, depth capped.
func (s *Store) MoveFolder(ctx context.Context, id, newParentID int) (*entity.LibraryFolder, error) {
	return nil, entity.ErrNotImplemented
}

// DeleteFolder deletes an empty folder; anything inside, visible or not, is folder_not_empty.
func (s *Store) DeleteFolder(ctx context.Context, id int) error {
	return entity.ErrNotImplemented
}

// MoveItems puts the refs into folderID (0 = out of every folder), all or nothing; answers the count.
func (s *Store) MoveItems(ctx context.Context, refs []entity.LibraryRef, folderID int) (int, error) {
	return 0, entity.ErrNotImplemented
}
