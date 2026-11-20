package store

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A private library file that got a public media twin (CopyLibraryFileToMedia) is ONE item in the
// unified library, the media (lane V1): hidden from ListItems, the total, the facets and the folder
// counts whenever the media branch runs in the same query, and "has a twin" is exactly the copy's
// dedup (FindMediaBySourceSha: lib_source_sha256 or content_hash equal to the file's sha).
func TestLibraryItemsHideMediaTwin(t *testing.T) {
	filesGuard(t)

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	ctx = superCtx(ctx)

	s, err := NewForTest(ctx, *testCfg)
	require.NoError(t, err)
	lib := s.Library()
	both := entity.LibraryBranches{Media: true, Files: true}

	sha := func() string {
		b := make([]byte, 32)
		_, err := rand.Read(b)
		require.NoError(t, err)
		return hex.EncodeToString(b)
	}

	folder := insertLibFolderFixture(ctx, t, 0, "test-lib-twin")
	adminID, admin := insertAdminFixture(ctx, t, "test-lib-twin-admin")
	t0 := time.Date(2032, 5, 12, 12, 0, 0, 0, time.UTC)

	file := func(name, ct, sum string, at time.Time) libFixture {
		f := libFixture{name: name, ct: ct, size: 100, created: at, folder: folder, kind: entity.FileKindOf(ct)}
		insertLibFileFixture(ctx, t, &f, adminID, admin)
		_, err := testDB.ExecContext(ctx, `UPDATE library_file SET sha256 = ? WHERE id = ?`, sum, f.id)
		require.NoError(t, err)
		return f
	}
	media := func(name, column, sum string, at time.Time) libFixture {
		m := libFixture{name: name, kind: entity.LibraryKindImage, w: 1080, h: 1350, size: 90, created: at, folder: folder}
		insertLibMediaFixture(ctx, t, &m)
		if column != "" {
			_, err := testDB.ExecContext(ctx, `UPDATE media SET `+column+` = ? WHERE id = ?`, sum, m.id)
			require.NoError(t, err)
		}
		return m
	}

	shaCopied, shaSameBytes, shaAlone := sha(), sha(), sha()
	copied := file("copied.jpg", "image/jpeg", shaCopied, t0)
	sameBytes := file("same-bytes.png", "image/png", shaSameBytes, t0.Add(time.Minute))
	alone := file("alone.png", "image/png", shaAlone, t0.Add(2*time.Minute))
	blank := file("blank.png", "image/png", "", t0.Add(3*time.Minute))
	doc := file("doc.pdf", "application/pdf", sha(), t0.Add(4*time.Minute))
	// The copy's twin carries the file's sha as lib_source_sha256 (uppercase: the column is
	// case-insensitive, as is the copy's lookup); the existed=true case is a media row whose
	// content_hash is the file's sha.
	twinCopied := media("copied.jpg", "lib_source_sha256", strings.ToUpper(shaCopied), t0.Add(5*time.Minute))
	twinSame := media("same-bytes.png", "content_hash", shaSameBytes, t0.Add(6*time.Minute))
	other := media("other.webp", "", "", t0.Add(7*time.Minute))
	// A media row with an empty content hash must never make a blank-sha file vanish.
	emptyHash := media("empty-hash.webp", "content_hash", "", t0.Add(8*time.Minute))

	// The predicate agrees with the copy's dedup, file by file.
	for _, f := range []libFixture{copied, sameBytes, alone, blank, doc} {
		var sum string
		require.NoError(t, testDB.QueryRowContext(ctx, `SELECT sha256 FROM library_file WHERE id = ?`, f.id).Scan(&sum))
		m, err := lib.FindMediaBySourceSha(ctx, sum)
		require.NoError(t, err)
		hidden := f.id == copied.id || f.id == sameBytes.id
		assert.Equal(t, hidden, m != nil, "dedup of %s", f.name)
	}

	visible := []libFixture{alone, blank, doc, twinCopied, twinSame, other, emptyHash}
	allFiles := []libFixture{copied, sameBytes, alone, blank, doc}
	scope := entity.LibraryItemFilter{FolderId: folder}

	listAll := func(b entity.LibraryBranches, f entity.LibraryItemFilter) ([]string, int) {
		rows, total, err := lib.ListItems(ctx, b, f, entity.LibrarySortNew, 200, 0)
		require.NoError(t, err)
		return rowKeys(rows), total
	}

	t.Run("media and files: the twin file is gone from list and total", func(t *testing.T) {
		got, total := listAll(both, scope)
		assert.Equal(t, fixtureKeys(libOrder(visible, entity.LibrarySortNew)), got)
		assert.Equal(t, len(visible), total)
		// Pages agree with the total.
		rows, total2, err := lib.ListItems(ctx, both, scope, entity.LibrarySortNew, 2, 6)
		require.NoError(t, err)
		assert.Equal(t, len(visible), total2)
		assert.Len(t, rows, 1)
	})

	t.Run("visual picker: one picture per twin", func(t *testing.T) {
		f := scope
		f.Accept = entity.LibraryAcceptVisual
		got, total := listAll(both, f)
		want := libWhere(visible, func(x libFixture) bool { return x.src == 1 || entity.CopyableToMedia(x.ct) })
		assert.ElementsMatch(t, fixtureKeys(want), got)
		assert.Equal(t, len(want), total)
	})

	t.Run("a files-only list keeps every file", func(t *testing.T) {
		// A caller without media, and the notes/label pickers (sources=[FILE]) need the file row.
		got, total := listAll(entity.LibraryBranches{Files: true}, scope)
		assert.ElementsMatch(t, fixtureKeys(allFiles), got)
		assert.Equal(t, len(allFiles), total)
		f := scope
		f.Sources = []entity.LibrarySource{entity.LibrarySourceFile}
		got, total = listAll(both, f)
		assert.ElementsMatch(t, fixtureKeys(allFiles), got)
		assert.Equal(t, len(allFiles), total)
	})

	t.Run("facets count one item per twin", func(t *testing.T) {
		fc, err := lib.Facets(ctx, both, scope)
		require.NoError(t, err)
		assert.Equal(t, len(visible), fc.Total)
		kinds := map[entity.LibraryKind]int{}
		for _, x := range visible {
			kinds[x.kind]++
		}
		got := map[entity.LibraryKind]int{}
		for _, k := range fc.Kinds {
			got[k.Kind] = k.Count
		}
		assert.Equal(t, kinds, got)
		assert.Equal(t, len(visible), fc.Used+fc.Free)
		months := 0
		for _, m := range fc.Months {
			months += m.Count
		}
		assert.Equal(t, len(visible), months)
	})

	t.Run("folder counts one item per twin", func(t *testing.T) {
		folders, err := lib.ListFolders(ctx, both, entity.LibraryItemFilter{})
		require.NoError(t, err)
		i := slices.IndexFunc(folders, func(f entity.LibraryFolder) bool { return f.Id == folder })
		require.GreaterOrEqual(t, i, 0)
		assert.Equal(t, len(visible), folders[i].ItemCount)

		folders, err = lib.ListFolders(ctx, entity.LibraryBranches{Files: true}, entity.LibraryItemFilter{})
		require.NoError(t, err)
		i = slices.IndexFunc(folders, func(f entity.LibraryFolder) bool { return f.Id == folder })
		require.GreaterOrEqual(t, i, 0)
		assert.Equal(t, len(allFiles), folders[i].ItemCount)
	})

	t.Run("the twin file itself stays readable", func(t *testing.T) {
		lf, err := s.Files().GetFileById(ctx, copied.id)
		require.NoError(t, err)
		assert.Equal(t, "copied.jpg", lf.FileName)
	})
}
