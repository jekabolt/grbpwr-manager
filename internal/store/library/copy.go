package library

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
)

// CopyLibraryFileToMedia support (02-DESIGN §5). The copy itself (read the private object, run it
// through the media upload doors) is the handler's; the store only finds an earlier copy and files
// the new media row into the file's folder.

// FindMediaBySourceSha finds the media row a file was already copied into (lib_source_sha256) or that
// holds the same bytes (content_hash); (nil, nil) when none, and for a blank sha (old files carry an empty one).
// The oldest matching row wins, so repeated copies keep answering the same id.
func (s *Store) FindMediaBySourceSha(ctx context.Context, sha256 string) (*entity.MediaFull, error) {
	sha := strings.ToLower(strings.TrimSpace(sha256))
	if sha == "" {
		return nil, nil
	}
	m, err := storeutil.QueryNamedOne[entity.MediaFull](ctx, s.DB,
		`SELECT * FROM media WHERE lib_source_sha256 = :sha OR content_hash = :sha ORDER BY id LIMIT 1`,
		map[string]any{"sha": sha})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to find media by source sha: %w", err)
	}
	return &m, nil
}

// SetMediaFolder files a media row into folderID under the folder taken FOR SHARE (the lock that
// conflicts with DeleteFolder's FOR UPDATE, as MoveItems does). It only fills an empty pointer: a row
// someone already filed stays where it is. folderID 0 is a no-op; a missing folder is
// entity.ErrFolderNotFound; a missing media row matches nothing and is not an error.
func (s *Store) SetMediaFolder(ctx context.Context, mediaID, folderID int) error {
	if folderID <= 0 {
		return nil
	}
	return s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		if _, err := getFolder(ctx, rep.DB(), folderID, "FOR SHARE"); err != nil {
			return err
		}
		if err := storeutil.ExecNamed(ctx, rep.DB(),
			`UPDATE media SET lib_folder_id = :folderId WHERE id = :id AND lib_folder_id IS NULL`,
			map[string]any{"id": mediaID, "folderId": folderID}); err != nil {
			return fmt.Errorf("failed to set media folder: %w", err)
		}
		return nil
	})
}

// FileFolderID answers the folder a library file sits in (0 = none). It is NOT on dependency.Library
// (that interface is P0's); the copy handler reaches it through a narrow interface assertion. It reads
// without the viewer predicate: the handler has already proven the file visible through GetFileById.
// A missing file is sql.ErrNoRows.
func (s *Store) FileFolderID(ctx context.Context, fileID int) (int, error) {
	ids, err := storeutil.QueryScalarListNamed[sql.NullInt64](ctx, s.DB,
		`SELECT folder_id FROM library_file WHERE id = :id`, map[string]any{"id": fileID})
	if err != nil {
		return 0, fmt.Errorf("failed to read library file folder: %w", err)
	}
	if len(ids) == 0 {
		return 0, sql.ErrNoRows
	}
	return int(ids[0].Int64), nil
}
