package store

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/cache"
	"github.com/jekabolt/grbpwr-manager/internal/dto"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/stretchr/testify/require"
)

// TestCanonicalSlugUsesDefaultLanguage is the wiring/acceptance test for problem 030: both the product
// pretty slug and the archive timeline slug must be built from the canonical (default-language)
// translation, not the order-dependent first row. It sets a NON-minimal language id as the sole
// default, then asserts each slug carries the default-language name and is stable across repeated reads.
func TestCanonicalSlugUsesDefaultLanguage(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	cfg := *testCfg
	cfg.Automigrate = true
	s, err := NewForTest(ctx, cfg)
	require.NoError(t, err)
	defer s.Close()

	// Two languages, default assigned to the LARGER id so "default != smallest id" is exercised.
	var l1, l2 int
	rows, err := testDB.QueryContext(ctx, "SELECT id FROM language ORDER BY id LIMIT 2")
	require.NoError(t, err)
	ids := []int{}
	for rows.Next() {
		var id int
		require.NoError(t, rows.Scan(&id))
		ids = append(ids, id)
	}
	require.NoError(t, rows.Close())
	require.GreaterOrEqual(t, len(ids), 2, "need at least two languages")
	l1, l2 = ids[0], ids[1]

	// Snapshot & restore the default-language flag (the suite shares one DB).
	var origDefault int
	_ = testDB.QueryRowContext(ctx, "SELECT id FROM language WHERE is_default = 1 LIMIT 1").Scan(&origDefault)
	reinit := func() {
		di, err := s.Cache().GetDictionaryInfo(ctx)
		require.NoError(t, err)
		hf, err := s.Hero().GetHero(ctx)
		require.NoError(t, err)
		require.NoError(t, cache.InitConsts(ctx, di, hf))
	}
	t.Cleanup(func() {
		cctx := context.Background()
		_, _ = testDB.ExecContext(cctx, "UPDATE language SET is_default = (id = ?)", origDefault)
	})

	_, err = testDB.ExecContext(ctx, "UPDATE language SET is_default = (id = ?)", l2)
	require.NoError(t, err)
	reinit() // cache.GetLanguages() now reports l2 as default

	mediaID, err := s.Media().AddMedia(ctx, &entity.MediaItem{
		FullSizeMediaURL: "https://x/f.jpg", FullSizeWidth: 100, FullSizeHeight: 100,
		ThumbnailMediaURL: "https://x/t.jpg", ThumbnailWidth: 10, ThumbnailHeight: 10,
		CompressedMediaURL: "https://x/c.jpg", CompressedWidth: 50, CompressedHeight: 50,
	})
	require.NoError(t, err)

	// --- product: the pretty slug is built in dto.ConvertToPbProductFull. Drive it with a ColorwayFull
	// whose translations list the NON-default (l1) first, to prove the default (l2) name wins regardless
	// of order. (Built in-memory: AddProduct is unrelated-broken at this base by the 0146 season CHECK.)
	pf := &entity.ColorwayFull{
		Product: &entity.Colorway{
			Id:  1,
			SKU: "SS26-00001-BLK",
			ProductDisplay: entity.ColorwayDisplay{
				ProductBody: entity.ColorwayBody{
					ProductBodyInsert: entity.ColorwayBodyInsert{
						Brand: "ACME", Color: "black", ColorCode: "BLK",
						ColorHexOverride: sql.NullString{String: "#000000", Valid: true},
						CountryOfOrigin:  "IT", TopCategoryId: 1, TargetGender: entity.Unisex, Season: entity.SeasonSS,
					},
					Translations: []entity.ColorwayTranslationInsert{
						{LanguageId: l1, Name: "Alpha Minimal", Description: "d"},
						{LanguageId: l2, Name: "Beta Default", Description: "d"},
					},
				},
			},
		},
	}
	productSlug := func() string {
		pb, err := dto.ConvertToPbProductFull(pf)
		require.NoError(t, err)
		return pb.GetProduct().GetSlug()
	}
	ps := productSlug()
	require.Contains(t, ps, "beta-default", "product slug must use the default-language name")
	require.NotContains(t, ps, "alpha-minimal", "product slug must not use the non-default first translation")
	require.Equal(t, ps, productSlug(), "product slug must be stable across repeated reads")

	// --- archive: same policy, observed directly on the store-built slug ---
	aid, err := s.Archive().AddArchive(ctx, &entity.ArchiveInsert{
		Tag: "t30", ThumbnailId: mediaID,
		Translations: []entity.ArchiveTranslation{
			{LanguageId: l1, Heading: "Alpha Timeline"},
			{LanguageId: l2, Heading: "Beta Timeline"},
		},
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		cctx := context.Background()
		_, _ = testDB.ExecContext(cctx, "DELETE FROM archive_translation WHERE archive_id = ?", aid)
		_, _ = testDB.ExecContext(cctx, "DELETE FROM archive WHERE id = ?", aid)
	})

	archiveSlug := func() string {
		full, err := s.Archive().GetArchiveById(ctx, aid)
		require.NoError(t, err)
		return full.ArchiveList.Slug
	}
	as := archiveSlug()
	require.Contains(t, as, "beta-timeline", "archive slug must use the default-language heading")
	require.NotContains(t, as, "alpha-timeline", "archive slug must not use the non-default first translation")
	require.Equal(t, as, archiveSlug(), "archive slug must be stable across repeated reads")
}
