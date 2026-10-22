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

func TestSheetBindingSaveInvariantAndSidecarHygiene(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	cfg := *testCfg
	cfg.Automigrate = true
	s, err := NewForTest(ctx, cfg)
	require.NoError(t, err)
	defer s.Close()
	T := s.TechCards()

	var sizeID int
	require.NoError(t, testDB.QueryRowContext(ctx, "SELECT MIN(id) FROM size").Scan(&sizeID))
	present := func(v string) sql.NullString { return sql.NullString{String: v, Valid: true} }
	const (
		lineMainA  = "01SHMAINLINEA0000000000001"
		lineMainB  = "01SHMAINLINEB0000000000002"
		lineLining = "01SHLININGLINE000000000003"
		pieceKey   = "01SHPIECEFRONT000000000004"
		sheetKey   = "01SHSHEETFRONT000000000005"
		sheetName  = "front shell"
		sheetURL   = "https://cdn.example/tech-card-patterns/front.dxf"
	)
	mainBom := []entity.TechCardBomItem{
		{LineKey: lineMainA, Section: entity.BomSectionFabric, Name: "shell A", Purpose: present("main")},
		{LineKey: lineMainB, Section: entity.BomSectionFabric, Name: "shell B", Purpose: present("main")},
	}
	patterns := func(purpose, line string) []entity.TechCardSizePattern {
		return []entity.TechCardSizePattern{{
			SizeId: sizeID, URL: sheetURL, Filename: present("front.dxf"), Name: present(sheetName),
			LineKey: sheetKey, FabricPurpose: present(purpose), BomLineKey: present(line),
		}}
	}
	card := func(bom []entity.TechCardBomItem, pats []entity.TechCardSizePattern) *entity.TechCardInsert {
		return &entity.TechCardInsert{
			Name: "Sheet binding hygiene", StyleNumber: present("SBH-1"),
			Stage: entity.TechCardStageProto, ApprovalState: entity.TechCardApprovalDraft,
			MeasurementUnit: entity.TechCardUnitMm,
			SizeIds:         []int{sizeID},
			BomItems:        bom,
			Pieces: []entity.TechCardPiece{{
				LineKey: pieceKey, Name: "front", PiecesPerGarment: 1, Grainline: "lengthwise",
			}},
			Patterns: pats,
		}
	}

	initial := card(mainBom, patterns("main", lineMainA))
	initial.PieceDxfAliasesSet = true
	initial.PieceDxfAliases = []entity.TechCardPieceDxfAlias{{
		FabricPurpose: "main", BomLineKey: lineMainA, BlockName: "FRONT", PieceLineKey: pieceKey,
	}}
	cardID, err := T.AddTechCard(ctx, initial)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = testDB.ExecContext(context.Background(), "DELETE FROM tech_card WHERE id = ?", cardID)
	})
	lock := 0
	resave := func(in *entity.TechCardInsert) error {
		err := T.UpdateTechCard(ctx, cardID, in, lock)
		if err == nil {
			lock++
		}
		return err
	}

	// Deleting the echoed line is safe while another line still carries the authoritative purpose.
	// Both the sheet and alias must shed that stale compatibility key on write.
	normalized := card(mainBom[1:], patterns("main", lineMainA))
	normalized.PieceDxfAliasesSet = true
	normalized.PieceDxfAliases = initial.PieceDxfAliases
	require.NoError(t, resave(normalized))
	got, err := T.GetTechCardById(ctx, cardID)
	require.NoError(t, err)
	require.Len(t, got.Patterns, 1)
	require.False(t, got.Patterns[0].BomLineKey.Valid, "stale pattern compatibility line must be cleared")
	require.Len(t, got.PieceDxfAliases, 1)
	require.Empty(t, got.PieceDxfAliases[0].BomLineKey, "stale alias compatibility line must be cleared")

	// The same binding becomes newly dead when the last MAIN line moves. The unchanged payload value
	// must not bypass the prospective-BOM invariant, and the violation names the affected sheet.
	liningOnly := []entity.TechCardBomItem{{
		LineKey: lineMainB, Section: entity.BomSectionLining, Name: "lining", Purpose: present("lining"),
	}}
	err = resave(card(liningOnly, patterns("main", "")))
	var violation *entity.ValidationError
	require.ErrorAs(t, err, &violation)
	require.Equal(t, "patterns", violation.Field)
	require.Equal(t, "new_unresolved_sheets", violation.Reason)
	require.Contains(t, violation.Conflicting, sheetName)

	// A legacy orphan stays saveable: direct SQL simulates a row that predates this invariant. The
	// save may preserve it and then repair it; only growth of the unresolved set is prohibited.
	_, err = testDB.ExecContext(ctx,
		`UPDATE tech_card_size_pattern SET fabric_purpose = 'mesh', bom_line_key = NULL WHERE tech_card_id = ?`, cardID)
	require.NoError(t, err)
	require.NoError(t, resave(card(mainBom[1:], patterns("mesh", ""))))
	require.NoError(t, resave(card(mainBom[1:], patterns("main", lineMainB))))

	// A brand-new DXF never gets the legacy exemption, even when both binding fields are explicit.
	withOrphan := card(mainBom[1:], append(patterns("main", lineMainB), entity.TechCardSizePattern{
		SizeId: sizeID, URL: "https://cdn.example/tech-card-patterns/unbound.dxf",
		Filename: present("unbound.dxf"), Name: present("unbound"), LineKey: "01SHSHEETUNBOUND0000000006",
		FabricPurpose: present(""), BomLineKey: present(""),
	}))
	err = resave(withOrphan)
	require.ErrorAs(t, err, &violation)
	require.Equal(t, "live_binding_required", violation.Reason)

	// Seed both sidecars under MAIN. LINING already has a piece-area row, so the move exercises both
	// policies: size-index rekeys, while piece-area keeps the target and discards the source.
	fingerprint := strings.Repeat("a", 64)
	_, err = testDB.ExecContext(ctx, `INSERT INTO tech_card_pattern_size_index
		(tech_card_id, scope_key, sheet_fingerprint, size_tokens, parsed_by)
		VALUES (?, 'main', ?, '["m"]', 'test')`, cardID, fingerprint)
	require.NoError(t, err)
	_, err = testDB.ExecContext(ctx, `INSERT INTO tech_card_piece_area
		(tech_card_id, scope_key, piece_line_key, area_cm2, sheet_fingerprint, parsed_by)
		VALUES (?, 'main', ?, 100, ?, 'test'), (?, 'lining', ?, 999, ?, 'test')`,
		cardID, pieceKey, fingerprint, cardID, pieceKey, fingerprint)
	require.NoError(t, err)
	twoScopes := []entity.TechCardBomItem{
		{LineKey: lineMainB, Section: entity.BomSectionFabric, Name: "shell", Purpose: present("main")},
		{LineKey: lineLining, Section: entity.BomSectionLining, Name: "lining", Purpose: present("lining")},
	}
	require.NoError(t, resave(card(twoScopes, patterns("lining", lineLining))))
	var n int
	require.NoError(t, testDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM tech_card_pattern_size_index
		WHERE tech_card_id = ? AND scope_key = 'main'`, cardID).Scan(&n))
	require.Zero(t, n)
	require.NoError(t, testDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM tech_card_pattern_size_index
		WHERE tech_card_id = ? AND scope_key = 'lining'`, cardID).Scan(&n))
	require.Equal(t, 1, n, "source size index must follow the only sheet destination")
	var area string
	require.NoError(t, testDB.QueryRowContext(ctx, `SELECT area_cm2 FROM tech_card_piece_area
		WHERE tech_card_id = ? AND scope_key = 'lining'`, cardID).Scan(&area))
	require.Equal(t, "999.00", area, "an existing target measurement wins over the moved source")
	require.NoError(t, testDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM tech_card_piece_area
		WHERE tech_card_id = ? AND scope_key = 'main'`, cardID).Scan(&n))
	require.Zero(t, n)

	// An orphaned stored sheet used to make these side RPCs accept its dead bucket as if it were live.
	_, err = testDB.ExecContext(ctx,
		`UPDATE tech_card_size_pattern SET fabric_purpose = 'mesh', bom_line_key = NULL WHERE tech_card_id = ?`, cardID)
	require.NoError(t, err)
	_, err = T.PutTechCardPatternSizeIndex(ctx, entity.PatternSizeIndexWrite{
		TechCardId: cardID, ScopeKey: "mesh", SheetLineKeys: []string{sheetKey},
	})
	require.ErrorAs(t, err, &violation)
	require.Equal(t, "scope_not_live", violation.Reason)
	_, err = T.SaveTechCardPieceAreas(ctx, entity.PieceAreaWrite{
		TechCardId: cardID, ScopeKey: "mesh", SheetLineKeys: []string{sheetKey},
		Rows: []entity.PieceAreaInput{{PieceLineKey: pieceKey, AreaCm2: decimal.NewFromInt(10)}},
	})
	require.ErrorAs(t, err, &violation)
	require.Equal(t, "scope_not_live", violation.Reason)

	// The next successful card save repairs the sheet and removes unrelated dead buckets in the same
	// transaction, even though their scope names no current sheet move.
	_, err = testDB.ExecContext(ctx, `INSERT INTO tech_card_pattern_size_index
		(tech_card_id, scope_key, sheet_fingerprint, size_tokens, parsed_by)
		VALUES (?, 'mesh', ?, '[]', 'test')`, cardID, fingerprint)
	require.NoError(t, err)
	_, err = testDB.ExecContext(ctx, `INSERT INTO tech_card_piece_area
		(tech_card_id, scope_key, piece_line_key, area_cm2, sheet_fingerprint, parsed_by)
		VALUES (?, 'mesh', ?, 10, ?, 'test')`, cardID, pieceKey, fingerprint)
	require.NoError(t, err)
	require.NoError(t, resave(card(twoScopes, patterns("lining", lineLining))))
	require.NoError(t, testDB.QueryRowContext(ctx, `SELECT
		(SELECT COUNT(*) FROM tech_card_pattern_size_index WHERE tech_card_id = ? AND scope_key = 'mesh') +
		(SELECT COUNT(*) FROM tech_card_piece_area WHERE tech_card_id = ? AND scope_key = 'mesh')`,
		cardID, cardID).Scan(&n))
	require.Zero(t, n)
}
