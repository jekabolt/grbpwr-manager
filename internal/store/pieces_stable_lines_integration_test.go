package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/cache"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

// TestPiecesStableLinesRecipeReferenceSurvives is the acceptance test for the WS4 pieces-keying root
// fix (S8 + the deferred half of 0159, migration 0168): cut-pieces are reconciled by line_key so a
// piece's id survives a save (was full-replaced, recreating ids every save), which is what lets a
// colourway recipe usage hold a real usage.piece_id FK RESTRICT. It proves (1) a recipe reference to
// a piece SURVIVES saving the tech card (the piece id does not churn), and (2) deleting a still-
// referenced piece is refused with a field-tagged error, not a silent dangle.
func TestPiecesStableLinesRecipeReferenceSurvives(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	cfg := *testCfg
	cfg.Automigrate = true
	s, err := NewForTest(ctx, cfg)
	require.NoError(t, err)
	defer s.Close()
	// Warm the in-process dictionary cache: colourway remint validates color_code against it
	// (same prep as commonWriteTestFixtures).
	{
		di, derr := s.Cache().GetDictionaryInfo(ctx)
		require.NoError(t, derr)
		hf, herr := s.Hero().GetHero(ctx)
		require.NoError(t, herr)
		require.NoError(t, cache.InitConsts(ctx, di, hf))
	}
	T := s.TechCards()
	ns := func(v string) sql.NullString { return sql.NullString{String: v, Valid: true} }

	// A style with one fabric BOM line and two keyed cut-pieces.
	piece := func(key, name string) entity.TechCardPiece {
		return entity.TechCardPiece{LineKey: key, Name: name, PiecesPerGarment: 1, Grainline: "lengthwise"}
	}
	card := func(pieces ...entity.TechCardPiece) *entity.TechCardInsert {
		return &entity.TechCardInsert{
			Name: "Pieces Stable Style", Stage: entity.TechCardStageProto, StyleNumber: ns("PCS-1"),
			Purpose: entity.TechCardPurposeSellable,
			MeasurementUnit: entity.TechCardUnitMm, ApprovalState: entity.TechCardApprovalDraft,
			// Linked colourways get reminted on card save — the style must carry a complete
			// sku_season or the remint fails (same fixture class as colorway_style_write tests).
			SeasonCode: sql.NullString{String: "SS", Valid: true},
			SeasonYear: sql.NullInt32{Int32: 2026, Valid: true},
			BomItems: []entity.TechCardBomItem{{LineKey: "FK1", Section: entity.TechCardBomSection("fabric"), Name: "Main Fabric"}},
			Pieces:   pieces,
		}
	}

	tcID, err := T.AddTechCard(ctx, card(piece("PK1", "Front"), piece("PK2", "Back")))
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = testDB.ExecContext(context.Background(), "DELETE FROM tech_card WHERE id = ?", tcID) })

	// Read back: line_keys round-trip, each piece has a stable id.
	c1, err := T.GetTechCardById(ctx, tcID)
	require.NoError(t, err)
	require.Len(t, c1.Pieces, 2)
	idByKey := map[string]int{}
	for _, p := range c1.Pieces {
		require.NotEmpty(t, p.LineKey, "piece line_key must round-trip")
		idByKey[p.LineKey] = p.Id
	}
	require.Contains(t, idByKey, "PK1")

	// A colourway (product) under the style (post-R1 merge).
	mediaID, err := s.Media().AddMedia(ctx, &entity.MediaItem{
		FullSizeMediaURL: "https://x/f.jpg", FullSizeWidth: 1, FullSizeHeight: 1,
		ThumbnailMediaURL: "https://x/t.jpg", ThumbnailWidth: 1, ThumbnailHeight: 1,
		CompressedMediaURL: "https://x/c.jpg", CompressedWidth: 1, CompressedHeight: 1,
	})
	require.NoError(t, err)
	res, err := testDB.ExecContext(ctx, `INSERT INTO product
		(sku, color, color_code, color_hex, country_of_origin, thumbnail_id, style_id)
		VALUES (?, 'c', 'BLK', '#000000', 'US', ?, ?)`, fmt.Sprintf("PCS-CW-%d", tcID), mediaID, tcID)
	require.NoError(t, err)
	cwID64, _ := res.LastInsertId()
	cwID := int(cwID64)
	t.Cleanup(func() { _, _ = testDB.ExecContext(context.Background(), "DELETE FROM product WHERE id = ?", cwID) })

	// Write a recipe usage that references PK1 by its stable piece_line_key (resolved to piece_id).
	_, err = T.UpdateColorwayRecipe(ctx, cwID, c1.LockVersion, []entity.TechCardColorwayUsage{
		{BomLineKey: "FK1", PieceLineKey: "PK1", Placement: ns("outer"),
			Consumption: decimal.NewNullDecimal(decimal.RequireFromString("1.0"))},
	})
	require.NoError(t, err)

	// The usage resolved piece_line_key -> the real piece_id (the recipe path never wrote piece_id before).
	c2, err := T.GetTechCardById(ctx, tcID)
	require.NoError(t, err)
	usagePieceID := requireUsagePieceID(t, c2, cwID)
	require.Equal(t, int64(idByKey["PK1"]), usagePieceID, "usage.piece_id must resolve to PK1's piece id")

	// Save the tech card again, editing PK1's name and keeping both keys. The piece id must be STABLE
	// (upsert-diff, not full-replace) so the recipe reference SURVIVES the save.
	require.NoError(t, T.UpdateTechCard(ctx, tcID, card(piece("PK1", "Front panel"), piece("PK2", "Back")), c2.LockVersion))
	c3, err := T.GetTechCardById(ctx, tcID)
	require.NoError(t, err)
	for _, p := range c3.Pieces {
		require.Equal(t, idByKey[p.LineKey], p.Id, "piece id must be stable across a save for line_key %s", p.LineKey)
		if p.LineKey == "PK1" {
			require.Equal(t, "Front panel", p.Name, "the edit persisted in place")
		}
	}
	require.Equal(t, usagePieceID, requireUsagePieceID(t, c3, cwID), "recipe reference survived the piece save")

	// Deleting the still-referenced piece PK1 (submit only PK2) is refused with a field-tagged error
	// (the usage.piece_id RESTRICT guard), and the whole save rolls back.
	err = T.UpdateTechCard(ctx, tcID, card(piece("PK2", "Back")), c3.LockVersion)
	require.Error(t, err, "deleting a piece a recipe usage references must be refused")
	var ve *entity.ValidationError
	require.True(t, errors.As(err, &ve), "the refusal must be a field-tagged validation error, got %v", err)

	// The rollback left PK1 in place — the reference is intact, nothing was orphaned.
	c4, err := T.GetTechCardById(ctx, tcID)
	require.NoError(t, err)
	require.Len(t, c4.Pieces, 2, "the failed delete rolled back; both pieces remain")
	require.Equal(t, usagePieceID, requireUsagePieceID(t, c4, cwID), "the recipe reference is still intact")

	// A NON-EXISTENT bom_line_key on an operation or a piece material is a field-tagged validation
	// error, never a silently-NULL link (no-silent-no-op norm; the beta A–L acceptance run caught the
	// operations case being accepted with 200 — C.10's negative probe). The legacy positional index
	// keeps its transition tolerance; only the stable-key form is strict.
	badOpCard := card(piece("PK1", "Front panel"), piece("PK2", "Back"))
	badOpCard.Operations = []entity.TechCardOperation{{Node: "neg probe", BomLineKey: "does-not-exist"}}
	err = T.UpdateTechCard(ctx, tcID, badOpCard, c4.LockVersion)
	require.Error(t, err, "unknown operation bom_line_key must be refused")
	require.True(t, errors.As(err, &ve), "operation bom_line_key refusal must be field-tagged, got %v", err)

	badPiece := piece("PK1", "Front panel")
	badPiece.Materials = []entity.TechCardPieceMaterial{{ColorwayID: cwID, BomLineKey: "does-not-exist"}}
	err = T.UpdateTechCard(ctx, tcID, card(badPiece, piece("PK2", "Back")), c4.LockVersion)
	require.Error(t, err, "unknown piece-material bom_line_key must be refused")
	require.True(t, errors.As(err, &ve), "piece-material bom_line_key refusal must be field-tagged, got %v", err)
}

// requireUsagePieceID returns the (single) recipe usage's resolved piece_id for the given colourway.
func requireUsagePieceID(t *testing.T, card *entity.TechCard, cwID int) int64 {
	t.Helper()
	for i := range card.Colorways {
		if card.Colorways[i].Id == cwID {
			require.Len(t, card.Colorways[i].Usages, 1)
			u := card.Colorways[i].Usages[0]
			require.True(t, u.PieceId.Valid, "usage.piece_id must be a real FK")
			return u.PieceId.Int64
		}
	}
	t.Fatalf("colourway %d not found on the style read", cwID)
	return 0
}
