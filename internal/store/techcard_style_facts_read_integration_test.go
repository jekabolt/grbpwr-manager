package store

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/stretchr/testify/require"
)

// TestTechCardReadExposesStyleFacts locks the read-projection added so the constructor can display
// and edit-in-place the style catalogue facts written via UpdateStyle (fit / legacy composition /
// care) — they live on the tech_card row but were not on the tech-card read contract. Composition
// must come back as PLAIN TEXT (M1: the column is native JSON, stored as a quoted scalar; the read
// JSON_UNQUOTEs it), never a JSON-encoded value.
func TestTechCardReadExposesStyleFacts(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	cfg := *testCfg
	cfg.Automigrate = true
	s, err := NewForTest(ctx, cfg)
	require.NoError(t, err)
	defer s.Close()

	tcID := seedSpineStyle(ctx, t, "STYLE-FACTS")

	// Read the current shared lock, then write the facts via UpdateStyle (the sole writer). Season
	// and gender must be a valid combination for the tech_card CHECK constraints.
	pre, err := s.TechCards().GetTechCardById(ctx, tcID)
	require.NoError(t, err)
	const plainComposition = "80% wool, 20% polyamide"
	_, err = s.Products().UpdateStyle(ctx, tcID, pre.LockVersion, entity.StylePatch{
		TopCategoryId:    1,
		Season:           entity.SeasonEnum("SS"),
		TargetGender:     entity.GenderEnum("unisex"),
		Fit:              sql.NullString{String: "relaxed", Valid: true},
		Composition:      sql.NullString{String: plainComposition, Valid: true},
		CareInstructions: sql.NullString{String: "dry clean only", Valid: true},
	}, nil)
	require.NoError(t, err)

	tc, err := s.TechCards().GetTechCardById(ctx, tcID)
	require.NoError(t, err)
	require.Equal(t, "relaxed", tc.Fit.String, "fit surfaces on the tech-card read")
	require.Equal(t, "dry clean only", tc.CareInstructions.String, "care surfaces on the tech-card read")
	require.Equal(t, plainComposition, tc.Composition.String,
		"composition surfaces as plain text on the tech-card read (M1: JSON scalar unquoted)")
}

// TestUpdateStyleFieldMask locks the field-mask honoring: a partial update writes ONLY the named
// facts and leaves the rest at their stored value — so the tech card (fit/composition/care) and the
// colourway card (model-wears) can each save what they own without clobbering the other's facts.
func TestUpdateStyleFieldMask(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	cfg := *testCfg
	cfg.Automigrate = true
	s, err := NewForTest(ctx, cfg)
	require.NoError(t, err)
	defer s.Close()

	tcID := seedSpineStyle(ctx, t, "MASK")
	pre, err := s.TechCards().GetTechCardById(ctx, tcID)
	require.NoError(t, err)

	// Seed a full set (no mask ⇒ full replace).
	v1, err := s.Products().UpdateStyle(ctx, tcID, pre.LockVersion, entity.StylePatch{
		Brand:              "grbpwr",
		TopCategoryId:      1,
		Season:             entity.SeasonEnum("SS"),
		TargetGender:       entity.GenderEnum("unisex"),
		Fit:                sql.NullString{String: "regular", Valid: true},
		Composition:        sql.NullString{String: "100% cotton", Valid: true},
		CareInstructions:   sql.NullString{String: "wash cold", Valid: true},
		ModelWearsHeightCm: sql.NullInt32{Int32: 180, Valid: true},
	}, nil)
	require.NoError(t, err)

	// Masked update: only fit. Everything else (brand/care/model-wears/season) must be untouched, and
	// a fit-only change must NOT trip the season re-mint path.
	v2, err := s.Products().UpdateStyle(ctx, tcID, v1, entity.StylePatch{
		Fit: sql.NullString{String: "relaxed", Valid: true},
		// deliberately empty brand/care/model-wears — the mask must stop them reaching the row
	}, []string{"fit"})
	require.NoError(t, err)

	got, err := s.TechCards().GetTechCardById(ctx, tcID)
	require.NoError(t, err)
	require.Equal(t, "relaxed", got.Fit.String, "masked fit was written")
	require.Equal(t, "grbpwr", got.Brand.String, "brand outside the mask is untouched")
	require.Equal(t, "wash cold", got.CareInstructions.String, "care outside the mask is untouched")
	require.Equal(t, "100% cotton", got.Composition.String, "composition outside the mask is untouched")
	require.True(t, got.ModelWearsHeightCm.Valid && got.ModelWearsHeightCm.Int32 == 180,
		"model-wears outside the mask is untouched")
	require.Equal(t, "SS", got.SeasonCode.String, "season outside the mask is untouched")

	// Masked update from the colourway card's side: only model-wears. Fit (tech-card-owned) survives.
	_, err = s.Products().UpdateStyle(ctx, tcID, v2, entity.StylePatch{
		ModelWearsHeightCm: sql.NullInt32{Int32: 175, Valid: true},
		ModelWearsSizeId:   sql.NullInt32{Int32: 4, Valid: true},
	}, []string{"modelWearsHeightCm", "modelWearsSizeId"})
	require.NoError(t, err)
	got2, err := s.TechCards().GetTechCardById(ctx, tcID)
	require.NoError(t, err)
	require.True(t, got2.ModelWearsHeightCm.Valid && got2.ModelWearsHeightCm.Int32 == 175, "model-wears written")
	require.Equal(t, "relaxed", got2.Fit.String, "fit (tech-card-owned) survives a colourway-side model-wears save")
}
