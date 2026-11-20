package library_test

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/stretchr/testify/require"
)

func TestCopyLibraryFindMediaBySourceSha(t *testing.T) {
	rep, raw := libraryDB(t)
	lib, ctx := rep.Library(), context.Background()

	got, err := lib.FindMediaBySourceSha(ctx, "")
	require.NoError(t, err)
	require.Nil(t, got, "a blank sha (old files) never matches")

	got, err = lib.FindMediaBySourceSha(ctx, randomSha())
	require.NoError(t, err)
	require.Nil(t, got)

	// By lib_source_sha256: the oldest of two copies wins, and the lookup ignores case.
	src := randomSha()
	first, second := insertMedia(t, raw), insertMedia(t, raw)
	_, err = raw.Exec(`UPDATE media SET lib_source_sha256 = ? WHERE id IN (?, ?)`, src, first, second)
	require.NoError(t, err)
	got, err = lib.FindMediaBySourceSha(ctx, " "+strings.ToUpper(src)+" ")
	require.NoError(t, err)
	require.NotNil(t, got)
	require.Equal(t, first, got.Id)

	// By content_hash: an upload of the same bytes counts as the copy.
	same := randomSha()
	uploaded := insertMedia(t, raw)
	_, err = raw.Exec(`UPDATE media SET content_hash = ? WHERE id = ?`, same, uploaded)
	require.NoError(t, err)
	got, err = lib.FindMediaBySourceSha(ctx, same)
	require.NoError(t, err)
	require.NotNil(t, got)
	require.Equal(t, uploaded, got.Id)
}

func TestCopyLibrarySetMediaFolderOnlyFillsAnEmptyPointer(t *testing.T) {
	rep, raw := libraryDB(t)
	lib, j, ctx := rep.Library(), newJanitor(t, raw), context.Background()
	a := mustCreate(t, lib, j, 0, "copy a "+tag())
	b := mustCreate(t, lib, j, 0, "copy b "+tag())
	m := insertMedia(t, raw)

	require.NoError(t, lib.SetMediaFolder(ctx, m, 0), "0 is a no-op")
	require.False(t, mediaFolder(t, raw, m).Valid)

	require.ErrorIs(t, lib.SetMediaFolder(ctx, m, 987654321), entity.ErrFolderNotFound)
	require.False(t, mediaFolder(t, raw, m).Valid)

	require.NoError(t, lib.SetMediaFolder(ctx, m, a.Id))
	require.EqualValues(t, a.Id, mediaFolder(t, raw, m).Int64)

	require.NoError(t, lib.SetMediaFolder(ctx, m, b.Id), "a filed row is not an error")
	require.EqualValues(t, a.Id, mediaFolder(t, raw, m).Int64, "and it stays where it was")

	require.NoError(t, lib.SetMediaFolder(ctx, 987654321, a.Id), "a missing media row matches nothing")
}

// The copy handler reads the file's folder through this narrow method (not on dependency.Library).
func TestCopyLibraryFileFolderID(t *testing.T) {
	rep, raw := libraryDB(t)
	lib, j, ctx := rep.Library(), newJanitor(t, raw), context.Background()
	_, owner := insertAdmin(t, raw)
	r, ok := lib.(interface {
		FileFolderID(context.Context, int) (int, error)
	})
	require.True(t, ok, "*library.Store must keep FileFolderID for the copy handler")

	f := mustCreate(t, lib, j, 0, "ff "+tag())
	inFolder, _ := insertFile(t, raw, "image/png", "team", owner, f.Id)
	loose, _ := insertFile(t, raw, "image/png", "team", owner, 0)

	id, err := r.FileFolderID(ctx, inFolder)
	require.NoError(t, err)
	require.Equal(t, f.Id, id)
	id, err = r.FileFolderID(ctx, loose)
	require.NoError(t, err)
	require.Zero(t, id)
	_, err = r.FileFolderID(ctx, 987654321)
	require.ErrorIs(t, err, sql.ErrNoRows)
}

// A dedup hit on upload keeps the first uploader's name, size and source sha (COALESCE).
func TestCopyLibraryMediaLibraryMetaKeepsTheFirstValues(t *testing.T) {
	rep, raw := libraryDB(t)
	ctx := context.Background()
	m := insertMedia(t, raw)
	sha := randomSha()

	require.NoError(t, rep.Media().SetMediaLibraryMeta(ctx, m, entity.MediaLibraryMeta{
		FileName:     sql.NullString{String: "first.png", Valid: true},
		SizeBytes:    sql.NullInt64{Int64: 100, Valid: true},
		SourceSha256: sql.NullString{String: sha, Valid: true},
	}))
	require.NoError(t, rep.Media().SetMediaLibraryMeta(ctx, m, entity.MediaLibraryMeta{
		FileName:     sql.NullString{String: "second.png", Valid: true},
		SizeBytes:    sql.NullInt64{Int64: 200, Valid: true},
		SourceSha256: sql.NullString{String: randomSha(), Valid: true},
	}))
	var name string
	var size int64
	var src string
	require.NoError(t, raw.QueryRow(`SELECT lib_file_name, lib_size_bytes, lib_source_sha256 FROM media WHERE id = ?`, m).
		Scan(&name, &size, &src))
	require.Equal(t, "first.png", name)
	require.EqualValues(t, 100, size)
	require.Equal(t, sha, src)

	got, err := rep.Library().FindMediaBySourceSha(ctx, sha)
	require.NoError(t, err)
	require.NotNil(t, got)
	require.Equal(t, m, got.Id)
}
