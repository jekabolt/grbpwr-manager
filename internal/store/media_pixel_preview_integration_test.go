package store

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"image"
	"image/color"
	"image/png"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/pixelbackfill"
	"github.com/stretchr/testify/require"
)

const testPixelPreview = "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAYAAAAIEAYAAAA"

// 0488 is gated on information_schema: replaying it over a migrated schema is a no-op.
func TestPixelPreviewMigrationReplay(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cfg := *testCfg
	cfg.Automigrate = true
	s, err := NewForTest(ctx, cfg)
	require.NoError(t, err)
	defer s.Close()

	body, err := os.ReadFile("sql/0488_media_pixel_preview.sql")
	require.NoError(t, err)
	up, _ := splitMigration(t, string(body))
	require.NotEmpty(t, up)
	for pass := 1; pass <= 2; pass++ {
		for i, q := range up {
			_, err := testDB.ExecContext(ctx, q)
			require.NoError(t, err, "pass %d statement %d:\n%s", pass, i, q)
		}
	}
	var n int
	require.NoError(t, testDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM information_schema.COLUMNS
		WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'media' AND COLUMN_NAME = 'pixel_preview'`).Scan(&n))
	require.Equal(t, 1, n)
}

// AddMedia writes pixel_preview, SELECT * reads it back, an empty preview stays NULL, and the
// hand-written archive SELECTs (GetArchiveById, GetArchivesPaged) carry it on the thumbnail.
func TestMediaPixelPreviewRoundTrip(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cfg := *testCfg
	cfg.Automigrate = true
	s, err := NewForTest(ctx, cfg)
	require.NoError(t, err)
	defer s.Close()

	id, err := s.Media().AddMedia(ctx, &entity.MediaItem{
		FullSizeMediaURL: "https://x/pp-f.webp", FullSizeWidth: 800, FullSizeHeight: 1000,
		CompressedMediaURL: "https://x/pp-c.webp", CompressedWidth: 400, CompressedHeight: 500,
		ThumbnailMediaURL: "https://x/pp-t.webp", ThumbnailWidth: 864, ThumbnailHeight: 1080,
		BlurHash:     sql.NullString{String: "LEHV6nWB2yk8pyo0adR*.7kCMdnj", Valid: true},
		PixelPreview: sql.NullString{String: testPixelPreview, Valid: true},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = testDB.Exec(`DELETE FROM media WHERE id = ?`, id) })

	bare, err := s.Media().AddMedia(ctx, &entity.MediaItem{
		FullSizeMediaURL: "https://x/pp2.webp", CompressedMediaURL: "https://x/pp2.webp", ThumbnailMediaURL: "https://x/pp2.webp",
		PixelPreview: sql.NullString{String: "", Valid: true},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = testDB.Exec(`DELETE FROM media WHERE id = ?`, bare) })

	m, err := s.Media().GetMediaById(ctx, id)
	require.NoError(t, err)
	require.Equal(t, testPixelPreview, m.PixelPreview.String)
	require.Equal(t, "LEHV6nWB2yk8pyo0adR*.7kCMdnj", m.BlurHash.String, "blurhash untouched")

	var isNull bool
	require.NoError(t, testDB.QueryRowContext(ctx, `SELECT pixel_preview IS NULL FROM media WHERE id = ?`, bare).Scan(&isNull))
	require.True(t, isNull, "an empty preview binds NULL so the backfill picks the row up")

	var langID int
	require.NoError(t, testDB.QueryRowContext(ctx, "SELECT MIN(id) FROM language").Scan(&langID))
	aid, err := s.Archive().AddArchive(ctx, &entity.ArchiveInsert{
		Tag: "pp", ThumbnailId: id,
		Translations: []entity.ArchiveTranslation{{LanguageId: langID, Heading: "pixel preview"}},
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = testDB.Exec("DELETE FROM archive_translation WHERE archive_id = ?", aid)
		_, _ = testDB.Exec("DELETE FROM archive WHERE id = ?", aid)
	})

	af, err := s.Archive().GetArchiveById(ctx, aid)
	require.NoError(t, err)
	require.Equal(t, testPixelPreview, af.ArchiveList.Thumbnail.PixelPreview.String)
	require.Equal(t, "LEHV6nWB2yk8pyo0adR*.7kCMdnj", af.ArchiveList.Thumbnail.BlurHash.String)

	list, _, err := s.Archive().GetArchivesPaged(ctx, 100, 0, entity.Descending)
	require.NoError(t, err)
	found := false
	for _, a := range list {
		if a.Id == aid {
			found = true
			require.Equal(t, testPixelPreview, a.Thumbnail.PixelPreview.String)
		}
	}
	require.True(t, found, "archive listed")
}

// The backfill fills only NULL raster rows, skips video, never overwrites, leaves a row whose
// download fails NULL, and a second run writes nothing.
func TestPixelPreviewBackfill(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cfg := *testCfg
	cfg.Automigrate = true
	s, err := NewForTest(ctx, cfg)
	require.NoError(t, err)
	defer s.Close()
	_, err = testDB.ExecContext(ctx, `UPDATE media SET pixel_preview = 'data:image/png;base64,pre' WHERE pixel_preview IS NULL`)
	require.NoError(t, err)

	add := func(thumb, preview string) int {
		id, err := s.Media().AddMedia(ctx, &entity.MediaItem{
			FullSizeMediaURL: thumb, CompressedMediaURL: thumb, ThumbnailMediaURL: thumb,
			PixelPreview: sql.NullString{String: preview, Valid: preview != ""},
		})
		require.NoError(t, err)
		t.Cleanup(func() { _, _ = testDB.Exec(`DELETE FROM media WHERE id = ?`, id) })
		return id
	}
	red := add("https://x/bf-red.webp", "")
	broken := add("https://x/bf-broken.webp", "")
	video := add("https://x/bf-video.MP4", "")
	done := add("https://x/bf-done.webp", testPixelPreview)

	var redPNG bytes.Buffer
	img := image.NewRGBA(image.Rect(0, 0, 40, 50))
	for y := 0; y < 50; y++ {
		for x := 0; x < 40; x++ {
			img.Set(x, y, color.RGBA{255, 0, 0, 255})
		}
	}
	require.NoError(t, png.Encode(&redPNG, img))
	fetched := map[string]int{}
	fetch := func(_ context.Context, url string) ([]byte, error) {
		fetched[url]++
		if strings.Contains(url, "broken") {
			return nil, errors.New("404")
		}
		return redPNG.Bytes(), nil
	}

	dry, err := pixelbackfill.Run(ctx, s.Media(), fetch, pixelbackfill.Options{Batch: 1, DryRun: true})
	require.NoError(t, err)
	require.Equal(t, pixelbackfill.Stats{Seen: 2, Written: 1, Failed: 1}, dry)
	var nulls int
	require.NoError(t, testDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM media WHERE pixel_preview IS NULL`).Scan(&nulls))
	require.Equal(t, 3, nulls, "dry-run writes nothing")

	st, err := pixelbackfill.Run(ctx, s.Media(), fetch, pixelbackfill.Options{Batch: 1})
	require.NoError(t, err)
	require.Equal(t, pixelbackfill.Stats{Seen: 2, Written: 1, Failed: 1}, st)
	require.Zero(t, fetched["https://x/bf-video.MP4"], "video is never downloaded")
	require.Zero(t, fetched["https://x/bf-done.webp"], "a filled row is not a candidate")

	m, err := s.Media().GetMediaById(ctx, red)
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(m.PixelPreview.String, "data:image/png;base64,"))
	m, err = s.Media().GetMediaById(ctx, done)
	require.NoError(t, err)
	require.Equal(t, testPixelPreview, m.PixelPreview.String, "never overwritten")
	for _, id := range []int{broken, video} {
		m, err = s.Media().GetMediaById(ctx, id)
		require.NoError(t, err)
		require.False(t, m.PixelPreview.Valid)
	}

	again, err := pixelbackfill.Run(ctx, s.Media(), fetch, pixelbackfill.Options{Batch: 50, Limit: 10})
	require.NoError(t, err)
	require.Equal(t, pixelbackfill.Stats{Seen: 1, Failed: 1}, again, "only the broken row is retried")

	ok, err := s.Media().SetMediaPixelPreview(ctx, done, "data:image/png;base64,other")
	require.NoError(t, err)
	require.False(t, ok)
}
