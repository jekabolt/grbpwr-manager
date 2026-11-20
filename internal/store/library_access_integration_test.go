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

// The access axis of the unified library (lane A1): PRIVATE is files without a public media twin
// (whatever else runs), PUBLIC is media; list, total, facets and folder counts agree; the access facet
// adds up to the total without access; MediaTwinIDs is FindMediaBySourceSha in bulk.
func TestLibraryItemsAccessFilter(t *testing.T) {
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

	folder := insertLibFolderFixture(ctx, t, 0, "test-lib-access")
	adminID, admin := insertAdminFixture(ctx, t, "test-lib-access-admin")
	t0 := time.Date(2033, 3, 4, 12, 0, 0, 0, time.UTC)

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

	shaPublished, shaSameBytes, shaPrivate := sha(), sha(), sha()
	published := file("published.jpg", "image/jpeg", shaPublished, t0)
	sameBytes := file("same-bytes.png", "image/png", shaSameBytes, t0.Add(time.Minute))
	private := file("private.png", "image/png", shaPrivate, t0.Add(2*time.Minute))
	blank := file("blank.png", "image/png", "", t0.Add(3*time.Minute))
	doc := file("doc.pdf", "application/pdf", sha(), t0.Add(4*time.Minute))
	twin := media("published.jpg", "lib_source_sha256", strings.ToUpper(shaPublished), t0.Add(5*time.Minute))
	// A later row with the same bytes: the lowest id still wins.
	twinLater := media("published-again.jpg", "content_hash", shaPublished, t0.Add(6*time.Minute))
	twinSame := media("same-bytes.png", "content_hash", shaSameBytes, t0.Add(7*time.Minute))
	other := media("other.webp", "", "", t0.Add(8*time.Minute))
	emptyHash := media("empty-hash.webp", "content_hash", "", t0.Add(9*time.Minute))

	privateFiles := []libFixture{private, blank, doc}
	allFiles := []libFixture{published, sameBytes, private, blank, doc}
	allMedia := []libFixture{twin, twinLater, twinSame, other, emptyHash}
	scope := entity.LibraryItemFilter{FolderId: folder}
	withAccess := func(f entity.LibraryItemFilter, a entity.LibraryItemAccess) entity.LibraryItemFilter {
		f.Access = a
		return f
	}
	list := func(b entity.LibraryBranches, f entity.LibraryItemFilter) ([]string, int) {
		rows, total, err := lib.ListItems(ctx, b, f, entity.LibrarySortNew, 200, 0)
		require.NoError(t, err)
		return rowKeys(rows), total
	}

	t.Run("private is files without a public twin", func(t *testing.T) {
		got, total := list(both, withAccess(scope, entity.LibraryItemAccessPrivate))
		assert.ElementsMatch(t, fixtureKeys(privateFiles), got)
		assert.Equal(t, len(privateFiles), total)
		// Unconditional: also when the media branch does not run.
		got, total = list(entity.LibraryBranches{Files: true}, withAccess(scope, entity.LibraryItemAccessPrivate))
		assert.ElementsMatch(t, fixtureKeys(privateFiles), got)
		assert.Equal(t, len(privateFiles), total)
		f := withAccess(scope, entity.LibraryItemAccessPrivate)
		f.Sources = []entity.LibrarySource{entity.LibrarySourceFile}
		got, _ = list(both, f)
		assert.ElementsMatch(t, fixtureKeys(privateFiles), got)
	})

	t.Run("public is media only", func(t *testing.T) {
		got, total := list(both, withAccess(scope, entity.LibraryItemAccessPublic))
		assert.ElementsMatch(t, fixtureKeys(allMedia), got)
		assert.Equal(t, len(allMedia), total)
	})

	t.Run("access with sources is the intersection; no access is empty, not an error", func(t *testing.T) {
		f := withAccess(scope, entity.LibraryItemAccessPrivate)
		f.Sources = []entity.LibrarySource{entity.LibrarySourceMedia}
		got, total := list(both, f)
		assert.Empty(t, got)
		assert.Zero(t, total)
		f = withAccess(scope, entity.LibraryItemAccessPublic)
		f.Sources = []entity.LibrarySource{entity.LibrarySourceFile}
		got, total = list(both, f)
		assert.Empty(t, got)
		assert.Zero(t, total)
		got, total = list(entity.LibraryBranches{Files: true}, withAccess(scope, entity.LibraryItemAccessPublic))
		assert.Empty(t, got)
		assert.Zero(t, total)
		got, total = list(entity.LibraryBranches{Media: true}, withAccess(scope, entity.LibraryItemAccessPrivate))
		assert.Empty(t, got)
		assert.Zero(t, total)
	})

	t.Run("unspecified is today's list; an unknown value is unspecified", func(t *testing.T) {
		want, wantTotal := list(both, scope)
		got, total := list(both, withAccess(scope, entity.LibraryItemAccess(9)))
		assert.Equal(t, want, got)
		assert.Equal(t, wantTotal, total)
		assert.Equal(t, len(privateFiles)+len(allMedia), wantTotal)
	})

	t.Run("facets: access counts add up, other groups honour access", func(t *testing.T) {
		fc, err := lib.Facets(ctx, both, scope)
		require.NoError(t, err)
		assert.Equal(t, len(privateFiles), fc.AccessPrivate)
		assert.Equal(t, len(allMedia), fc.AccessPublic)
		assert.Equal(t, fc.Total, fc.AccessPrivate+fc.AccessPublic)

		// The access group ignores its own axis.
		fp, err := lib.Facets(ctx, both, withAccess(scope, entity.LibraryItemAccessPrivate))
		require.NoError(t, err)
		assert.Equal(t, fc.AccessPrivate, fp.AccessPrivate)
		assert.Equal(t, fc.AccessPublic, fp.AccessPublic)
		assert.Equal(t, len(privateFiles), fp.Total)
		assert.Equal(t, len(privateFiles), fp.Used+fp.Free)
		kinds := 0
		for _, k := range fp.Kinds {
			kinds += k.Count
		}
		assert.Equal(t, len(privateFiles), kinds)
		assert.Empty(t, fp.Ratios, "ratios are media only")

		// The rest of the filter applies to the access counts.
		fk := scope
		fk.Kinds = []entity.LibraryKind{entity.LibraryKindDocument}
		fd, err := lib.Facets(ctx, both, fk)
		require.NoError(t, err)
		assert.Equal(t, 1, fd.AccessPrivate)
		assert.Equal(t, 0, fd.AccessPublic)
	})

	t.Run("folder counts honour access", func(t *testing.T) {
		count := func(a entity.LibraryItemAccess) int {
			folders, err := lib.ListFolders(ctx, both, entity.LibraryItemFilter{Access: a})
			require.NoError(t, err)
			i := slices.IndexFunc(folders, func(f entity.LibraryFolder) bool { return f.Id == folder })
			require.GreaterOrEqual(t, i, 0)
			return folders[i].ItemCount
		}
		assert.Equal(t, len(privateFiles), count(entity.LibraryItemAccessPrivate))
		assert.Equal(t, len(allMedia), count(entity.LibraryItemAccessPublic))
		assert.Equal(t, len(privateFiles)+len(allMedia), count(entity.LibraryItemAccessUnspecified))
	})

	t.Run("MediaTwinIDs agrees with FindMediaBySourceSha", func(t *testing.T) {
		var shas []string
		for _, f := range allFiles {
			var sum string
			require.NoError(t, testDB.QueryRowContext(ctx, `SELECT sha256 FROM library_file WHERE id = ?`, f.id).Scan(&sum))
			shas = append(shas, sum)
		}
		got, err := lib.MediaTwinIDs(ctx, append(shas, strings.ToUpper(shaPrivate), "  "))
		require.NoError(t, err)
		assert.Equal(t, map[string]int{shaPublished: twin.id, shaSameBytes: twinSame.id}, got)
		for _, sum := range shas {
			m, err := lib.FindMediaBySourceSha(ctx, sum)
			require.NoError(t, err)
			if m == nil {
				assert.NotContains(t, got, strings.ToLower(sum))
				continue
			}
			assert.Equal(t, m.Id, got[strings.ToLower(sum)])
		}
		empty, err := lib.MediaTwinIDs(ctx, nil)
		require.NoError(t, err)
		assert.Empty(t, empty)
	})
}
