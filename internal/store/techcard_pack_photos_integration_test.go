package store

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/stretchr/testify/require"
)

// The pack photos (0452): the list comes back in order with the cover in packed_media_id, an older
// client that sends the stored cover alone keeps the further photos, another cover is a one-photo
// list, and every photo is a media carrier.
func TestTechCardPackedPhotosRoundTrip(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()
	f := newPackagingPickFixture(t, ctx)
	T := f.st.TechCards()
	addMedia := func(name string) int {
		t.Helper()
		id, err := f.st.Media().AddMedia(ctx, &entity.MediaItem{
			FullSizeMediaURL: "https://x/" + name + "-f.jpg", FullSizeWidth: 100, FullSizeHeight: 100,
			ThumbnailMediaURL: "https://x/" + name + "-t.jpg", ThumbnailWidth: 10, ThumbnailHeight: 10,
			CompressedMediaURL: "https://x/" + name + "-c.jpg", CompressedWidth: 50, CompressedHeight: 50,
		})
		require.NoError(t, err)
		return id
	}
	second, third := addMedia("packed-2"), addMedia("packed-3")
	cover := f.packedMedia

	read := func() *entity.TechCardPackaging {
		t.Helper()
		card, err := T.GetTechCardById(ctx, f.card)
		require.NoError(t, err)
		require.NotNil(t, card.Packaging)
		return card.Packaging
	}

	require.NoError(t, f.save(t, ctx, nil, &entity.TechCardPackaging{
		PackedMediaId: packagingPickInt(cover), PackedMediaIds: []int{cover, third, second},
	}))
	p := read()
	require.Equal(t, []int{cover, third, second}, p.PackedMediaIds)
	require.Equal(t, packagingPickInt(cover), p.PackedMediaId)

	// Carrying the read card back changes nothing.
	require.NoError(t, f.save(t, ctx, nil, p))
	require.Equal(t, []int{cover, third, second}, read().PackedMediaIds)

	// Every photo is a media carrier: the library names the card, the delete is refused.
	usage, err := f.st.Media().GetMediaUsage(ctx, []int{cover, second, third})
	require.NoError(t, err)
	for _, id := range []int{cover, second, third} {
		require.Len(t, usage[id], 1, "media %d is held once by the card", id)
		require.Equal(t, f.card, usage[id][0].EntityId)
	}
	_, err = testDB.ExecContext(ctx, `DELETE FROM media WHERE id = ?`, second)
	require.Error(t, err)

	// An older client: the stored cover alone keeps the further photos.
	require.NoError(t, f.save(t, ctx, nil, &entity.TechCardPackaging{PackedMediaId: packagingPickInt(cover)}))
	require.Equal(t, []int{cover, third, second}, read().PackedMediaIds)

	// An older client with another cover: a one-photo list.
	require.NoError(t, f.save(t, ctx, nil, &entity.TechCardPackaging{PackedMediaId: packagingPickInt(third)}))
	p = read()
	require.Equal(t, []int{third}, p.PackedMediaIds)
	require.Equal(t, packagingPickInt(third), p.PackedMediaId)

	// Reorder, then clear.
	require.NoError(t, f.save(t, ctx, nil, &entity.TechCardPackaging{
		PackedMediaId: packagingPickInt(second), PackedMediaIds: []int{second, cover},
	}))
	require.Equal(t, []int{second, cover}, read().PackedMediaIds)
	require.NoError(t, f.save(t, ctx, nil, &entity.TechCardPackaging{}))
	p = read()
	require.Empty(t, p.PackedMediaIds)
	require.False(t, p.PackedMediaId.Valid)
	var left int
	require.NoError(t, testDB.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM tech_card_packaging_media WHERE tech_card_id = ?`, f.card).Scan(&left))
	require.Zero(t, left)
}

// The composition label orientation: "" and vertical read vertical, horizontal comes back.
func TestTechCardCareLabelOrientationRoundTrip(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()
	f := newPackagingPickFixture(t, ctx)
	T := f.st.TechCards()

	saveCare := func(c *entity.TechCardCareLabel) string {
		t.Helper()
		card, err := T.GetTechCardById(ctx, f.card)
		require.NoError(t, err)
		edit := card.TechCardInsert
		edit.LabelsAware = true
		edit.CareLabel = c
		require.NoError(t, T.UpdateTechCard(ctx, f.card, &edit, card.LockVersion))
		card, err = T.GetTechCardById(ctx, f.card)
		require.NoError(t, err)
		require.NotNil(t, card.CareLabel)
		return card.CareLabel.Orientation
	}
	require.Equal(t, entity.CareLabelHorizontal, saveCare(&entity.TechCardCareLabel{Orientation: entity.CareLabelHorizontal}))
	var stored string
	require.NoError(t, testDB.QueryRowContext(ctx,
		`SELECT care_orientation FROM tech_card_care_label WHERE tech_card_id = ?`, f.card).Scan(&stored))
	require.Equal(t, entity.CareLabelHorizontal, stored)

	require.Equal(t, entity.CareLabelVertical, saveCare(&entity.TechCardCareLabel{Orientation: entity.CareLabelVertical}))
	require.NoError(t, testDB.QueryRowContext(ctx,
		`SELECT care_orientation FROM tech_card_care_label WHERE tech_card_id = ?`, f.card).Scan(&stored))
	require.Empty(t, stored, "vertical is stored as the pre-0452 default")
	require.Equal(t, entity.CareLabelVertical, saveCare(&entity.TechCardCareLabel{}))
}

// TestTechCardPackPhotosMigrationIsIdempotent: the boot applied 0452 on a fresh schema; re-running its
// Up body (as a mid-file crash would on the next boot) under ANSI_QUOTES — prod's global sql_mode — is a
// no-op, and the photo table keeps its cascade FK and the column its default.
func TestTechCardPackPhotosMigrationIsIdempotent(t *testing.T) {
	if os.Getenv("CI") == "" &&
		!strings.Contains(testCfg.DSN, "127.0.0.1") &&
		!strings.Contains(testCfg.DSN, "localhost") {
		t.Skip("skipping outside CI unless the DSN targets a local container (avoids the configured prod DB)")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	conn, err := testDB.Conn(ctx)
	require.NoError(t, err)
	defer conn.Close()
	_, err = conn.ExecContext(ctx, "SET SESSION sql_mode = CONCAT(@@SESSION.sql_mode, ',ANSI_QUOTES')")
	require.NoError(t, err)
	up := migrationUpBody(t, "0452_tech_card_pack_photos_care_orientation.sql")
	for i := 0; i < 2; i++ {
		_, err = conn.ExecContext(ctx, up)
		require.NoError(t, err, "re-run %d", i+1)
	}
	var n int
	require.NoError(t, conn.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM information_schema.table_constraints
		WHERE table_schema = DATABASE() AND table_name = 'tech_card_packaging_media'
		  AND constraint_name IN ('fk_tc_packaging_media_packaging', 'fk_tc_packaging_media_media')`).Scan(&n))
	require.Equal(t, 2, n)
	var def, nullable string
	require.NoError(t, conn.QueryRowContext(ctx, `
		SELECT COLUMN_DEFAULT, IS_NULLABLE FROM information_schema.COLUMNS
		WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_care_label' AND COLUMN_NAME = 'care_orientation'`).
		Scan(&def, &nullable))
	require.Equal(t, "", def)
	require.Equal(t, "NO", nullable)
}
