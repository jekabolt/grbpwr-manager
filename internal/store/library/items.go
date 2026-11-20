package library

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
)

// The UNION ALL list and its counts (02-DESIGN §3.3, §3.5).

// Sort columns per branch. The name is cast to one charset and collation in both branches (the repo
// idiom of mediaRefSource.selectSQL): media and files may sit in different collations, and the UNION
// column must be one, or MySQL refuses with "Illegal mix of collations".
const (
	mediaSortNameSQL = "CAST(COALESCE(m.lib_file_name, '') AS CHAR CHARACTER SET utf8mb4) COLLATE utf8mb4_unicode_ci"
	mediaSizeSQL     = "COALESCE(m.lib_size_bytes, 0)"
	fileSortNameSQL  = "CAST(COALESCE(lf.file_name, '') AS CHAR CHARACTER SET utf8mb4) COLLATE utf8mb4_unicode_ci"
	fileSizeSQL      = "COALESCE(lf.size_bytes, 0)"
)

// sortKeys answers the ORDER BY of one branch (alias a, its name and size expressions; src is a
// constant there) and of the merged list u. The keys are total: ties end on src, then id.
func sortKeys(sort entity.LibrarySort, a, name, size string) (inner, outer string) {
	switch sort {
	case entity.LibrarySortOld:
		return a + ".created_at ASC, " + a + ".id ASC",
			"u.created_at ASC, u.src ASC, u.id ASC"
	case entity.LibrarySortName:
		// Named rows first, then by name; nameless rows keep a stable order among themselves.
		return "(" + name + " = '') ASC, " + name + " ASC, " + a + ".id DESC",
			"(u.sort_name = '') ASC, u.sort_name ASC, u.src ASC, u.id DESC"
	case entity.LibrarySortSize:
		return size + " DESC, " + a + ".id DESC",
			"u.size_bytes DESC, u.src ASC, u.id DESC"
	default: // NEW
		return a + ".created_at DESC, " + a + ".id DESC",
			"u.created_at DESC, u.src ASC, u.id DESC"
	}
}

// ListItems answers one page of the unified list and the total under the filter.
//
// Each branch is pushed down to ORDER BY <key> LIMIT offset+limit, so the outer sort merges two short
// lists instead of both tables; the outer ORDER BY on the same total keys then cuts the page. The total
// is the sum of one COUNT per branch that runs.
func (s *Store) ListItems(ctx context.Context, b entity.LibraryBranches, f entity.LibraryItemFilter, sort entity.LibrarySort, limit, offset int) ([]entity.LibraryItemRow, int, error) {
	vf, err := validateItemFilter(f)
	if err != nil {
		return nil, 0, err
	}
	limit, offset, err = normalizePage(limit, offset)
	if err != nil {
		return nil, 0, err
	}
	q, err := s.buildQuery(ctx, b, vf, skipAxes{}, false, &buildCache{})
	if err != nil {
		return nil, 0, err
	}
	if q.empty() {
		return []entity.LibraryItemRow{}, 0, nil
	}

	var outer string
	branches := make([]string, 0, 2)
	if q.media {
		inner, o := sortKeys(sort, "m", mediaSortNameSQL, mediaSizeSQL)
		outer = o
		branches = append(branches, fmt.Sprintf(
			"SELECT 1 AS src, m.id AS id, m.created_at AS created_at, %s AS sort_name, %s AS size_bytes"+
				" FROM %s WHERE %s ORDER BY %s LIMIT :lib_window",
			mediaSortNameSQL, mediaSizeSQL, q.mediaFrom, q.mediaWhere, inner))
	}
	if q.files {
		inner, o := sortKeys(sort, "lf", fileSortNameSQL, fileSizeSQL)
		outer = o
		branches = append(branches, fmt.Sprintf(
			"SELECT 2 AS src, lf.id AS id, lf.created_at AS created_at, %s AS sort_name, %s AS size_bytes"+
				" FROM library_file lf WHERE %s ORDER BY %s LIMIT :lib_window",
			fileSortNameSQL, fileSizeSQL, q.fileWhere, inner))
	}
	union := branches[0]
	if len(branches) > 1 {
		union = "(" + strings.Join(branches, ") UNION ALL (") + ")"
	}
	query := "SELECT u.src, u.id, u.created_at, u.sort_name, u.size_bytes FROM (" + union + ") u" +
		" ORDER BY " + outer + " LIMIT :lib_limit OFFSET :lib_offset"
	q.params["lib_window"] = offset + limit
	q.params["lib_limit"] = limit
	q.params["lib_offset"] = offset

	rows, err := storeutil.QueryListNamed[entity.LibraryItemRow](ctx, s.DB, query, q.params)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to list library items: %w", err)
	}
	if rows == nil {
		rows = []entity.LibraryItemRow{}
	}
	total, err := s.countItems(ctx, q)
	if err != nil {
		return nil, 0, err
	}
	return rows, total, nil
}

// countItems is the total of a built query: one scalar COUNT per branch that runs.
func (s *Store) countItems(ctx context.Context, q *itemQuery) (int, error) {
	if q.empty() {
		return 0, nil
	}
	var parts []string
	if q.media {
		parts = append(parts, "(SELECT COUNT(*) FROM "+q.mediaFrom+" WHERE "+q.mediaWhere+")")
	}
	if q.files {
		parts = append(parts, "(SELECT COUNT(*) FROM library_file lf WHERE "+q.fileWhere+")")
	}
	total, err := storeutil.QueryCountNamed(ctx, s.DB, "SELECT "+strings.Join(parts, " + ")+" AS total", q.params)
	if err != nil {
		return 0, fmt.Errorf("failed to count library items: %w", err)
	}
	return total, nil
}

// FileUsedIDs answers which of the given library file ids are in use (task_file, labels).
func (s *Store) FileUsedIDs(ctx context.Context, ids []int) (map[int]bool, error) {
	out := map[int]bool{}
	if len(ids) == 0 {
		return out, nil
	}
	used, err := storeutil.QueryScalarListNamed[int](ctx, s.DB,
		"SELECT lf.id FROM library_file lf WHERE lf.id IN (:lib_ids) AND "+fileUsedSQL,
		map[string]any{"lib_ids": ids})
	if err != nil {
		return nil, fmt.Errorf("failed to read library file usage: %w", err)
	}
	for _, id := range used {
		out[id] = true
	}
	return out, nil
}

type kindCountRow struct {
	Kind  string `db:"k"`
	Count int    `db:"c"`
}

type usedCountRow struct {
	N    int `db:"n"`
	Used int `db:"used"`
}

type ratioCountRow struct {
	W     int `db:"w"`
	H     int `db:"h"`
	Count int `db:"c"`
}

// Facets answers the counts for the filter (02-DESIGN §3.5): months and total under the whole
// filter, kinds under the filter minus kinds (accept still applies), used/free under the filter minus
// usage, ratios under the filter minus ratios (media only).
func (s *Store) Facets(ctx context.Context, b entity.LibraryBranches, f entity.LibraryItemFilter) (*entity.LibraryFacets, error) {
	vf, err := validateItemFilter(f)
	if err != nil {
		return nil, err
	}
	cache := &buildCache{}
	out := &entity.LibraryFacets{Months: []entity.MonthCount{}, Kinds: []entity.LibraryKindCount{}, Ratios: []entity.LibraryRatioCount{}}

	// Months; the total is their sum (every row has exactly one month).
	q, err := s.buildQuery(ctx, b, vf, skipAxes{}, false, cache)
	if err != nil {
		return nil, err
	}
	months := map[[2]int]int{}
	if q.media {
		if err := s.addMonths(ctx, months, "SELECT YEAR(m.created_at) AS y, MONTH(m.created_at) AS m, COUNT(*) AS c FROM "+
			q.mediaFrom+" WHERE "+q.mediaWhere+" GROUP BY YEAR(m.created_at), MONTH(m.created_at)", q.params); err != nil {
			return nil, err
		}
	}
	if q.files {
		if err := s.addMonths(ctx, months, "SELECT YEAR(lf.created_at) AS y, MONTH(lf.created_at) AS m, COUNT(*) AS c FROM library_file lf WHERE "+
			q.fileWhere+" GROUP BY YEAR(lf.created_at), MONTH(lf.created_at)", q.params); err != nil {
			return nil, err
		}
	}
	for ym, c := range months {
		out.Months = append(out.Months, entity.MonthCount{Year: ym[0], Month: ym[1], Count: c})
		out.Total += c
	}
	slices.SortFunc(out.Months, func(a, b entity.MonthCount) int {
		if a.Year != b.Year {
			return b.Year - a.Year
		}
		return b.Month - a.Month
	})

	// Kinds.
	q, err = s.buildQuery(ctx, b, vf, skipAxes{kinds: true}, false, cache)
	if err != nil {
		return nil, err
	}
	kinds := map[entity.LibraryKind]int{}
	if q.media {
		if err := s.addKinds(ctx, kinds, "SELECT m.lib_kind AS k, COUNT(*) AS c FROM "+q.mediaFrom+
			" WHERE "+q.mediaWhere+" GROUP BY m.lib_kind", q.params); err != nil {
			return nil, err
		}
	}
	if q.files {
		if err := s.addKinds(ctx, kinds, "SELECT "+fileKindSQL+" AS k, COUNT(*) AS c FROM library_file lf WHERE "+
			q.fileWhere+" GROUP BY k", q.params); err != nil {
			return nil, err
		}
	}
	for _, k := range []entity.LibraryKind{entity.LibraryKindImage, entity.LibraryKindVideo, entity.LibraryKindVector,
		entity.LibraryKindModel, entity.LibraryKindDocument} {
		if kinds[k] > 0 {
			out.Kinds = append(out.Kinds, entity.LibraryKindCount{Kind: k, Count: kinds[k]})
		}
	}

	// Used / free.
	q, err = s.buildQuery(ctx, b, vf, skipAxes{usage: true}, true, cache)
	if err != nil {
		return nil, err
	}
	var all, used int
	if q.media {
		r, err := storeutil.QueryNamedOne[usedCountRow](ctx, s.DB, "SELECT COUNT(*) AS n, CAST(COALESCE(SUM("+mediaUsedSQL+"), 0) AS SIGNED) AS used FROM "+
			q.mediaFrom+" WHERE "+q.mediaWhere, q.params)
		if err != nil {
			return nil, fmt.Errorf("failed to count used library media: %w", err)
		}
		all, used = all+r.N, used+r.Used
	}
	if q.files {
		r, err := storeutil.QueryNamedOne[usedCountRow](ctx, s.DB, "SELECT COUNT(*) AS n, CAST(COALESCE(SUM("+fileUsedSQL+"), 0) AS SIGNED) AS used FROM library_file lf WHERE "+
			q.fileWhere, q.params)
		if err != nil {
			return nil, fmt.Errorf("failed to count used library files: %w", err)
		}
		all, used = all+r.N, used+r.Used
	}
	out.Used, out.Free = used, all-used

	// Ratios, media only.
	q, err = s.buildQuery(ctx, b, vf, skipAxes{ratios: true}, false, cache)
	if err != nil {
		return nil, err
	}
	if q.media {
		rows, err := storeutil.QueryListNamed[ratioCountRow](ctx, s.DB, "SELECT m.thumbnail_width AS w, m.thumbnail_height AS h, COUNT(*) AS c FROM "+
			q.mediaFrom+" WHERE "+q.mediaWhere+" AND m.thumbnail_width > 0 AND m.thumbnail_height > 0"+
			" GROUP BY m.thumbnail_width, m.thumbnail_height", q.params)
		if err != nil {
			return nil, fmt.Errorf("failed to count library ratios: %w", err)
		}
		ratios := map[string]int{}
		for _, r := range rows {
			if key := entity.LibraryRatioKey(r.W, r.H); key != "" {
				ratios[key] += r.Count
			}
		}
		for _, key := range append(slices.Clone(entity.NamedRatios), entity.LibraryRatioOther) {
			if ratios[key] > 0 {
				out.Ratios = append(out.Ratios, entity.LibraryRatioCount{Ratio: key, Count: ratios[key]})
			}
		}
	}
	return out, nil
}

func (s *Store) addMonths(ctx context.Context, into map[[2]int]int, query string, params map[string]any) error {
	rows, err := storeutil.QueryListNamed[entity.MonthCount](ctx, s.DB, query, params)
	if err != nil {
		return fmt.Errorf("failed to count library months: %w", err)
	}
	for _, r := range rows {
		into[[2]int{r.Year, r.Month}] += r.Count
	}
	return nil
}

func (s *Store) addKinds(ctx context.Context, into map[entity.LibraryKind]int, query string, params map[string]any) error {
	rows, err := storeutil.QueryListNamed[kindCountRow](ctx, s.DB, query, params)
	if err != nil {
		return fmt.Errorf("failed to count library kinds: %w", err)
	}
	for _, r := range rows {
		into[entity.LibraryKindFromValue(r.Kind)] += r.Count
	}
	return nil
}

type fileFolderRow struct {
	Id       int `db:"id"`
	FolderId int `db:"folder_id"`
}

// FileFolderIDs answers the folder of each given library file that sits in one (files in no folder are
// absent). entity.LibraryFile does not carry folder_id, so the list hydration reads it here for the
// page. Not on dependency.Library yet (B2 may not edit it); the handler reaches it through an optional
// interface, see libraryFileFolders in apisrv/admin/library_items.go.
func (s *Store) FileFolderIDs(ctx context.Context, ids []int) (map[int]int, error) {
	out := map[int]int{}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := storeutil.QueryListNamed[fileFolderRow](ctx, s.DB,
		"SELECT id, folder_id FROM library_file WHERE id IN (:lib_ids) AND folder_id IS NOT NULL",
		map[string]any{"lib_ids": ids})
	if err != nil {
		return nil, fmt.Errorf("failed to read library file folders: %w", err)
	}
	for _, r := range rows {
		out[r.Id] = r.FolderId
	}
	return out, nil
}
