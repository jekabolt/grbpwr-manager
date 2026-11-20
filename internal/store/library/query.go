package library

import (
	"context"
	"database/sql"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/content"
	"github.com/jekabolt/grbpwr-manager/internal/store/fileslibrary"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
)

// The predicate builder of the unified list (02-DESIGN §3.1, §3.2, §3.4): which branches run, and
// the WHERE of each. Every SQL string here follows the sqlx named-scanner rule of the store: no ':'
// outside bind names and no SQL comments. The media alias is m, the file alias lf. Bind names of this
// file start with lib_ so they never meet fileslibrary.FileWhere's (visViewerId, topicId…, search).

// Page bounds of ListLibraryItems (02-DESIGN §3.3).
const (
	LibraryItemsDefaultLimit = 60
	LibraryItemsMaxLimit     = 200
	LibraryItemsMaxOffset    = 100000
	// libraryMaxRatios bounds the ratio chips one filter can carry (there are 9 keys; more is junk).
	libraryMaxRatios = 20
	// libraryMaxSearchRunes bounds the search text; a longer one matches nothing useful.
	libraryMaxSearchRunes = 255
)

// fileContentTypeSQL is a library file's content type normalized the way entity.normContentType
// does it in Go: the part before ';', trimmed, lowercased.
const fileContentTypeSQL = "LOWER(TRIM(SUBSTRING_INDEX(lf.content_type, ';', 1)))"

// fileKindSQL is the files-branch kind, the SQL twin of entity.FileKindOf (same order of cases):
// svg is a vector, dxf a document, any other image/* an image, video/* a video, glb a model, the
// rest a document. TestLibraryFileKindMatchesGo pins the two together.
var fileKindSQL = "(CASE" +
	" WHEN " + fileContentTypeSQL + " = 'image/svg+xml' THEN '" + entity.LibraryKindValueVector + "'" +
	" WHEN " + fileContentTypeSQL + " = 'image/vnd.dxf' THEN '" + entity.LibraryKindValueDocument + "'" +
	" WHEN " + fileContentTypeSQL + " LIKE 'image/%' THEN '" + entity.LibraryKindValueImage + "'" +
	" WHEN " + fileContentTypeSQL + " LIKE 'video/%' THEN '" + entity.LibraryKindValueVideo + "'" +
	" WHEN " + fileContentTypeSQL + " = 'model/gltf-binary' THEN '" + entity.LibraryKindValueModel + "'" +
	" ELSE '" + entity.LibraryKindValueDocument + "' END)"

// fileUsedSQL is "the file is in use" (02-DESIGN §3.4): a task attachment or a garment-label artwork
// (0447) or a piece print (0467). An invoice attachment (material_stock_movement.invoice_file_id)
// is not usage: it is not something anybody picks.
const fileUsedSQL = "(EXISTS (SELECT 1 FROM task_file tfu WHERE tfu.file_id = lf.id)" +
	" OR EXISTS (SELECT 1 FROM tech_card_garment_label_file glfu WHERE glfu.library_file_id = lf.id)" +
	" OR EXISTS (SELECT 1 FROM tech_card_garment_label_print glpu WHERE glpu.library_file_id = lf.id))"

// mediaUsedSQL / mediaFreeSQL are the media used/free predicates over content.UsedMediaFilter's join.
var (
	mediaUsedSQL = "(" + content.MediaUsedJoinAlias + ".id IS NOT NULL OR m.id IN (:lib_json_used))"
	mediaFreeSQL = "(" + content.MediaUsedJoinAlias + ".id IS NULL AND m.id NOT IN (:lib_json_used))"
)

// libraryRatio is one parsed ratio chip: a reduced w:h, or "other".
type libraryRatio struct {
	w, h  int
	other bool
}

// itemFilter is a validated LibraryItemFilter: unspecified enum values dropped, duplicates folded,
// ratios parsed.
type itemFilter struct {
	entity.LibraryItemFilter
	ratios []libraryRatio
}

func invalid(format string, args ...any) error {
	return fmt.Errorf("%w: "+format, append([]any{entity.ErrLibraryItemsFilterInvalid}, args...)...)
}

// validateItemFilter refuses a filter that has no meaning (02-DESIGN §1 errors table), before any
// branch is gated away: an invalid filter is invalid whichever sections the caller holds.
func validateItemFilter(f entity.LibraryItemFilter) (itemFilter, error) {
	out := itemFilter{LibraryItemFilter: f}
	out.Search = strings.TrimSpace(f.Search)
	switch {
	case f.NoFolder && f.FolderId > 0:
		return out, invalid("no_folder together with folder_id")
	case f.FolderId < 0:
		return out, invalid("folder_id %d", f.FolderId)
	case f.ProjectTopicId < 0 || f.UploadedById < 0:
		return out, invalid("negative id")
	case len(f.TopicIds) > entity.MaxLibraryTopicFilters:
		return out, invalid("at most %d topics, got %d", entity.MaxLibraryTopicFilters, len(f.TopicIds))
	case len(f.Ratios) > libraryMaxRatios:
		return out, invalid("at most %d ratios, got %d", libraryMaxRatios, len(f.Ratios))
	case len([]rune(out.Search)) > libraryMaxSearchRunes:
		return out, invalid("search longer than %d", libraryMaxSearchRunes)
	}
	for _, id := range f.TopicIds {
		if id <= 0 {
			return out, invalid("topic id %d", id)
		}
	}
	out.Kinds = nil
	for _, k := range f.Kinds {
		if k.String() != "" && !slices.Contains(out.Kinds, k) {
			out.Kinds = append(out.Kinds, k)
		}
	}
	out.Sources = nil
	for _, s := range f.Sources {
		if (s == entity.LibrarySourceMedia || s == entity.LibrarySourceFile) && !slices.Contains(out.Sources, s) {
			out.Sources = append(out.Sources, s)
		}
	}
	out.Ratios = nil
	for _, raw := range f.Ratios {
		r := strings.TrimSpace(raw)
		if r == "" {
			continue
		}
		var lr libraryRatio
		if r == entity.LibraryRatioOther {
			lr.other = true
		} else {
			w, h, ok := entity.ParseLibraryRatio(r)
			if !ok {
				return out, invalid("ratio %q", r)
			}
			lr.w, lr.h = w, h
		}
		if !slices.Contains(out.ratios, lr) {
			out.ratios = append(out.ratios, lr)
			out.Ratios = append(out.Ratios, r)
		}
	}
	return out, nil
}

// normalizePage applies the page bounds: limit 0 is the default, anything outside is invalid.
func normalizePage(limit, offset int) (int, int, error) {
	if limit == 0 {
		limit = LibraryItemsDefaultLimit
	}
	if limit < 0 || limit > LibraryItemsMaxLimit {
		return 0, 0, invalid("limit %d, want 1..%d", limit, LibraryItemsMaxLimit)
	}
	if offset < 0 || offset > LibraryItemsMaxOffset {
		return 0, 0, invalid("offset %d, want 0..%d", offset, LibraryItemsMaxOffset)
	}
	return limit, offset, nil
}

// kindsSubset reports whether every kind of ks is in allowed. Empty ks means "all kinds", which is
// never a subset.
func kindsSubset(ks []entity.LibraryKind, allowed ...entity.LibraryKind) bool {
	if len(ks) == 0 {
		return false
	}
	for _, k := range ks {
		if !slices.Contains(allowed, k) {
			return false
		}
	}
	return true
}

// planBranches narrows the handler's gating by the filter (02-DESIGN §3.1). A branch it drops is
// left out of the SQL entirely.
func planBranches(b entity.LibraryBranches, f itemFilter) entity.LibraryBranches {
	if len(f.Sources) > 0 {
		b.Media = b.Media && slices.Contains(f.Sources, entity.LibrarySourceMedia)
		b.Files = b.Files && slices.Contains(f.Sources, entity.LibrarySourceFile)
	}
	// Topics and projects exist only on files.
	if f.ProjectTopicId > 0 || len(f.TopicIds) > 0 {
		b.Media = false
	}
	// Ratios are read off media thumbnails; files have no dimensions.
	if len(f.ratios) > 0 {
		b.Files = false
	}
	// A media row is never a document.
	if kindsSubset(f.Kinds, entity.LibraryKindDocument) {
		b.Media = false
	}
	// A picker for pictures takes copyable files only (images, vectors, videos): none is a
	// document or a model.
	if f.Accept == entity.LibraryAcceptVisual && kindsSubset(f.Kinds, entity.LibraryKindDocument, entity.LibraryKindModel) {
		b.Files = false
	}
	return b
}

// skipAxes says which axes of the filter a count ignores (facets: "filter minus its own axis";
// folder rail: the folder fields).
type skipAxes struct {
	kinds, usage, ratios, folder bool
}

func (f itemFilter) without(skip skipAxes) itemFilter {
	if skip.kinds {
		f.Kinds = nil
	}
	if skip.usage {
		f.Usage = entity.LibraryUsageUnspecified
	}
	if skip.ratios {
		f.Ratios, f.ratios = nil, nil
	}
	if skip.folder {
		f.FolderId, f.NoFolder, f.IncludeSubfolders = 0, false, false
	}
	return f
}

// itemQuery is a built filter: which branches run, the FROM+WHERE of each and the shared binds.
type itemQuery struct {
	media, files bool
	mediaFrom    string // "media m [LEFT JOIN … mref]"
	mediaWhere   string
	fileWhere    string
	params       map[string]any
}

func (q *itemQuery) empty() bool { return !q.media && !q.files }

// buildCache holds what one request reads at most once however many counts it builds: the media
// used-id join (it walks every JSON holder), the files viewer and folder subtrees.
type buildCache struct {
	usedJoin     string
	jsonIDs      []int
	usedLoaded   bool
	viewer       fileslibrary.Viewer
	viewerLoaded bool
	subtrees     map[int][]int
}

func (s *Store) usedMedia(ctx context.Context, c *buildCache) error {
	if c.usedLoaded {
		return nil
	}
	join, ids, err := content.UsedMediaFilter(ctx, s.DB)
	if err != nil {
		return err
	}
	c.usedJoin, c.jsonIDs, c.usedLoaded = join, ids, true
	return nil
}

func (s *Store) viewer(ctx context.Context, c *buildCache) (fileslibrary.Viewer, error) {
	if c.viewerLoaded {
		return c.viewer, nil
	}
	v, err := fileslibrary.ResolveViewer(ctx, s.DB)
	if err != nil {
		return fileslibrary.Viewer{}, fmt.Errorf("failed to resolve library viewer: %w", err)
	}
	c.viewer, c.viewerLoaded = v, true
	return v, nil
}

func (s *Store) subtree(ctx context.Context, c *buildCache, root int) ([]int, error) {
	if ids, ok := c.subtrees[root]; ok {
		return ids, nil
	}
	ids, err := s.folderSubtree(ctx, root)
	if err != nil {
		return nil, err
	}
	if c.subtrees == nil {
		c.subtrees = map[int][]int{}
	}
	c.subtrees[root] = ids
	return ids, nil
}

// buildQuery builds the branches and their WHERE for f minus the skipped axes. needUsedJoin forces the
// media used-id join even when the filter has no usage (the used/free facet counts over it).
func (s *Store) buildQuery(ctx context.Context, b entity.LibraryBranches, full itemFilter, skip skipAxes,
	needUsedJoin bool, c *buildCache) (*itemQuery, error) {
	f := full.without(skip)
	plan := planBranches(b, f)
	q := &itemQuery{media: plan.Media, files: plan.Files, params: map[string]any{}}
	if q.empty() {
		return q, nil
	}

	var folderIDs []int
	if f.FolderId > 0 {
		folderIDs = []int{f.FolderId}
		if f.IncludeSubfolders {
			ids, err := s.subtree(ctx, c, f.FolderId)
			if err != nil {
				return nil, err
			}
			folderIDs = ids
		}
		q.params["lib_fids"] = folderIDs
	}
	if f.UploadedById > 0 {
		q.params["lib_u"] = f.UploadedById
	}
	if !f.CreatedFrom.IsZero() {
		q.params["lib_from"] = f.CreatedFrom.UTC()
	}
	if !f.CreatedTo.IsZero() {
		q.params["lib_to"] = f.CreatedTo.UTC()
	}
	kindValues := make([]string, 0, len(f.Kinds))
	for _, k := range f.Kinds {
		kindValues = append(kindValues, k.String())
	}
	if len(kindValues) > 0 {
		q.params["lib_kinds"] = kindValues
	}

	if q.media {
		where := []string{"1 = 1"}
		if len(kindValues) > 0 {
			where = append(where, "m.lib_kind IN (:lib_kinds)")
		}
		switch {
		case len(folderIDs) > 0:
			where = append(where, "m.lib_folder_id IN (:lib_fids)")
		case f.NoFolder:
			where = append(where, "m.lib_folder_id IS NULL")
		}
		if f.Search != "" {
			q.params["lib_q"] = "%" + escapeLikeBang(f.Search) + "%"
			cond := "m.lib_file_name LIKE :lib_q ESCAPE '!'"
			if id, ok := searchID(f.Search); ok {
				q.params["lib_qid"] = id
				cond += " OR m.id = :lib_qid"
			}
			where = append(where, "("+cond+")")
		}
		if f.UploadedById > 0 {
			where = append(where, "m.lib_uploaded_by_id = :lib_u")
		}
		if !f.CreatedFrom.IsZero() {
			where = append(where, "m.created_at >= :lib_from")
		}
		if !f.CreatedTo.IsZero() {
			where = append(where, "m.created_at < :lib_to")
		}
		if len(f.ratios) > 0 {
			where = append(where, mediaRatioSQL(f.ratios))
		}
		q.mediaFrom = "media m"
		if f.Usage != entity.LibraryUsageUnspecified || needUsedJoin {
			if err := s.usedMedia(ctx, c); err != nil {
				return nil, err
			}
			q.mediaFrom += " " + c.usedJoin
			q.params["lib_json_used"] = c.jsonIDs
		}
		switch f.Usage {
		case entity.LibraryUsageUsed:
			where = append(where, mediaUsedSQL)
		case entity.LibraryUsageFree:
			where = append(where, mediaFreeSQL)
		}
		q.mediaWhere = strings.Join(where, " AND ")
	}

	if q.files {
		v, err := s.viewer(ctx, c)
		if err != nil {
			return nil, err
		}
		base, err := fileslibrary.FileWhere(entity.LibraryFileListFilter{
			TopicIds:       f.TopicIds,
			ProjectTopicId: f.ProjectTopicId,
			Search:         f.Search,
		}, v, q.params)
		if err != nil {
			return nil, invalid("%v", err)
		}
		where := []string{base}
		if len(kindValues) > 0 {
			where = append(where, fileKindSQL+" IN (:lib_kinds)")
		}
		if f.Accept == entity.LibraryAcceptVisual {
			q.params["lib_copyable"] = entity.CopyableToMediaContentTypes
			where = append(where, fileContentTypeSQL+" IN (:lib_copyable)")
		}
		switch {
		case len(folderIDs) > 0:
			where = append(where, "lf.folder_id IN (:lib_fids)")
		case f.NoFolder:
			where = append(where, "lf.folder_id IS NULL")
		}
		if f.UploadedById > 0 {
			where = append(where, "lf.uploaded_by_id = :lib_u")
		}
		if !f.CreatedFrom.IsZero() {
			where = append(where, "lf.created_at >= :lib_from")
		}
		if !f.CreatedTo.IsZero() {
			where = append(where, "lf.created_at < :lib_to")
		}
		switch f.Usage {
		case entity.LibraryUsageUsed:
			where = append(where, fileUsedSQL)
		case entity.LibraryUsageFree:
			where = append(where, "NOT "+fileUsedSQL)
		}
		q.fileWhere = strings.Join(where, " AND ")
	}
	return q, nil
}

// mediaRatioSQL is the ratio predicate: the thumbnail's reduced ratio is one of the chips, "other"
// being "known dimensions, none of the named ratios". Ratios are parsed integers, so they are written
// into the SQL as numbers (no binds, no injection surface).
func mediaRatioSQL(rs []libraryRatio) string {
	exact := func(w, h int) string {
		return fmt.Sprintf("m.thumbnail_width * %d = m.thumbnail_height * %d", h, w)
	}
	var named []string
	for _, n := range entity.NamedRatios {
		w, h, _ := entity.ParseLibraryRatio(n)
		named = append(named, exact(w, h))
	}
	ors := make([]string, 0, len(rs))
	for _, r := range rs {
		if r.other {
			ors = append(ors, "NOT ("+strings.Join(named, " OR ")+")")
			continue
		}
		ors = append(ors, exact(r.w, r.h))
	}
	return "(m.thumbnail_width > 0 AND m.thumbnail_height > 0 AND (" + strings.Join(ors, " OR ") + "))"
}

// escapeLikeBang escapes a LIKE pattern with '!' (02-DESIGN §3.2): no backslashes, so the meaning
// does not depend on NO_BACKSLASH_ESCAPES.
func escapeLikeBang(s string) string {
	return strings.NewReplacer("!", "!!", "%", "!%", "_", "!_").Replace(s)
}

// searchID reads an all-digits search as a media id ("1234" finds media 1234 by id as well).
func searchID(s string) (int, bool) {
	if s == "" || len(s) > 9 {
		return 0, false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0, false
		}
	}
	n, err := strconv.Atoi(s)
	return n, err == nil && n > 0
}

type folderNode struct {
	Id       int           `db:"id"`
	ParentId sql.NullInt64 `db:"parent_id"`
}

// folderSubtree is the folder and every folder under it, from one read of the (small) tree. A missing
// folder answers just itself, which matches nothing.
func (s *Store) folderSubtree(ctx context.Context, root int) ([]int, error) {
	rows, err := storeutil.QueryListNamed[folderNode](ctx, s.DB, "SELECT id, parent_id FROM library_folder", nil)
	if err != nil {
		return nil, fmt.Errorf("failed to read library folders: %w", err)
	}
	children := map[int][]int{}
	for _, r := range rows {
		if r.ParentId.Valid {
			p := int(r.ParentId.Int64)
			children[p] = append(children[p], r.Id)
		}
	}
	out := []int{root}
	seen := map[int]bool{root: true}
	for i := 0; i < len(out); i++ {
		for _, c := range children[out[i]] {
			if !seen[c] {
				seen[c] = true
				out = append(out, c)
			}
		}
	}
	return out, nil
}
