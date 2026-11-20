package library

import (
	"context"
	"fmt"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
)

// The folder rail (02-DESIGN §3.5).

type folderCountRow struct {
	FolderId int `db:"fid"`
	Count    int `db:"c"`
}

// ListFolders answers every folder, flat, ordered by parent then name, with the direct item count
// visible to the caller under the filter (its folder fields ignored).
//
// Folder names are not secret (D12: no per-folder rights), so every folder is listed to anyone who may
// read either section; only the counts go through the branch gating and the files Viewer.
func (s *Store) ListFolders(ctx context.Context, b entity.LibraryBranches, f entity.LibraryItemFilter) ([]entity.LibraryFolder, error) {
	f.FolderId, f.NoFolder, f.IncludeSubfolders = 0, false, false
	vf, err := validateItemFilter(f)
	if err != nil {
		return nil, err
	}
	folders, err := storeutil.QueryListNamed[entity.LibraryFolder](ctx, s.DB,
		"SELECT id, parent_id, name, created_by_id, created_at, updated_at FROM library_folder"+
			" ORDER BY IFNULL(parent_id, 0), name, id", nil)
	if err != nil {
		return nil, fmt.Errorf("failed to list library folders: %w", err)
	}
	if len(folders) == 0 {
		return []entity.LibraryFolder{}, nil
	}

	q, err := s.buildQuery(ctx, b, vf, skipAxes{folder: true}, false, &buildCache{})
	if err != nil {
		return nil, err
	}
	counts := map[int]int{}
	add := func(query string) error {
		rows, err := storeutil.QueryListNamed[folderCountRow](ctx, s.DB, query, q.params)
		if err != nil {
			return fmt.Errorf("failed to count library folder items: %w", err)
		}
		for _, r := range rows {
			counts[r.FolderId] += r.Count
		}
		return nil
	}
	if q.media {
		if err := add("SELECT m.lib_folder_id AS fid, COUNT(*) AS c FROM " + q.mediaFrom + " WHERE " + q.mediaWhere +
			" AND m.lib_folder_id IS NOT NULL GROUP BY m.lib_folder_id"); err != nil {
			return nil, err
		}
	}
	if q.files {
		if err := add("SELECT lf.folder_id AS fid, COUNT(*) AS c FROM library_file lf WHERE " + q.fileWhere +
			" AND lf.folder_id IS NOT NULL GROUP BY lf.folder_id"); err != nil {
			return nil, err
		}
	}

	children := map[int]int{}
	for _, fo := range folders {
		if fo.ParentId.Valid {
			children[int(fo.ParentId.Int64)]++
		}
	}
	for i := range folders {
		folders[i].ItemCount = counts[folders[i].Id]
		folders[i].ChildCount = children[folders[i].Id]
	}
	return folders, nil
}
