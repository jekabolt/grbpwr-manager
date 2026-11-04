package store

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/stretchr/testify/require"
)

// The compare-and-set of an edited sheet. It has to run on the real schema: the protection lives in
// the re-read inside the transaction, and on mocks "checked and wrote" looks like "wrote blindly".

// sheetRow reads the columns this write owns, directly and past the viewer predicate.
type sheetRow struct {
	ObjectKey        string
	PreviewObjectKey sql.NullString
	Sha256           string
	SizeBytes        int64
	ContentExcerpt   string
	ContentUpdatedBy string
	ContentUpdatedAt sql.NullTime
	FileName         string
	ContentType      string
}

func readSheetRow(ctx context.Context, t *testing.T, id int) sheetRow {
	t.Helper()
	var r sheetRow
	require.NoError(t, testDB.QueryRowContext(ctx, `SELECT object_key, preview_object_key, sha256, size_bytes,
		COALESCE(content_excerpt, ''), content_updated_by, content_updated_at, file_name, content_type
		FROM library_file WHERE id = ?`, id).Scan(&r.ObjectKey, &r.PreviewObjectKey, &r.Sha256, &r.SizeBytes,
		&r.ContentExcerpt, &r.ContentUpdatedBy, &r.ContentUpdatedAt, &r.FileName, &r.ContentType))
	return r
}

// insertSheetFixture is a sheet with a preview and a marker in content_excerpt, so a write that
// touched the excerpt would show.
func insertSheetFixture(ctx context.Context, t *testing.T, uploader string) (id int, key, preview string) {
	t.Helper()
	id = insertLibraryFileFixture(ctx, t, "plan.xlsx", 10, uploader)
	key = noteObjectKey(t, "sheet-old")
	preview = noteObjectKey(t, "sheet-preview")
	_, err := testDB.ExecContext(ctx, `UPDATE library_file SET object_key = ?, preview_object_key = ?, sha256 = ?,
		content_excerpt = 'marker', content_type = 'application/x-sheet' WHERE id = ?`,
		key, preview, noteSha("old"), id)
	require.NoError(t, err)
	return id, key, preview
}

func TestLibraryFileContentCAS(t *testing.T) {
	filesGuard(t)

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	s, err := NewForTest(ctx, *testCfg)
	require.NoError(t, err)
	super := superCtx(ctx)

	save := func(id int, base string, force bool) (*entity.LibraryFileContentSaveResult, string) {
		t.Helper()
		key := noteObjectKey(t, "sheet-new")
		res, err := s.Files().SaveFileContent(super, entity.LibraryFileContentSave{
			FileId: id, BaseSha256: base, Force: force, ObjectKey: key,
			Sha256: noteSha("new"), SizeBytes: 3, EditedBy: "kirill",
		})
		require.NoError(t, err)
		return res, key
	}

	t.Run("a matching base writes, clears the preview and hands out both old keys", func(t *testing.T) {
		id, oldKey, oldPreview := insertSheetFixture(ctx, t, "pasha")

		res, newKey := save(id, noteSha("old"), false)
		require.False(t, res.Conflict)
		require.Equal(t, noteSha("new"), res.CurrentSha256)
		require.Equal(t, int64(3), res.SizeBytes)
		require.Equal(t, "kirill", res.LastEditedBy)
		require.True(t, res.LastEditedAt.Valid)
		require.Equal(t, oldKey, res.PreviousObjectKey)
		require.Equal(t, oldPreview, res.PreviousPreviewKey)

		row := readSheetRow(ctx, t, id)
		require.Equal(t, newKey, row.ObjectKey)
		require.Equal(t, noteSha("new"), row.Sha256)
		require.Equal(t, int64(3), row.SizeBytes)
		require.Equal(t, "kirill", row.ContentUpdatedBy)
		require.True(t, row.ContentUpdatedAt.Valid)
		require.Equal(t, res.LastEditedAt.Time, row.ContentUpdatedAt.Time)
		require.False(t, row.PreviewObjectKey.Valid, "a stale preview would be a lie")
		require.Equal(t, "marker", row.ContentExcerpt, "the excerpt is not this write's")
		require.Equal(t, "plan.xlsx", row.FileName)
		require.Equal(t, "application/x-sheet", row.ContentType)
	})

	t.Run("a stale base conflicts and writes nothing", func(t *testing.T) {
		id, oldKey, oldPreview := insertSheetFixture(ctx, t, "pasha")
		before := readSheetRow(ctx, t, id)

		res, _ := save(id, noteSha("something else"), false)
		require.True(t, res.Conflict)
		require.Equal(t, noteSha("old"), res.CurrentSha256)
		require.Equal(t, int64(10), res.SizeBytes)
		require.Empty(t, res.PreviousObjectKey)
		require.Empty(t, res.PreviousPreviewKey)

		require.Equal(t, before, readSheetRow(ctx, t, id))
		require.Equal(t, oldKey, before.ObjectKey)
		require.Equal(t, oldPreview, before.PreviewObjectKey.String)
	})

	t.Run("an empty base is a base, not a pass", func(t *testing.T) {
		id, _, _ := insertSheetFixture(ctx, t, "pasha")
		res, _ := save(id, "", false)
		require.True(t, res.Conflict)
	})

	t.Run("the second of two saves from one base loses", func(t *testing.T) {
		id, _, _ := insertSheetFixture(ctx, t, "pasha")
		first, firstKey := save(id, noteSha("old"), false)
		require.False(t, first.Conflict)

		second, _ := save(id, noteSha("old"), false)
		require.True(t, second.Conflict)
		require.Equal(t, "kirill", second.LastEditedBy)
		require.True(t, second.LastEditedAt.Valid)
		require.Equal(t, firstKey, readSheetRow(ctx, t, id).ObjectKey)
	})

	t.Run("force writes over a stale base", func(t *testing.T) {
		id, oldKey, _ := insertSheetFixture(ctx, t, "pasha")
		res, newKey := save(id, "stale", true)
		require.False(t, res.Conflict)
		require.Equal(t, oldKey, res.PreviousObjectKey)
		require.Equal(t, newKey, readSheetRow(ctx, t, id).ObjectKey)
	})

	t.Run("a file without a preview hands out no preview key", func(t *testing.T) {
		id, _, _ := insertSheetFixture(ctx, t, "pasha")
		_, err := testDB.ExecContext(ctx, `UPDATE library_file SET preview_object_key = NULL WHERE id = ?`, id)
		require.NoError(t, err)
		res, _ := save(id, noteSha("old"), false)
		require.Empty(t, res.PreviousPreviewKey)
	})

	t.Run("a stranger cannot write a restricted file, even with force", func(t *testing.T) {
		_, uploader := insertAdminFixture(ctx, t, "test-sheet-up")
		_, stranger := insertAdminFixture(ctx, t, "test-sheet-st")
		id, oldKey, _ := insertSheetFixture(ctx, t, uploader)
		_, err := testDB.ExecContext(ctx, `UPDATE library_file lf
			JOIN admins a ON a.username = ? SET lf.uploaded_by_id = a.id, lf.access_level = 'people' WHERE lf.id = ?`,
			uploader, id)
		require.NoError(t, err)

		in := entity.LibraryFileContentSave{FileId: id, ObjectKey: noteObjectKey(t, "hijack"), Sha256: noteSha("x"), EditedBy: stranger, Force: true}
		_, err = s.Files().SaveFileContent(viewerCtx(ctx, stranger), in)
		require.ErrorIs(t, err, sql.ErrNoRows)
		require.Equal(t, oldKey, readSheetRow(ctx, t, id).ObjectKey)

		// The positive control: the predicate, not something else, refused the stranger.
		_, err = s.Files().SaveFileContent(viewerCtx(ctx, uploader), in)
		require.NoError(t, err)
		require.Equal(t, in.ObjectKey, readSheetRow(ctx, t, id).ObjectKey)
	})

	t.Run("a file that is gone is not found", func(t *testing.T) {
		_, err := s.Files().SaveFileContent(super, entity.LibraryFileContentSave{
			FileId: 2147483600, ObjectKey: noteObjectKey(t, "ghost"), Sha256: noteSha("ghost"),
		})
		require.ErrorIs(t, err, sql.ErrNoRows)
	})

	t.Run("a save without a key or a sha is refused before the database", func(t *testing.T) {
		_, err := s.Files().SaveFileContent(super, entity.LibraryFileContentSave{FileId: 1, Sha256: "x"})
		require.Error(t, err)
		_, err = s.Files().SaveFileContent(super, entity.LibraryFileContentSave{FileId: 1, ObjectKey: "x"})
		require.Error(t, err)
	})
}
