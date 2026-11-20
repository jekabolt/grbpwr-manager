package store

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The unified library reads (media-files wave, lane B2): ListItems, Facets and ListFolders over one
// UNION ALL of media and library_file. Container test: every claim is about SQL (sort keys across two
// tables and two collations, the used-id set, the files Viewer, the folder subtree).
//
// Isolation: the shared test database holds other tests' media and files, so every read is scoped to
// this test's own folders, and the expectations are computed in Go from the fixture table below.

type libFixture struct {
	src     int // 1 media, 2 file
	id      int
	name    string
	created time.Time
	size    int64
	kind    entity.LibraryKind
	ct      string // files only
	folder  int
	used    bool
	w, h    int // media only
}

func (f libFixture) key() string { return fmt.Sprintf("%d/%d", f.src, f.id) }

func rowKeys(rows []entity.LibraryItemRow) []string {
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, fmt.Sprintf("%d/%d", r.Src, r.Id))
	}
	return out
}

func fixtureKeys(fs []libFixture) []string {
	out := make([]string, 0, len(fs))
	for _, f := range fs {
		out = append(out, f.key())
	}
	return out
}

func insertLibFolderFixture(ctx context.Context, t *testing.T, parent int, name string) int {
	t.Helper()
	var p any
	if parent > 0 {
		p = parent
	}
	res, err := testDB.ExecContext(ctx,
		`INSERT INTO library_folder (parent_id, name) VALUES (?, CONCAT(?, '-', UUID_SHORT()))`, p, name)
	require.NoError(t, err)
	id, err := res.LastInsertId()
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = testDB.Exec(`UPDATE media SET lib_folder_id = NULL WHERE lib_folder_id = ?`, id)
		_, _ = testDB.Exec(`UPDATE library_file SET folder_id = NULL WHERE folder_id = ?`, id)
		_, _ = testDB.Exec(`DELETE FROM library_folder WHERE id = ?`, id)
	})
	return int(id)
}

// insertLibMediaFixture inserts a media row whose lib_kind (VIRTUAL, 0483) follows from the URLs.
func insertLibMediaFixture(ctx context.Context, t *testing.T, f *libFixture) {
	t.Helper()
	full, thumb := "x.webp", "x-thumb.webp"
	switch f.kind {
	case entity.LibraryKindVideo:
		full, thumb = "x.webp", "x-thumb.mp4"
	case entity.LibraryKindVector:
		full = "x.svg"
	case entity.LibraryKindModel:
		full = "x.glb"
	}
	var name, size any
	if f.name != "" {
		name = f.name
	}
	if f.size > 0 {
		size = f.size
	}
	tag := fmt.Sprintf("https://cdn.test/lib-%d-", time.Now().UnixNano())
	res, err := testDB.ExecContext(ctx, `
		INSERT INTO media (
			full_size, full_size_width, full_size_height,
			compressed, compressed_width, compressed_height,
			thumbnail, thumbnail_width, thumbnail_height,
			lib_file_name, lib_size_bytes, lib_folder_id, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		tag+full, f.w, f.h, tag+"c-"+full, f.w, f.h, tag+thumb, f.w, f.h,
		name, size, f.folder, f.created)
	require.NoError(t, err)
	id, err := res.LastInsertId()
	require.NoError(t, err)
	f.id, f.src = int(id), 1
	t.Cleanup(func() { _, _ = testDB.Exec(`DELETE FROM media WHERE id = ?`, id) })
}

func insertLibFileFixture(ctx context.Context, t *testing.T, f *libFixture, uploaderID int, uploader string) {
	t.Helper()
	res, err := testDB.ExecContext(ctx, `INSERT INTO library_file
		(object_key, file_name, content_type, size_bytes, sha256, uploaded_by, uploaded_by_id, folder_id, created_at)
		VALUES (CONCAT('files-library/test-lib-', UUID_SHORT()), ?, ?, ?, '', ?, ?, ?, ?)`,
		f.name, f.ct, f.size, uploader, uploaderID, f.folder, f.created)
	require.NoError(t, err)
	id, err := res.LastInsertId()
	require.NoError(t, err)
	f.id, f.src = int(id), 2
	t.Cleanup(func() { _, _ = testDB.Exec(`DELETE FROM library_file WHERE id = ?`, id) })
}

// libOrder is the Go model of the store's total sort keys (items.go sortKeys).
func libOrder(fs []libFixture, sort entity.LibrarySort) []libFixture {
	out := slices.Clone(fs)
	cmpSrcID := func(a, b libFixture, idDesc bool) int {
		if a.src != b.src {
			return a.src - b.src
		}
		if idDesc {
			return b.id - a.id
		}
		return a.id - b.id
	}
	slices.SortFunc(out, func(a, b libFixture) int {
		switch sort {
		case entity.LibrarySortOld:
			if c := a.created.Compare(b.created); c != 0 {
				return c
			}
			return cmpSrcID(a, b, false)
		case entity.LibrarySortName:
			ea, eb := a.name == "", b.name == ""
			if ea != eb {
				if ea {
					return 1
				}
				return -1
			}
			if c := strings.Compare(strings.ToLower(a.name), strings.ToLower(b.name)); c != 0 {
				return c
			}
			return cmpSrcID(a, b, true)
		case entity.LibrarySortSize:
			if a.size != b.size {
				if a.size > b.size {
					return -1
				}
				return 1
			}
			return cmpSrcID(a, b, true)
		default:
			if c := b.created.Compare(a.created); c != 0 {
				return c
			}
			return cmpSrcID(a, b, true)
		}
	})
	return out
}

func libWhere(fs []libFixture, keep func(libFixture) bool) []libFixture {
	var out []libFixture
	for _, f := range fs {
		if keep(f) {
			out = append(out, f)
		}
	}
	return out
}

func TestLibraryItems(t *testing.T) {
	filesGuard(t)

	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()
	ctx = superCtx(ctx)

	s, err := NewForTest(ctx, *testCfg)
	require.NoError(t, err)
	lib := s.Library()
	both := entity.LibraryBranches{Media: true, Files: true}

	// Folders first: t.Cleanup runs last-in first-out, so they are removed after their rows.
	fRoot := insertLibFolderFixture(ctx, t, 0, "test-lib-root")
	fSub := insertLibFolderFixture(ctx, t, fRoot, "test-lib-sub")
	fVis := insertLibFolderFixture(ctx, t, 0, "test-lib-visibility")

	aliceID, alice := insertAdminFixture(ctx, t, "test-lib-alice")
	_, bob := insertAdminFixture(ctx, t, "test-lib-bob")

	t0 := time.Date(2031, 3, 10, 12, 0, 0, 0, time.UTC)
	jan := t0.AddDate(0, 0, -40) // 2031-01-29, far from a month edge in any session time zone

	media := []libFixture{
		{name: "alpha.webp", kind: entity.LibraryKindImage, w: 1080, h: 1350, size: 500, created: t0, folder: fRoot, used: true},
		{name: "Bravo.mp4", kind: entity.LibraryKindVideo, w: 1920, h: 1080, size: 900, created: t0.Add(time.Hour), folder: fRoot, used: true},
		{name: "", kind: entity.LibraryKindImage, w: 1000, h: 1000, created: t0.Add(time.Hour), folder: fRoot},
		{name: "charlie.svg", kind: entity.LibraryKindVector, w: 1447, h: 1080, size: 300, created: jan, folder: fRoot, used: true},
		{name: "delta.webp", kind: entity.LibraryKindImage, w: 1080, h: 1350, size: 700, created: t0.Add(2 * time.Hour), folder: fSub, used: true},
	}
	files := []libFixture{
		{name: "alpha.pdf", ct: "application/pdf", size: 500, created: t0.Add(time.Hour), folder: fRoot},
		{name: "echo.png", ct: "image/png", size: 400, created: t0.Add(3 * time.Hour), folder: fRoot, used: true},
		{name: "foxtrot.gif", ct: "image/gif", size: 100, created: jan, folder: fRoot},
		{name: "Golf.glb", ct: "model/gltf-binary", size: 800, created: t0.Add(4 * time.Hour), folder: fRoot, used: true},
		{name: "hotel.svg", ct: "IMAGE/SVG+XML; charset=utf-8", size: 200, created: t0.Add(5 * time.Hour), folder: fSub},
		{name: "india.dxf", ct: "image/vnd.dxf", size: 50, created: t0.Add(5 * time.Hour), folder: fSub, used: true},
		{name: "juliet.mp4", ct: "video/mp4", size: 60, created: t0.Add(6 * time.Hour), folder: fSub},
	}
	for i := range media {
		insertLibMediaFixture(ctx, t, &media[i])
	}
	for i := range files {
		files[i].kind = entity.FileKindOf(files[i].ct)
		insertLibFileFixture(ctx, t, &files[i], aliceID, alice)
	}
	all := append(slices.Clone(media), files...)

	// ── usage: three FK sources and one JSON holder for media ─────────────────────────────────
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	modelRes, err := testDB.ExecContext(ctx, `INSERT INTO model (name, thumbnail_id) VALUES (?, ?)`,
		"Lib Test Model "+suffix, media[0].id)
	require.NoError(t, err)
	modelID, err := modelRes.LastInsertId()
	require.NoError(t, err)
	_, err = testDB.ExecContext(ctx, `INSERT INTO model_media (model_id, media_id, display_order) VALUES (?, ?, 1)`,
		modelID, media[1].id)
	require.NoError(t, err)
	body, err := json.Marshal([]entity.ArchiveItemInsert{{
		Type:      entity.ArchiveItemTypeMainMedia,
		MainMedia: &entity.ArchiveMainMediaInsert{MediaId: media[4].id},
	}})
	require.NoError(t, err)
	archiveRes, err := testDB.ExecContext(ctx, `INSERT INTO archive (tag, thumbnail_id, code, body) VALUES (?, ?, ?, ?)`,
		"lib-test", media[3].id, "AR"+suffix[len(suffix)-8:], string(body))
	require.NoError(t, err)
	archiveID, err := archiveRes.LastInsertId()
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = testDB.Exec(`DELETE FROM model_media WHERE model_id = ?`, modelID)
		_, _ = testDB.Exec(`DELETE FROM model WHERE id = ?`, modelID)
		_, _ = testDB.Exec(`DELETE FROM archive_translation WHERE archive_id = ?`, archiveID)
		_, _ = testDB.Exec(`DELETE FROM archive WHERE id = ?`, archiveID)
	})

	// ── usage of files: a task attachment, a label artwork, a piece print; an invoice is not usage ─
	taskRes, err := testDB.ExecContext(ctx, `INSERT INTO task (title, board, status) VALUES (?, 'design', 'todo')`,
		"lib test task "+suffix)
	require.NoError(t, err)
	taskID, err := taskRes.LastInsertId()
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = testDB.Exec(`DELETE FROM task_file WHERE task_id = ?`, taskID)
		_, _ = testDB.Exec(`DELETE FROM task WHERE id = ?`, taskID)
	})
	require.NoError(t, s.Tasks().AttachFileToTask(ctx, files[1].id, int(taskID)))

	// The label rows hang off a label that would need a whole tech card; the predicate only reads
	// library_file_id, so the rows are written with FK checks off on one pinned connection.
	conn, err := testDB.Conn(ctx)
	require.NoError(t, err)
	dummyLabel := 2000000000 + int(time.Now().UnixNano()%100000000)
	_, err = conn.ExecContext(ctx, `SET FOREIGN_KEY_CHECKS = 0`)
	require.NoError(t, err)
	_, err = conn.ExecContext(ctx, `INSERT INTO tech_card_garment_label_file (label_id, library_file_id) VALUES (?, ?)`,
		dummyLabel, files[3].id)
	require.NoError(t, err)
	_, err = conn.ExecContext(ctx, `INSERT INTO tech_card_garment_label_print (label_id, library_file_id, method, width_mm, height_mm, x_mm, y_mm)
		VALUES (?, ?, 'screen', 10, 10, 0, 0)`, dummyLabel, files[5].id)
	require.NoError(t, err)
	movRes, err := conn.ExecContext(ctx, `INSERT INTO material_stock_movement
		(material_id, movement_type, quantity, on_hand_before, on_hand_after, invoice_file_id)
		VALUES (?, 'receipt', 1, 0, 1, ?)`, dummyLabel, files[2].id)
	require.NoError(t, err)
	movID, err := movRes.LastInsertId()
	require.NoError(t, err)
	_, err = conn.ExecContext(ctx, `SET FOREIGN_KEY_CHECKS = 1`)
	require.NoError(t, err)
	require.NoError(t, conn.Close())
	t.Cleanup(func() {
		_, _ = testDB.Exec(`DELETE FROM tech_card_garment_label_file WHERE label_id = ?`, dummyLabel)
		_, _ = testDB.Exec(`DELETE FROM tech_card_garment_label_print WHERE label_id = ?`, dummyLabel)
		_, _ = testDB.Exec(`DELETE FROM material_stock_movement WHERE id = ?`, movID)
	})

	scope := entity.LibraryItemFilter{FolderId: fRoot, IncludeSubfolders: true}
	list := func(t *testing.T, b entity.LibraryBranches, f entity.LibraryItemFilter, sort entity.LibrarySort, limit int) ([]string, int) {
		t.Helper()
		var keys []string
		total := -1
		for offset := 0; ; offset += limit {
			rows, n, err := lib.ListItems(ctx, b, f, sort, limit, offset)
			require.NoError(t, err)
			if total >= 0 {
				require.Equal(t, total, n, "the total must not move between pages")
			}
			total = n
			keys = append(keys, rowKeys(rows)...)
			if len(rows) < limit {
				return keys, total
			}
		}
	}

	t.Run("one list of media and files, every sort total and stable over pages", func(t *testing.T) {
		for _, sort := range []entity.LibrarySort{entity.LibrarySortNew, entity.LibrarySortOld, entity.LibrarySortName, entity.LibrarySortSize} {
			got, total := list(t, both, scope, sort, 2)
			want := fixtureKeys(libOrder(all, sort))
			assert.Equal(t, want, got, "sort %d", sort)
			assert.Equal(t, len(all), total)
		}
		// Unspecified is NEW.
		got, _ := list(t, both, scope, entity.LibrarySortUnspecified, 5)
		assert.Equal(t, fixtureKeys(libOrder(all, entity.LibrarySortNew)), got)
	})

	t.Run("the NAME sort merges two collations", func(t *testing.T) {
		var mediaColl, fileColl string
		require.NoError(t, testDB.QueryRowContext(ctx, `SELECT COLLATION_NAME FROM information_schema.COLUMNS
			WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'media' AND COLUMN_NAME = 'lib_file_name'`).Scan(&mediaColl))
		require.NoError(t, testDB.QueryRowContext(ctx, `SELECT COLLATION_NAME FROM information_schema.COLUMNS
			WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'library_file' AND COLUMN_NAME = 'file_name'`).Scan(&fileColl))
		t.Logf("media.lib_file_name %s, library_file.file_name %s", mediaColl, fileColl)
		got, _ := list(t, both, scope, entity.LibrarySortName, 50)
		assert.Equal(t, fixtureKeys(libOrder(all, entity.LibrarySortName)), got)
	})

	t.Run("branches: content only, files only, neither", func(t *testing.T) {
		got, total := list(t, entity.LibraryBranches{Media: true}, scope, entity.LibrarySortNew, 50)
		assert.Equal(t, fixtureKeys(libOrder(media, entity.LibrarySortNew)), got)
		assert.Equal(t, len(media), total)
		got, total = list(t, entity.LibraryBranches{Files: true}, scope, entity.LibrarySortNew, 50)
		assert.Equal(t, fixtureKeys(libOrder(files, entity.LibrarySortNew)), got)
		assert.Equal(t, len(files), total)
		rows, n, err := lib.ListItems(ctx, entity.LibraryBranches{}, scope, entity.LibrarySortNew, 10, 0)
		require.NoError(t, err)
		assert.Empty(t, rows)
		assert.Zero(t, n)
	})

	check := func(t *testing.T, name string, f entity.LibraryItemFilter, keep func(libFixture) bool) {
		t.Helper()
		got, total := list(t, both, f, entity.LibrarySortNew, 3)
		want := fixtureKeys(libOrder(libWhere(all, keep), entity.LibrarySortNew))
		assert.Equal(t, want, got, name)
		assert.Equal(t, len(want), total, name+": total == rows")
	}

	t.Run("filters", func(t *testing.T) {
		f := scope
		f.Sources = []entity.LibrarySource{entity.LibrarySourceFile}
		f.Accept = entity.LibraryAcceptVisual
		check(t, "visual files", f, func(x libFixture) bool { return x.src == 2 && entity.CopyableToMedia(x.ct) })

		f = scope
		f.Kinds = []entity.LibraryKind{entity.LibraryKindVideo}
		check(t, "video", f, func(x libFixture) bool { return x.kind == entity.LibraryKindVideo })

		f = scope
		f.Kinds = []entity.LibraryKind{entity.LibraryKindImage}
		f.Usage = entity.LibraryUsageFree
		check(t, "free images", f, func(x libFixture) bool { return x.kind == entity.LibraryKindImage && !x.used })

		f = scope
		f.Ratios = []string{"4:5"}
		check(t, "ratio 4:5 drops files", f, func(x libFixture) bool { return x.src == 1 && x.w*5 == x.h*4 })
		f.Ratios = []string{"other"}
		check(t, "ratio other", f, func(x libFixture) bool {
			return x.src == 1 && entity.LibraryRatioKey(x.w, x.h) == entity.LibraryRatioOther
		})

		f = scope
		f.Search = "ALPHA"
		check(t, "search", f, func(x libFixture) bool { return strings.Contains(strings.ToLower(x.name), "alpha") })
		f.Search = fmt.Sprint(media[2].id)
		got, _ := list(t, both, f, entity.LibrarySortNew, 10)
		assert.Contains(t, got, media[2].key(), "an all-digits search finds the media row by id")

		f = scope
		f.CreatedFrom, f.CreatedTo = t0.Add(-24*time.Hour), t0.Add(90*time.Minute)
		check(t, "period", f, func(x libFixture) bool {
			return !x.created.Before(f.CreatedFrom) && x.created.Before(f.CreatedTo)
		})

		check(t, "subfolder only", entity.LibraryItemFilter{FolderId: fSub},
			func(x libFixture) bool { return x.folder == fSub })
		check(t, "root without subfolders", entity.LibraryItemFilter{FolderId: fRoot},
			func(x libFixture) bool { return x.folder == fRoot })

		f = scope
		f.UploadedById = aliceID
		check(t, "uploader", f, func(x libFixture) bool { return x.src == 2 })
	})

	t.Run("topic drops media", func(t *testing.T) {
		topic := insertFileTopicFixture(ctx, t, "test-lib-topic")
		linkFileTopicFixture(ctx, t, files[0].id, topic)
		f := scope
		f.TopicIds = []int{topic}
		check(t, "topic", f, func(x libFixture) bool { return x.src == 2 && x.id == files[0].id })
	})

	t.Run("used and free agree with GetMediaUsage and the file usage tables", func(t *testing.T) {
		ids := make([]int, 0, len(media))
		for _, m := range media {
			ids = append(ids, m.id)
		}
		usage, err := s.Media().GetMediaUsage(ctx, ids)
		require.NoError(t, err)
		for _, m := range media {
			assert.Equal(t, m.used, len(usage[m.id]) > 0, "media %q: fixture and GetMediaUsage disagree", m.name)
		}
		for _, u := range []entity.LibraryUsage{entity.LibraryUsageUsed, entity.LibraryUsageFree} {
			f := scope
			f.Usage = u
			check(t, fmt.Sprintf("usage %d", u), f, func(x libFixture) bool { return x.used == (u == entity.LibraryUsageUsed) })
		}
		fileIDs := make([]int, 0, len(files))
		for _, x := range files {
			fileIDs = append(fileIDs, x.id)
		}
		usedFiles, err := lib.FileUsedIDs(ctx, fileIDs)
		require.NoError(t, err)
		for _, x := range files {
			assert.Equal(t, x.used, usedFiles[x.id], "file %q in_use", x.name)
		}
	})

	t.Run("file kinds in SQL match entity.FileKindOf", func(t *testing.T) { // TestLibraryFileKindMatchesGo
		for _, k := range []entity.LibraryKind{entity.LibraryKindImage, entity.LibraryKindVideo,
			entity.LibraryKindVector, entity.LibraryKindModel, entity.LibraryKindDocument} {
			f := scope
			f.Sources = []entity.LibrarySource{entity.LibrarySourceFile}
			f.Kinds = []entity.LibraryKind{k}
			check(t, "file kind "+k.String(), f, func(x libFixture) bool { return x.src == 2 && x.kind == k })
		}
	})

	t.Run("facets", func(t *testing.T) {
		fc, err := lib.Facets(ctx, both, scope)
		require.NoError(t, err)
		assert.Equal(t, len(all), fc.Total)

		months := map[[2]int]int{}
		kinds := map[entity.LibraryKind]int{}
		ratios := map[string]int{}
		used := 0
		for _, x := range all {
			months[[2]int{x.created.Year(), int(x.created.Month())}]++
			kinds[x.kind]++
			if x.used {
				used++
			}
			if x.src == 1 {
				ratios[entity.LibraryRatioKey(x.w, x.h)]++
			}
		}
		gotMonths := map[[2]int]int{}
		for _, m := range fc.Months {
			gotMonths[[2]int{m.Year, m.Month}] = m.Count
		}
		assert.Equal(t, months, gotMonths)
		gotKinds := map[entity.LibraryKind]int{}
		for _, k := range fc.Kinds {
			gotKinds[k.Kind] = k.Count
		}
		assert.Equal(t, kinds, gotKinds)
		gotRatios := map[string]int{}
		for _, r := range fc.Ratios {
			gotRatios[r.Ratio] = r.Count
		}
		assert.Equal(t, ratios, gotRatios)
		assert.Equal(t, used, fc.Used)
		assert.Equal(t, len(all)-used, fc.Free)

		// Each month's count is what the list answers for that month.
		for _, m := range fc.Months {
			f := scope
			f.CreatedFrom = time.Date(m.Year, time.Month(m.Month), 1, 0, 0, 0, 0, time.UTC)
			f.CreatedTo = f.CreatedFrom.AddDate(0, 1, 0)
			_, total, err := lib.ListItems(ctx, both, f, entity.LibrarySortNew, 1, 0)
			require.NoError(t, err)
			assert.Equal(t, m.Count, total, "month %d-%02d", m.Year, m.Month)
		}

		// A count ignores its own axis only: kinds stay whole under a kind filter, the rest narrow.
		f := scope
		f.Kinds = []entity.LibraryKind{entity.LibraryKindImage}
		fk, err := lib.Facets(ctx, both, f)
		require.NoError(t, err)
		gotKinds = map[entity.LibraryKind]int{}
		for _, k := range fk.Kinds {
			gotKinds[k.Kind] = k.Count
		}
		assert.Equal(t, kinds, gotKinds)
		images := libWhere(all, func(x libFixture) bool { return x.kind == entity.LibraryKindImage })
		usedImages := len(libWhere(images, func(x libFixture) bool { return x.used }))
		assert.Equal(t, len(images), fk.Total)
		assert.Equal(t, usedImages, fk.Used)
		assert.Equal(t, len(images)-usedImages, fk.Free)
	})

	t.Run("folders: direct counts and children", func(t *testing.T) {
		folders, err := lib.ListFolders(ctx, both, entity.LibraryItemFilter{})
		require.NoError(t, err)
		byID := map[int]entity.LibraryFolder{}
		for _, f := range folders {
			byID[f.Id] = f
		}
		require.Contains(t, byID, fRoot)
		require.Contains(t, byID, fSub)
		assert.Equal(t, len(libWhere(all, func(x libFixture) bool { return x.folder == fRoot })), byID[fRoot].ItemCount)
		assert.Equal(t, 1, byID[fRoot].ChildCount)
		assert.Equal(t, len(libWhere(all, func(x libFixture) bool { return x.folder == fSub })), byID[fSub].ItemCount)
		assert.Equal(t, 0, byID[fSub].ChildCount)
		assert.EqualValues(t, fRoot, byID[fSub].ParentId.Int64)

		folders, err = lib.ListFolders(ctx, both, entity.LibraryItemFilter{Kinds: []entity.LibraryKind{entity.LibraryKindDocument}, FolderId: fSub})
		require.NoError(t, err)
		for _, f := range folders {
			if f.Id == fRoot || f.Id == fSub {
				want := len(libWhere(all, func(x libFixture) bool { return x.folder == f.Id && x.kind == entity.LibraryKindDocument }))
				assert.Equal(t, want, f.ItemCount, "folder counts ignore the folder fields of the filter")
			}
		}
	})

	t.Run("file folders by id", func(t *testing.T) {
		ff, ok := lib.(interface {
			FileFolderIDs(ctx context.Context, ids []int) (map[int]int, error)
		})
		require.True(t, ok, "the library store answers the page's file folders")
		got, err := ff.FileFolderIDs(ctx, []int{files[0].id, files[4].id})
		require.NoError(t, err)
		assert.Equal(t, map[int]int{files[0].id: fRoot, files[4].id: fSub}, got)
	})

	t.Run("a restricted file is invisible to a non-member everywhere", func(t *testing.T) {
		secret := libFixture{name: "kilo.pdf", ct: "application/pdf", size: 10, created: t0, folder: fVis}
		insertLibFileFixture(ctx, t, &secret, aliceID, alice)
		_, err := s.Files().SetFileAccess(ctx, secret.id, entity.LibraryFileAccessUpdate{
			Level: entity.LibraryFileAccessPeople, AdminIDs: []int{aliceID}, Actor: alice,
		})
		require.NoError(t, err)

		vis := entity.LibraryItemFilter{FolderId: fVis}
		for _, tc := range []struct {
			who  context.Context
			want int
		}{{ctx, 1}, {viewerCtx(ctx, alice), 1}, {viewerCtx(ctx, bob), 0}} {
			rows, total, err := lib.ListItems(tc.who, both, vis, entity.LibrarySortNew, 10, 0)
			require.NoError(t, err)
			assert.Len(t, rows, tc.want)
			assert.Equal(t, tc.want, total)
			fc, err := lib.Facets(tc.who, both, vis)
			require.NoError(t, err)
			assert.Equal(t, tc.want, fc.Total)
			assert.Equal(t, tc.want, fc.Free)
			folders, err := lib.ListFolders(tc.who, both, entity.LibraryItemFilter{})
			require.NoError(t, err)
			for _, f := range folders {
				if f.Id == fVis {
					assert.Equal(t, tc.want, f.ItemCount)
				}
			}
			rows, _, err = lib.ListItems(tc.who, both, entity.LibraryItemFilter{Search: "kilo.pdf"}, entity.LibrarySortNew, 50, 0)
			require.NoError(t, err)
			assert.Equal(t, tc.want == 1, slices.Contains(rowKeys(rows), secret.key()), "search must not leak it either")
		}
	})

	t.Run("invalid filters and pages", func(t *testing.T) {
		_, _, err := lib.ListItems(ctx, both, entity.LibraryItemFilter{FolderId: fRoot, NoFolder: true}, entity.LibrarySortNew, 10, 0)
		require.ErrorIs(t, err, entity.ErrLibraryItemsFilterInvalid)
		_, _, err = lib.ListItems(ctx, both, scope, entity.LibrarySortNew, 201, 0)
		require.ErrorIs(t, err, entity.ErrLibraryItemsFilterInvalid)
		_, _, err = lib.ListItems(ctx, both, scope, entity.LibrarySortNew, 10, -1)
		require.ErrorIs(t, err, entity.ErrLibraryItemsFilterInvalid)
		_, err = lib.Facets(ctx, both, entity.LibraryItemFilter{Ratios: []string{"4x5"}})
		require.ErrorIs(t, err, entity.ErrLibraryItemsFilterInvalid)
		// A search full of LIKE metacharacters is literal text, not a pattern.
		rows, total, err := lib.ListItems(ctx, both, entity.LibraryItemFilter{FolderId: fRoot, IncludeSubfolders: true, Search: "%_!"},
			entity.LibrarySortNew, 10, 0)
		require.NoError(t, err)
		assert.Empty(t, rows)
		assert.Zero(t, total)
	})
}
