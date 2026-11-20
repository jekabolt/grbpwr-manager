package store

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/stretchr/testify/require"
)

// Media-files P0 (0482–0484). Every step of the three files is guarded on information_schema, so a
// replay over an already-migrated schema must be a no-op, never a halt of the boot.
func TestLibraryMigrationsReplay(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	for _, f := range []string{
		"sql/0482_library_folder.sql",
		"sql/0483_library_media_columns.sql",
		"sql/0484_library_file_folder.sql",
	} {
		body, err := os.ReadFile(f)
		require.NoError(t, err)
		up, _ := splitMigration(t, string(body))
		require.NotEmpty(t, up, f)
		for pass := 1; pass <= 2; pass++ {
			for i, q := range up {
				_, err := testDB.ExecContext(ctx, q)
				require.NoError(t, err, "%s pass %d statement %d:\n%s", f, pass, i, q)
			}
		}
	}

	var n int
	require.NoError(t, testDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM information_schema.COLUMNS
		WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'media' AND COLUMN_NAME LIKE 'lib\_%'`).Scan(&n))
	require.Equal(t, 6, n, "media carries the six lib_ columns")
	require.NoError(t, testDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM information_schema.STATISTICS
		WHERE TABLE_SCHEMA = DATABASE() AND INDEX_NAME IN
		  ('idx_media_lib_folder', 'idx_media_lib_kind', 'idx_media_lib_source_sha', 'idx_library_file_folder')
		  AND SEQ_IN_INDEX = 1`).Scan(&n))
	require.Equal(t, 4, n, "all four library indexes exist exactly once")
}

// The generated lib_kind column and entity.MediaKindOf must agree on every URL shape the upload
// paths produce; the Go copy exists for rows the handler builds before they reach the DB.
func TestLibraryMediaKindMatchesGo(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	cfg := *testCfg
	cfg.Automigrate = true
	s, err := NewForTest(ctx, cfg)
	require.NoError(t, err)
	defer s.Close()

	shapes := []struct{ full, thumb string }{
		{"https://x/a.webp", "https://x/a-thumb.webp"},
		{"https://x/v.mp4", "https://x/v.mp4"},
		{"https://x/v.webm", "https://x/v.WEBM"},
		{"https://x/m.glb", "https://x/m-poster.png"},
		{"https://x/logo.svg", "https://x/logo.svg"},
		{"https://x/p.GIF", "https://x/p.gif"},
	}
	for i, sh := range shapes {
		id, err := s.Media().AddMedia(ctx, &entity.MediaItem{
			FullSizeMediaURL: sh.full, CompressedMediaURL: sh.full, ThumbnailMediaURL: sh.thumb,
			ContentHash: sql.NullString{String: fmt.Sprintf("%064x", 0x1ab0+i), Valid: true},
		})
		require.NoError(t, err)
		t.Cleanup(func() { _, _ = testDB.Exec(`DELETE FROM media WHERE id = ?`, id) })

		m, err := s.Media().GetMediaById(ctx, id)
		require.NoError(t, err)
		require.Equal(t, entity.MediaKindOf(sh.full, sh.thumb).String(), m.Kind, "%s / %s", sh.full, sh.thumb)
		require.False(t, m.FolderId.Valid, "a new upload sits in no folder")
	}
}

// SetMediaLibraryMeta fills the metadata once and never overwrites it: a re-upload that dedups onto
// an existing row must not rename it or re-attribute it.
func TestMediaLibraryMetaFillsOnce(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	cfg := *testCfg
	cfg.Automigrate = true
	s, err := NewForTest(ctx, cfg)
	require.NoError(t, err)
	defer s.Close()

	id, err := s.Media().AddMedia(ctx, &entity.MediaItem{
		FullSizeMediaURL: "https://x/meta.webp", CompressedMediaURL: "https://x/meta.webp", ThumbnailMediaURL: "https://x/meta.webp",
		ContentHash: sql.NullString{String: fmt.Sprintf("%064x", 0x2bc0), Valid: true},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = testDB.Exec(`DELETE FROM media WHERE id = ?`, id) })

	first := entity.MediaLibraryMeta{
		FileName:     sql.NullString{String: "look 01.jpg", Valid: true},
		SizeBytes:    sql.NullInt64{Int64: 1234, Valid: true},
		SourceSha256: sql.NullString{String: fmt.Sprintf("%064x", 0xfeed), Valid: true},
	}
	require.NoError(t, s.Media().SetMediaLibraryMeta(ctx, id, first))
	require.NoError(t, s.Media().SetMediaLibraryMeta(ctx, id, entity.MediaLibraryMeta{
		FileName:  sql.NullString{String: "other.jpg", Valid: true},
		SizeBytes: sql.NullInt64{Int64: 99, Valid: true},
	}))
	m, err := s.Media().GetMediaById(ctx, id)
	require.NoError(t, err)
	require.Equal(t, "look 01.jpg", m.FileName.String)
	require.Equal(t, int64(1234), m.SizeBytes.Int64)
	require.Equal(t, first.SourceSha256, m.SourceSha256)
	require.False(t, m.UploadedById.Valid)
	require.Equal(t, entity.LibraryKindValueImage, m.Kind)
}

// Sibling folder names are unique case-insensitively, at the root too (parent_key folds NULL to 0).
func TestLibraryFolderSiblingNamesAreUnique(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	res, err := testDB.ExecContext(ctx, `INSERT INTO library_folder (parent_id, name) VALUES (NULL, 'Shoots')`)
	require.NoError(t, err)
	rootID, err := res.LastInsertId()
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = testDB.Exec(`DELETE FROM library_folder WHERE parent_id = ?`, rootID)
		_, _ = testDB.Exec(`DELETE FROM library_folder WHERE id = ?`, rootID)
	})

	_, err = testDB.ExecContext(ctx, `INSERT INTO library_folder (parent_id, name) VALUES (NULL, 'shoots')`)
	require.Error(t, err, "a root duplicate differing only in case must hit the unique key")
	require.Contains(t, err.Error(), "uq_library_folder_sibling")

	_, err = testDB.ExecContext(ctx, `INSERT INTO library_folder (parent_id, name) VALUES (?, 'shoots')`, rootID)
	require.NoError(t, err, "the same name under another parent is fine")

	_, err = testDB.ExecContext(ctx, `DELETE FROM library_folder WHERE id = ?`, rootID)
	require.Error(t, err, "a folder with children cannot be deleted (ON DELETE RESTRICT)")
}
