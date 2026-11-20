package library

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
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

type twinRow struct {
	Sha string `db:"sha"`
	Id  int    `db:"id"`
}

// MediaTwinIDs answers, for each given file sha256, the public media made from it: the same rule as
// FindMediaBySourceSha (lib_source_sha256 OR content_hash, the lowest id), keyed by the lowercased
// sha. Blank shas are skipped; a sha without a twin is absent. Two grouped reads, one per media
// column, each on its own covering index (idx_media_lib_source_sha, idx_media_content_hash).
func (s *Store) MediaTwinIDs(ctx context.Context, sha256s []string) (map[string]int, error) {
	out := map[string]int{}
	var shas []string
	for _, raw := range sha256s {
		if sha := strings.ToLower(strings.TrimSpace(raw)); sha != "" && !slices.Contains(shas, sha) {
			shas = append(shas, sha)
		}
	}
	if len(shas) == 0 {
		return out, nil
	}
	for _, col := range []string{"lib_source_sha256", "content_hash"} {
		rows, err := storeutil.QueryListNamed[twinRow](ctx, s.DB,
			"SELECT "+col+" AS sha, MIN(id) AS id FROM media WHERE "+col+" IN (:lib_shas) GROUP BY "+col,
			map[string]any{"lib_shas": shas})
		if err != nil {
			return nil, fmt.Errorf("failed to find media twins: %w", err)
		}
		for _, r := range rows {
			sha := strings.ToLower(r.Sha)
			if cur, ok := out[sha]; !ok || r.Id < cur {
				out[sha] = r.Id
			}
		}
	}
	return out, nil
}
