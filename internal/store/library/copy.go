package library

import (
	"context"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
)

// CopyLibraryFileToMedia support (02-DESIGN §5). Filled by lane B1.

// FindMediaBySourceSha finds the media row a file was already copied into (lib_source_sha256) or that
// holds the same bytes (content_hash); (nil, nil) when none.
func (s *Store) FindMediaBySourceSha(ctx context.Context, sha256 string) (*entity.MediaFull, error) {
	return nil, entity.ErrNotImplemented
}

// SetMediaFolder sets media.lib_folder_id under the folder taken FOR SHARE (0 = no folder).
func (s *Store) SetMediaFolder(ctx context.Context, mediaID, folderID int) error {
	return entity.ErrNotImplemented
}
