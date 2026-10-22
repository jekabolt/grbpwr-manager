package store

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

// The three seams the sheet-binding review found on the server: rows filed under a sorted line's raw
// key, an interlining line losing its sheets to an ordinary line of the old purpose, and a clone that
// aborts on a grandfathered orphan DXF. Each one silently lost or refused user data.

func sheetBindingReviewStore(t *testing.T) (context.Context, *MYSQLStore, int) {
	t.Helper()
	skipUnlessLocalContainer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	t.Cleanup(cancel)
	cfg := *testCfg
	cfg.Automigrate = true
	s, err := NewForTest(ctx, cfg)
	require.NoError(t, err)
	t.Cleanup(s.Close)
	var sizeID int
	require.NoError(t, testDB.QueryRowContext(ctx, "SELECT MIN(id) FROM size").Scan(&sizeID))
	return ctx, s, sizeID
}

func sheetBindingScopeRows(t *testing.T, ctx context.Context, table string, cardID int, scope string) int {
	t.Helper()
	var n int
	require.NoError(t, testDB.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM "+table+" WHERE tech_card_id = ? AND scope_key = ?", cardID, scope).Scan(&n))
	return n
}

// A sheet bound to line L before L was sorted keeps naming L; its size index and piece areas were
// filed under L. Once L carries purpose «main», L is no longer a live scope, but those rows are the
// main fabric's — an ordinary save must move them there, and the side RPCs must accept the raw key.
func TestSheetBindingRawLineSidecarsFollowTheLinesPurpose(t *testing.T) {
	ctx, s, sizeID := sheetBindingReviewStore(t)
	T := s.TechCards()
	present := func(v string) sql.NullString { return sql.NullString{String: v, Valid: true} }
	const (
		lineL    = "01SBRAWLINEL00000000000001"
		pieceKey = "01SBRAWPIECE00000000000002"
		sheetKey = "01SBRAWSHEET00000000000003"
	)
	card := func(purpose string) *entity.TechCardInsert {
		bom := entity.TechCardBomItem{LineKey: lineL, Section: entity.BomSectionFabric, Name: "shell"}
		if purpose != "" {
			bom.Purpose = present(purpose)
		}
		return &entity.TechCardInsert{
			Name: "Raw line sidecars", StyleNumber: present("SBR-1"),
			Stage: entity.TechCardStageProto, ApprovalState: entity.TechCardApprovalDraft,
			MeasurementUnit: entity.TechCardUnitMm,
			SizeIds:         []int{sizeID},
			BomItems:        []entity.TechCardBomItem{bom},
			Pieces: []entity.TechCardPiece{{
				LineKey: pieceKey, Name: "front", PiecesPerGarment: 1, Grainline: "lengthwise",
			}},
			Patterns: []entity.TechCardSizePattern{{
				SizeId: sizeID, URL: "https://cdn.example/tech-card-patterns/raw-front.dxf",
				Filename: present("raw-front.dxf"), Name: present("raw front"),
				LineKey: sheetKey, BomLineKey: present(lineL),
			}},
			PieceDxfAliasesSet: true,
			PieceDxfAliases: []entity.TechCardPieceDxfAlias{{
				BomLineKey: lineL, BlockName: "FRONT", PieceLineKey: pieceKey,
			}},
		}
	}
	cardID, err := T.AddTechCard(ctx, card(""))
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = testDB.ExecContext(context.Background(), "DELETE FROM tech_card WHERE id = ?", cardID)
	})

	// The legacy state: rows written while L was unsorted, then L sorted outside this save (an earlier
	// save, before the cleanup existed). Nothing here is a sheet move, so the reconcile is silent.
	fingerprint := strings.Repeat("b", 64)
	_, err = testDB.ExecContext(ctx, `INSERT INTO tech_card_pattern_size_index
		(tech_card_id, scope_key, sheet_fingerprint, size_tokens, parsed_by)
		VALUES (?, ?, ?, '["m"]', 'test')`, cardID, lineL, fingerprint)
	require.NoError(t, err)
	_, err = testDB.ExecContext(ctx, `INSERT INTO tech_card_piece_area
		(tech_card_id, scope_key, piece_line_key, area_cm2, sheet_fingerprint, parsed_by)
		VALUES (?, ?, ?, 321, ?, 'test')`, cardID, lineL, pieceKey, fingerprint)
	require.NoError(t, err)
	_, err = testDB.ExecContext(ctx,
		`UPDATE tech_card_bom_item SET purpose = 'main' WHERE tech_card_id = ? AND line_key = ?`, cardID, lineL)
	require.NoError(t, err)

	lock := 0
	require.NoError(t, T.UpdateTechCard(ctx, cardID, card("main"), lock))
	lock++
	require.Zero(t, sheetBindingScopeRows(t, ctx, "tech_card_pattern_size_index", cardID, lineL))
	require.Equal(t, 1, sheetBindingScopeRows(t, ctx, "tech_card_pattern_size_index", cardID, "main"),
		"the size index filed under the raw line key must follow the line into its purpose")
	require.Zero(t, sheetBindingScopeRows(t, ctx, "tech_card_piece_area", cardID, lineL))
	var area string
	require.NoError(t, testDB.QueryRowContext(ctx, `SELECT area_cm2 FROM tech_card_piece_area
		WHERE tech_card_id = ? AND scope_key = 'main'`, cardID).Scan(&area))
	require.Equal(t, "321.00", area, "the piece area filed under the raw line key must survive the save")

	// A raw-key row arriving next to an existing purpose set loses to it (the merge rule): the
	// destination is the newer, scope-wide measurement.
	_, err = testDB.ExecContext(ctx, `INSERT INTO tech_card_piece_area
		(tech_card_id, scope_key, piece_line_key, area_cm2, sheet_fingerprint, parsed_by)
		VALUES (?, ?, ?, 999, ?, 'test')`, cardID, lineL, pieceKey, fingerprint)
	require.NoError(t, err)
	require.NoError(t, T.UpdateTechCard(ctx, cardID, card("main"), lock))
	require.Zero(t, sheetBindingScopeRows(t, ctx, "tech_card_piece_area", cardID, lineL))
	require.NoError(t, testDB.QueryRowContext(ctx, `SELECT area_cm2 FROM tech_card_piece_area
		WHERE tech_card_id = ? AND scope_key = 'main'`, cardID).Scan(&area))
	require.Equal(t, "321.00", area, "an existing destination set wins over a raw-key source")

	// An older client still publishes the sheet's raw binding. Both side RPCs write under «main».
	_, err = T.PutTechCardPatternSizeIndex(ctx, entity.PatternSizeIndexWrite{
		TechCardId: cardID, ScopeKey: lineL, SheetLineKeys: []string{sheetKey},
		SizeTokens: []string{"s", "m"}, ParsedBy: "tester",
	})
	require.NoError(t, err)
	require.Zero(t, sheetBindingScopeRows(t, ctx, "tech_card_pattern_size_index", cardID, lineL))
	var tokens string
	require.NoError(t, testDB.QueryRowContext(ctx, `SELECT size_tokens FROM tech_card_pattern_size_index
		WHERE tech_card_id = ? AND scope_key = 'main'`, cardID).Scan(&tokens))
	require.Contains(t, tokens, `"s"`)

	res, err := T.SaveTechCardPieceAreas(ctx, entity.PieceAreaWrite{
		TechCardId: cardID, ScopeKey: lineL, SheetLineKeys: []string{sheetKey}, ParsedBy: "tester",
		Rows: []entity.PieceAreaInput{{PieceLineKey: pieceKey, AreaCm2: decimal.NewFromInt(123)}},
	})
	require.NoError(t, err)
	require.Equal(t, "main", res.ScopeKey)
	require.Zero(t, sheetBindingScopeRows(t, ctx, "tech_card_piece_area", cardID, lineL))
	require.NoError(t, testDB.QueryRowContext(ctx, `SELECT area_cm2 FROM tech_card_piece_area
		WHERE tech_card_id = ? AND scope_key = 'main'`, cardID).Scan(&area))
	require.Equal(t, "123.00", area)

	// A key naming no cloth on the card is still refused.
	_, err = T.PutTechCardPatternSizeIndex(ctx, entity.PatternSizeIndexWrite{
		TechCardId: cardID, ScopeKey: "01SBRAWGONE000000000000009", SheetLineKeys: []string{sheetKey},
	})
	var violation *entity.ValidationError
	require.ErrorAs(t, err, &violation)
	require.Equal(t, "scope_not_live", violation.Reason)
}

// An interlining line F and an ordinary rib share purpose «other»; a sheet stores {other, F}. F is
// re-sorted to «interfacing». The sheet still names F (a named interlining line wins), so its key,
// its alias and its sidecars must stay on F — not slide onto the rib's live «other» group.
func TestSheetBindingInterliningLineKeepsItsSheetsAfterAPurposeChange(t *testing.T) {
	ctx, s, sizeID := sheetBindingReviewStore(t)
	T := s.TechCards()
	present := func(v string) sql.NullString { return sql.NullString{String: v, Valid: true} }
	const (
		lineRib  = "01SBINTRIBLINE000000000001"
		lineFelt = "01SBINTFELTLINE00000000002"
		pieceKey = "01SBINTPIECE00000000000003"
		sheetKey = "01SBINTSHEET00000000000004"
	)
	card := func(feltPurpose string) *entity.TechCardInsert {
		return &entity.TechCardInsert{
			Name: "Interlining keeps its sheets", StyleNumber: present("SBI-1"),
			Stage: entity.TechCardStageProto, ApprovalState: entity.TechCardApprovalDraft,
			MeasurementUnit: entity.TechCardUnitMm,
			SizeIds:         []int{sizeID},
			BomItems: []entity.TechCardBomItem{
				{LineKey: lineRib, Section: entity.BomSectionFabric, Name: "rib", Purpose: present("other")},
				{LineKey: lineFelt, Section: entity.BomSectionInterlining, Name: "felt", Purpose: present(feltPurpose)},
			},
			Pieces: []entity.TechCardPiece{{
				LineKey: pieceKey, Name: "collar", PiecesPerGarment: 1, Grainline: "lengthwise",
			}},
			Patterns: []entity.TechCardSizePattern{{
				SizeId: sizeID, URL: "https://cdn.example/tech-card-patterns/felt.dxf",
				Filename: present("felt.dxf"), Name: present("felt"),
				LineKey: sheetKey, FabricPurpose: present("other"), BomLineKey: present(lineFelt),
			}},
			PieceDxfAliasesSet: true,
			PieceDxfAliases: []entity.TechCardPieceDxfAlias{{
				FabricPurpose: "other", BomLineKey: lineFelt, BlockName: "COLLAR", PieceLineKey: pieceKey,
			}},
		}
	}
	cardID, err := T.AddTechCard(ctx, card("other"))
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = testDB.ExecContext(context.Background(), "DELETE FROM tech_card WHERE id = ?", cardID)
	})
	_, err = testDB.ExecContext(ctx, `INSERT INTO tech_card_pattern_size_index
		(tech_card_id, scope_key, sheet_fingerprint, size_tokens, parsed_by)
		VALUES (?, ?, ?, '["m"]', 'test')`, cardID, lineFelt, strings.Repeat("c", 64))
	require.NoError(t, err)

	// The client leaves the line-scoped sheet and alias exactly as they were.
	require.NoError(t, T.UpdateTechCard(ctx, cardID, card("interfacing"), 0))

	got, err := T.GetTechCardById(ctx, cardID)
	require.NoError(t, err)
	require.Len(t, got.Patterns, 1)
	require.Equal(t, lineFelt, got.Patterns[0].BomLineKey.String, "the felt sheet must keep naming the felt")
	require.Len(t, got.PieceDxfAliases, 1)
	require.Equal(t, lineFelt, got.PieceDxfAliases[0].BomLineKey, "the felt alias must keep naming the felt")
	require.Equal(t, 1, sheetBindingScopeRows(t, ctx, "tech_card_pattern_size_index", cardID, lineFelt),
		"the felt's size index must not follow a rebind onto the rib")
	require.Zero(t, sheetBindingScopeRows(t, ctx, "tech_card_pattern_size_index", cardID, "other"))
}

// A source card legally keeps a DXF (and its block alias) whose purpose went dead. The season clone
// copies them verbatim; it must succeed and carry the binding as-is, while an ordinary create with
// the same payload is still refused.
func TestSheetBindingCloneCarriesAGrandfatheredOrphanDXF(t *testing.T) {
	ctx, s, sizeID := sheetBindingReviewStore(t)
	T := s.TechCards()
	present := func(v string) sql.NullString { return sql.NullString{String: v, Valid: true} }
	const (
		lineMain = "01SBCLNMAINLINE00000000001"
		pieceKey = "01SBCLNPIECE00000000000002"
		sheetKey = "01SBCLNSHEET00000000000003"
	)
	card := func(styleNumber, purpose, line string) *entity.TechCardInsert {
		return &entity.TechCardInsert{
			Name: "Clone grandfathered orphan", StyleNumber: present(styleNumber),
			Stage: entity.TechCardStageProto, ApprovalState: entity.TechCardApprovalDraft,
			MeasurementUnit: entity.TechCardUnitMm,
			SizeIds:         []int{sizeID},
			BomItems: []entity.TechCardBomItem{
				{LineKey: lineMain, Section: entity.BomSectionFabric, Name: "shell", Purpose: present("main")},
			},
			Pieces: []entity.TechCardPiece{{
				LineKey: pieceKey, Name: "front", PiecesPerGarment: 1, Grainline: "lengthwise",
			}},
			Patterns: []entity.TechCardSizePattern{{
				SizeId: sizeID, URL: "https://cdn.example/tech-card-patterns/orphan.dxf",
				Filename: present("orphan.dxf"), Name: present("orphan"),
				LineKey: sheetKey, FabricPurpose: present(purpose), BomLineKey: present(line),
			}},
			PieceDxfAliasesSet: true,
			PieceDxfAliases: []entity.TechCardPieceDxfAlias{{
				FabricPurpose: purpose, BomLineKey: line, BlockName: "FRONT", PieceLineKey: pieceKey,
			}},
		}
	}
	sourceID, err := T.AddTechCard(ctx, card("SBC-1", "main", lineMain))
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = testDB.ExecContext(context.Background(), "DELETE FROM tech_card WHERE id = ?", sourceID)
	})
	// The grandfathered state: both bindings name a purpose no line carries (written directly, the
	// way such rows predate the save invariant).
	_, err = testDB.ExecContext(ctx, `UPDATE tech_card_size_pattern SET fabric_purpose = 'mesh', bom_line_key = NULL
		WHERE tech_card_id = ?`, sourceID)
	require.NoError(t, err)
	_, err = testDB.ExecContext(ctx, `UPDATE tech_card_piece_dxf_block SET fabric_purpose = 'mesh', bom_line_key = ''
		WHERE tech_card_id = ?`, sourceID)
	require.NoError(t, err)
	source, err := T.GetTechCardById(ctx, sourceID)
	require.NoError(t, err)

	// The ordinary create API keeps refusing a new dead binding.
	_, err = T.AddTechCard(ctx, card("SBC-2", "mesh", ""))
	var violation *entity.ValidationError
	require.ErrorAs(t, err, &violation)

	cloneID, err := T.CloneTechCardForSeason(ctx, sourceID, source.LockVersion, card("SBC-3", "mesh", ""))
	require.NoError(t, err, "a grandfathered orphan DXF must not abort the clone")
	t.Cleanup(func() {
		_, _ = testDB.ExecContext(context.Background(), "DELETE FROM tech_card WHERE id = ?", cloneID)
	})
	clone, err := T.GetTechCardById(ctx, cloneID)
	require.NoError(t, err)
	require.Len(t, clone.Patterns, 1)
	require.Equal(t, "mesh", clone.Patterns[0].FabricPurpose.String, "the binding is copied as-is")
	require.False(t, clone.Patterns[0].BomLineKey.Valid && clone.Patterns[0].BomLineKey.String != "")
	require.Len(t, clone.PieceDxfAliases, 1)
	require.Equal(t, "mesh", clone.PieceDxfAliases[0].FabricPurpose)
}
