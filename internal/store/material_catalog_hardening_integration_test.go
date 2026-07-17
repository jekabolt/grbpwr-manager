package store

import (
	"context"
	"database/sql"
	"regexp"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/stretchr/testify/require"
)

// TestMaterialCatalogHardening is the acceptance test for the catalog-hardening capabilities
// (migration 0184): the warehouse code auto-generates when left blank (#68) and stays unique, an
// explicit code is preserved and its duplicate rejected, the purpose mark defaults to 'both' and an
// explicit purpose round-trips (#40), and an optional catalog image resolves to MediaFull on read (#39).
func TestMaterialCatalogHardening(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	cfg := *testCfg
	cfg.Automigrate = true
	s, err := NewForTest(ctx, cfg)
	require.NoError(t, err)
	defer s.Close()
	tc := s.TechCards()

	// --- #68 auto-generate code when blank; #40 purpose defaults to 'both' ---
	id1, err := tc.CreateMaterial(ctx, &entity.MaterialInsert{Name: "Auto A", Section: "fabric", MaterialClass: "fabric"})
	require.NoError(t, err)
	m1, err := tc.GetMaterial(ctx, id1)
	require.NoError(t, err)
	require.True(t, m1.Code.Valid, "a blank code must be auto-generated")
	require.Regexp(t, regexp.MustCompile(`^FAB-\d{6,}$`), m1.Code.String, "auto code carries the type prefix + zero-padded id")
	require.Equal(t, string(entity.MaterialPurposeBoth), m1.Purpose, "purpose must default to both")

	// A second blank-code material gets a DISTINCT auto code (uniqueness by construction from the id).
	id2, err := tc.CreateMaterial(ctx, &entity.MaterialInsert{Name: "Auto B", Section: "fabric", MaterialClass: "fabric"})
	require.NoError(t, err)
	m2, err := tc.GetMaterial(ctx, id2)
	require.NoError(t, err)
	require.NotEqual(t, m1.Code.String, m2.Code.String, "auto-generated codes must be unique")

	// --- #68 an explicit operator-provided code is preserved (override) ---
	id3, err := tc.CreateMaterial(ctx, &entity.MaterialInsert{
		Name: "Manual", Section: "hardware", MaterialClass: "hardware",
		Code: sql.NullString{String: "CATHARD-MYCODE-1", Valid: true},
	})
	require.NoError(t, err)
	m3, err := tc.GetMaterial(ctx, id3)
	require.NoError(t, err)
	require.Equal(t, "CATHARD-MYCODE-1", m3.Code.String, "an explicit code must be preserved")

	// A duplicate explicit code among non-archived rows is rejected.
	_, err = tc.CreateMaterial(ctx, &entity.MaterialInsert{
		Name: "Dup", Section: "hardware", MaterialClass: "hardware",
		Code: sql.NullString{String: "CATHARD-MYCODE-1", Valid: true},
	})
	require.ErrorIs(t, err, entity.ErrMaterialCodeTaken)

	// --- #40 an explicit purpose round-trips ---
	id4, err := tc.CreateMaterial(ctx, &entity.MaterialInsert{
		Name: "Sample only", Section: "fabric", MaterialClass: "fabric",
		Purpose: string(entity.MaterialPurposeSample),
	})
	require.NoError(t, err)
	m4, err := tc.GetMaterial(ctx, id4)
	require.NoError(t, err)
	require.Equal(t, string(entity.MaterialPurposeSample), m4.Purpose)

	// --- #39 an image resolves to MediaFull on read ---
	mediaID, err := s.Media().AddMedia(ctx, &entity.MediaItem{
		FullSizeMediaURL: "https://x/f.jpg", FullSizeWidth: 100, FullSizeHeight: 100,
		ThumbnailMediaURL: "https://x/t.jpg", ThumbnailWidth: 10, ThumbnailHeight: 10,
		CompressedMediaURL: "https://x/c.jpg", CompressedWidth: 50, CompressedHeight: 50,
	})
	require.NoError(t, err)
	id5, err := tc.CreateMaterial(ctx, &entity.MaterialInsert{
		Name: "With image", Section: "fabric", MaterialClass: "fabric",
		ImageId: sql.NullInt32{Int32: int32(mediaID), Valid: true},
	})
	require.NoError(t, err)
	m5, err := tc.GetMaterial(ctx, id5)
	require.NoError(t, err)
	require.NotNil(t, m5.Image, "the image must resolve to a MediaFull on read")
	require.Equal(t, mediaID, m5.Image.Id)
	require.Equal(t, "https://x/f.jpg", m5.Image.FullSizeMediaURL)
}
