package library

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/go-sql-driver/mysql"
	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/fileslibrary"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
)

// Folder structure edits (02-DESIGN §2.4).
//
// LOCKING. Every structure edit (Create, Move) opens with lockTree: `SELECT id, parent_id FROM
// library_folder FOR UPDATE` takes every folder row (and, under the store's SERIALIZABLE tx, the gaps
// around them), so tree edits run one at a time. The table is tiny; in exchange the tree checks
// (parent exists, no cycle, depth) run in Go over a snapshot nobody can change underneath them.
// Delete locks only its target: a child can appear under it only through Create or Move, and both of
// those lock the target row too. Item moves take the target folder FOR SHARE, which conflicts with
// Delete's FOR UPDATE, so a move and a delete of the same folder serialize and no item is ever left
// pointing at a deleted folder (the folder columns carry no FK; this ordering is the integrity).
//
// Every error from inside txFunc is returned unchanged or wrapped with %w, so the tx retry on a
// deadlock (1213) and the sentinel mapping in the handler both keep working.

const mysqlErrDupEntry = 1062

// folderCols are the library_folder columns entity.LibraryFolder carries. Named, not *, so the
// STORED parent_key never meets a struct that has no field for it.
const folderCols = `id, parent_id, name, created_by_id, created_at, updated_at`

// normalizeFolderName trims and validates a folder name: 1–120 runes, valid UTF-8, no '/', no
// control characters. Uniqueness among siblings is left to the DB key (case- and accent-insensitive).
func normalizeFolderName(name string) (string, error) {
	n := strings.TrimSpace(name)
	if n == "" || !utf8.ValidString(n) || utf8.RuneCountInString(n) > entity.LibraryFolderNameMaxLen {
		return "", entity.ErrFolderNameInvalid
	}
	for _, r := range n {
		if r == '/' || unicode.IsControl(r) {
			return "", entity.ErrFolderNameInvalid
		}
	}
	return n, nil
}

// isDupEntry reports a MySQL duplicate-key error.
func isDupEntry(err error) bool {
	var me *mysql.MySQLError
	return errors.As(err, &me) && me.Number == mysqlErrDupEntry
}

// folderTree is the locked snapshot of the whole folder table: id -> parent id (0 = root).
type folderTree map[int]int

type folderEdge struct {
	Id       int           `db:"id"`
	ParentId sql.NullInt64 `db:"parent_id"`
}

// lockTree reads and locks every folder row. Called first in every structure tx.
func lockTree(ctx context.Context, db dependency.DB) (folderTree, error) {
	rows, err := storeutil.QueryListNamed[folderEdge](ctx, db,
		`SELECT id, parent_id FROM library_folder FOR UPDATE`, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to lock library folders: %w", err)
	}
	t := make(folderTree, len(rows))
	for _, r := range rows {
		t[r.Id] = int(r.ParentId.Int64) // NULL -> 0
	}
	return t, nil
}

// depth is the number of folders on the path from the root to id, id included: a root folder is 1,
// "no folder" (0) is 0. A corrupt parent chain longer than the table is cut off instead of looping.
func (t folderTree) depth(id int) int {
	d := 0
	for cur := id; cur != 0 && d <= len(t); cur = t[cur] {
		d++
	}
	return d
}

// isWithin reports whether id is root itself or one of root's descendants.
func (t folderTree) isWithin(id, root int) bool {
	for cur, steps := id, 0; cur != 0 && steps <= len(t); cur, steps = t[cur], steps+1 {
		if cur == root {
			return true
		}
	}
	return false
}

// height is the number of levels of the subtree rooted at id: a folder with no children is 1.
func (t folderTree) height(id int) int {
	children := make(map[int][]int, len(t))
	for c, p := range t {
		children[p] = append(children[p], c)
	}
	var walk func(n, guard int) int
	walk = func(n, guard int) int {
		if guard > len(t) {
			return guard // corrupt (cyclic) data: report "too deep" rather than recurse forever
		}
		h := 1
		for _, c := range children[n] {
			if ch := 1 + walk(c, guard+1); ch > h {
				h = ch
			}
		}
		return h
	}
	return walk(id, 1)
}

// childCount is the number of direct subfolders of id.
func (t folderTree) childCount(id int) int {
	n := 0
	for _, p := range t {
		if p == id {
			n++
		}
	}
	return n
}

func nullID(id int) sql.NullInt64 {
	if id <= 0 {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: int64(id), Valid: true}
}

// getFolder reads one folder row; sql.ErrNoRows becomes entity.ErrFolderNotFound.
func getFolder(ctx context.Context, db dependency.DB, id int, lock string) (*entity.LibraryFolder, error) {
	f, err := storeutil.QueryNamedOne[entity.LibraryFolder](ctx, db,
		`SELECT `+folderCols+` FROM library_folder WHERE id = :id `+lock, map[string]any{"id": id})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, entity.ErrFolderNotFound
		}
		return nil, fmt.Errorf("failed to get library folder: %w", err)
	}
	return &f, nil
}

// CreateFolder makes a folder under parentID (0 = root). createdByID 0 stores NULL.
func (s *Store) CreateFolder(ctx context.Context, parentID int, name string, createdByID int) (*entity.LibraryFolder, error) {
	n, err := normalizeFolderName(name)
	if err != nil {
		return nil, err
	}
	if parentID < 0 {
		return nil, entity.ErrFolderNotFound
	}
	var out *entity.LibraryFolder
	err = s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		t, err := lockTree(ctx, rep.DB())
		if err != nil {
			return err
		}
		if parentID > 0 {
			if _, ok := t[parentID]; !ok {
				return entity.ErrFolderNotFound
			}
		}
		if t.depth(parentID)+1 > entity.LibraryFolderMaxDepth {
			return entity.ErrFolderDepth
		}
		id, err := storeutil.ExecNamedLastId(ctx, rep.DB(),
			`INSERT INTO library_folder (parent_id, name, created_by_id) VALUES (:parentId, :name, :createdById)`,
			map[string]any{"parentId": nullID(parentID), "name": n, "createdById": nullID(createdByID)})
		if err != nil {
			if isDupEntry(err) {
				return entity.ErrFolderNameTaken
			}
			return fmt.Errorf("failed to insert library folder: %w", err)
		}
		out, err = getFolder(ctx, rep.DB(), id, "")
		return err
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// RenameFolder renames a folder; the name stays unique among its siblings. Only the name is checked:
// the tree does not change, so the tree lock is not taken.
func (s *Store) RenameFolder(ctx context.Context, id int, name string) (*entity.LibraryFolder, error) {
	n, err := normalizeFolderName(name)
	if err != nil {
		return nil, err
	}
	var out *entity.LibraryFolder
	err = s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		// Existence first and under a row lock: rows-affected cannot tell "missing" from "same name".
		if _, err := getFolder(ctx, rep.DB(), id, "FOR UPDATE"); err != nil {
			return err
		}
		if err := storeutil.ExecNamed(ctx, rep.DB(),
			`UPDATE library_folder SET name = :name WHERE id = :id`,
			map[string]any{"id": id, "name": n}); err != nil {
			if isDupEntry(err) {
				return entity.ErrFolderNameTaken
			}
			return fmt.Errorf("failed to rename library folder: %w", err)
		}
		f, err := getFolder(ctx, rep.DB(), id, "")
		if err != nil {
			return err
		}
		cc, err := storeutil.QueryCountNamed(ctx, rep.DB(),
			`SELECT COUNT(*) FROM library_folder WHERE parent_id = :id`, map[string]any{"id": id})
		if err != nil {
			return fmt.Errorf("failed to count library subfolders: %w", err)
		}
		f.ChildCount = cc
		out = f
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// MoveFolder moves a folder (with its whole subtree) under newParentID (0 = root). Refused: a
// missing folder or parent (ErrFolderNotFound), a parent inside the moved subtree (ErrFolderCycle),
// a result deeper than LibraryFolderMaxDepth (ErrFolderDepth), a sibling with the same name
// (ErrFolderNameTaken).
func (s *Store) MoveFolder(ctx context.Context, id, newParentID int) (*entity.LibraryFolder, error) {
	if newParentID < 0 {
		return nil, entity.ErrFolderNotFound
	}
	var out *entity.LibraryFolder
	err := s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		t, err := lockTree(ctx, rep.DB())
		if err != nil {
			return err
		}
		if _, ok := t[id]; !ok {
			return entity.ErrFolderNotFound
		}
		if newParentID > 0 {
			if _, ok := t[newParentID]; !ok {
				return entity.ErrFolderNotFound
			}
			if t.isWithin(newParentID, id) {
				return entity.ErrFolderCycle
			}
		}
		if t.depth(newParentID)+t.height(id) > entity.LibraryFolderMaxDepth {
			return entity.ErrFolderDepth
		}
		if err := storeutil.ExecNamed(ctx, rep.DB(),
			`UPDATE library_folder SET parent_id = :parentId WHERE id = :id`,
			map[string]any{"id": id, "parentId": nullID(newParentID)}); err != nil {
			if isDupEntry(err) {
				return entity.ErrFolderNameTaken
			}
			return fmt.Errorf("failed to move library folder: %w", err)
		}
		f, err := getFolder(ctx, rep.DB(), id, "")
		if err != nil {
			return err
		}
		f.ChildCount = t.childCount(id)
		out = f
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// DeleteFolder deletes an empty folder. Anything inside, visible to the caller or not (a subfolder, a
// media row, a library file), is ErrFolderNotEmpty, and the error does not say what: the check is
// blind to ACL and features on purpose, or a user could delete a folder that still holds files they
// cannot see and leave those rows pointing at nothing.
func (s *Store) DeleteFolder(ctx context.Context, id int) error {
	return s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		if _, err := getFolder(ctx, rep.DB(), id, "FOR UPDATE"); err != nil {
			return err
		}
		for _, q := range []string{
			`SELECT 1 FROM library_folder WHERE parent_id = :id LIMIT 1 FOR SHARE`,
			`SELECT 1 FROM media WHERE lib_folder_id = :id LIMIT 1 FOR SHARE`,
			`SELECT 1 FROM library_file WHERE folder_id = :id LIMIT 1 FOR SHARE`,
		} {
			hit, err := storeutil.QueryScalarListNamed[int](ctx, rep.DB(), q, map[string]any{"id": id})
			if err != nil {
				return fmt.Errorf("failed to check library folder contents: %w", err)
			}
			if len(hit) > 0 {
				return entity.ErrFolderNotEmpty
			}
		}
		if err := storeutil.ExecNamed(ctx, rep.DB(),
			`DELETE FROM library_folder WHERE id = :id`, map[string]any{"id": id}); err != nil {
			return fmt.Errorf("failed to delete library folder: %w", err)
		}
		return nil
	})
}

// MoveItems puts the refs into folderID (0 = out of every folder), all or nothing, and answers the
// number of distinct items moved.
//
// A FILE ref passes only when the file exists AND the caller may see it (fileslibrary's Viewer, the
// one visibility predicate of the repo), checked inside this tx so the check and the write see the
// same rows; a hidden file is indistinguishable from a missing one. Any missing or hidden ref, or a
// missing folder, refuses the whole batch: sql.ErrNoRows for an item, entity.ErrFolderNotFound for
// the folder. Section rights per source are the handler's.
//
// The existing ids are read FOR UPDATE and counted before the UPDATEs: RowsAffected counts changed
// rows only, so an item already in the folder would read as missing.
func (s *Store) MoveItems(ctx context.Context, refs []entity.LibraryRef, folderID int) (int, error) {
	var mediaIDs, fileIDs []int
	seenMedia, seenFile := map[int]bool{}, map[int]bool{}
	for _, r := range refs {
		if r.Id <= 0 {
			return 0, fmt.Errorf("library item id %d: %w", r.Id, sql.ErrNoRows)
		}
		switch r.Source {
		case entity.LibrarySourceMedia:
			if !seenMedia[r.Id] {
				seenMedia[r.Id] = true
				mediaIDs = append(mediaIDs, r.Id)
			}
		case entity.LibrarySourceFile:
			if !seenFile[r.Id] {
				seenFile[r.Id] = true
				fileIDs = append(fileIDs, r.Id)
			}
		default:
			return 0, fmt.Errorf("library item of unknown source: %w", sql.ErrNoRows)
		}
	}
	if folderID < 0 {
		return 0, entity.ErrFolderNotFound
	}
	if len(mediaIDs)+len(fileIDs) == 0 {
		return 0, nil
	}
	var v fileslibrary.Viewer
	if len(fileIDs) > 0 {
		var err error
		if v, err = fileslibrary.ResolveViewer(ctx, s.DB); err != nil {
			return 0, err
		}
	}
	err := s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		db := rep.DB()
		if folderID > 0 {
			if _, err := getFolder(ctx, db, folderID, "FOR SHARE"); err != nil {
				return err
			}
		}
		if len(mediaIDs) > 0 {
			found, err := storeutil.QueryScalarListNamed[int](ctx, db,
				`SELECT id FROM media WHERE id IN (:ids) FOR UPDATE`, map[string]any{"ids": mediaIDs})
			if err != nil {
				return fmt.Errorf("failed to lock media for move: %w", err)
			}
			if len(found) != len(mediaIDs) {
				return fmt.Errorf("media to move: %w", sql.ErrNoRows)
			}
		}
		if len(fileIDs) > 0 {
			params := map[string]any{"ids": fileIDs}
			found, err := storeutil.QueryScalarListNamed[int](ctx, db,
				`SELECT lf.id FROM library_file lf WHERE lf.id IN (:ids) AND `+v.Where("lf", params)+` FOR UPDATE`,
				params)
			if err != nil {
				return fmt.Errorf("failed to lock library files for move: %w", err)
			}
			if len(found) != len(fileIDs) {
				return fmt.Errorf("library files to move: %w", sql.ErrNoRows)
			}
		}
		if len(mediaIDs) > 0 {
			if err := storeutil.ExecNamed(ctx, db,
				`UPDATE media SET lib_folder_id = :folderId WHERE id IN (:ids)`,
				map[string]any{"folderId": nullID(folderID), "ids": mediaIDs}); err != nil {
				return fmt.Errorf("failed to move media: %w", err)
			}
		}
		if len(fileIDs) > 0 {
			// updated_at = updated_at: a move is not an edit of the file.
			if err := storeutil.ExecNamed(ctx, db,
				`UPDATE library_file SET folder_id = :folderId, updated_at = updated_at WHERE id IN (:ids)`,
				map[string]any{"folderId": nullID(folderID), "ids": fileIDs}); err != nil {
				return fmt.Errorf("failed to move library files: %w", err)
			}
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return len(mediaIDs) + len(fileIDs), nil
}
