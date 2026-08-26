package store

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/techcardarchive"
	"github.com/stretchr/testify/require"
)

// ─────────────────────────────────────────────────────────────────────────────
// Ф6.2 — the STORE half of «create colourways from archive», against a real MySQL.
//
// Two writes live here and neither exists anywhere else in this service:
//
//   - the piece→cloth mapping of ONE colourway. Its only other writer is the CARD SAVE, which
//     full-replaces the mapping of EVERY colour of the card — reusing that would have made this
//     action wipe the neighbours it was never asked about. The scoping is the property under test,
//     and it can only be tested where the rows actually are.
//   - the report stamp of an ALREADY COMMITTED import, guarded on that status so a screen cannot
//     rewrite the record of an import that never happened.
//
// The mock-level behaviour of the action (idempotency by colour, the caught UNIQUE, the fresh
// optimistic token, the report rewrite) is proved in internal/apisrv/admin without a database.
// What a database is needed for is the SQL: that the keys resolve to this card's own rows, that
// re-running replaces instead of duplicating, and that the guards actually refuse.
// ─────────────────────────────────────────────────────────────────────────────

// tcicPieceMaterialRow is one stored mapping row, read back by the identities the archive named.
type tcicPieceMaterialRow struct {
	PieceLineKey  string
	BomLineKey    sql.NullString
	FusingLineKey sql.NullString
	Note          sql.NullString
	DisplayOrder  int
}

// tcicReadMapping reads one colourway's mapping back THROUGH THE LINE KEYS, not through the ids the
// test happens to know. A read by id would pass against a row bound to the wrong BOM line.
func tcicReadMapping(ctx context.Context, t *testing.T, colorwayID int) []tcicPieceMaterialRow {
	t.Helper()
	rows, err := testDB.QueryContext(ctx, `
		SELECT p.line_key, fabric.line_key, fusing.line_key, pm.note, pm.display_order
		FROM tech_card_piece_material pm
		JOIN tech_card_piece p ON p.id = pm.piece_id
		LEFT JOIN tech_card_bom_item fabric ON fabric.id = pm.bom_item_id
		LEFT JOIN tech_card_bom_item fusing ON fusing.id = pm.fusing_bom_item_id
		WHERE pm.colorway_id = ?
		ORDER BY pm.display_order, p.line_key`, colorwayID)
	require.NoError(t, err)
	defer rows.Close()

	var out []tcicPieceMaterialRow
	for rows.Next() {
		var r tcicPieceMaterialRow
		require.NoError(t, rows.Scan(&r.PieceLineKey, &r.BomLineKey, &r.FusingLineKey, &r.Note, &r.DisplayOrder))
		out = append(out, r)
	}
	require.NoError(t, rows.Err())
	return out
}

// tcicColourway attaches a colourway (a product) to a style, the way the post-R1 merge has it.
func tcicColourway(ctx context.Context, t *testing.T, styleID int, code string) int {
	t.Helper()
	res, err := testDB.ExecContext(ctx, `INSERT INTO product
		(sku, color, color_code, color_hex, country_of_origin, style_id)
		VALUES (?, 'c', ?, '#000000', 'IT', ?)`,
		fmt.Sprintf("TCIC-%s-%d", code, styleID), code, styleID)
	require.NoError(t, err)
	id, err := res.LastInsertId()
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = testDB.ExecContext(context.Background(), "DELETE FROM product WHERE id = ?", id) })
	return int(id)
}

// TestImportColorwaysApply is the acceptance test for both store writes of Ф6.2.
func TestImportColorwaysApply(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	cfg := *testCfg
	cfg.Automigrate = true
	s, err := NewForTest(ctx, cfg)
	require.NoError(t, err)
	defer s.Close()
	T := s.TechCards()
	ns := func(v string) sql.NullString { return sql.NullString{String: v, Valid: true} }

	// A card carrying exactly what an archive addresses: BOM lines and cut-pieces with stable line
	// keys. The keys travel verbatim, so these ARE the archive's own strings.
	const (
		bomFabric = "TCIC-BOM-FABRIC"
		bomFusing = "TCIC-BOM-FUSING"
		pieceFrnt = "TCIC-PIECE-FRONT"
		pieceBack = "TCIC-PIECE-BACK"
	)
	tcID, err := T.AddTechCard(ctx, &entity.TechCardInsert{
		Name: "Import Colourways Style", Stage: entity.TechCardStageProto,
		StyleNumber:     ns(fmt.Sprintf("TCIC-%d", time.Now().UnixNano())),
		MeasurementUnit: entity.TechCardUnitMm, ApprovalState: entity.TechCardApprovalDraft,
		BomItems: []entity.TechCardBomItem{
			{LineKey: bomFabric, Section: entity.TechCardBomSection("fabric"), Name: "Main Fabric"},
			{LineKey: bomFusing, Section: entity.TechCardBomSection("fabric"), Name: "Fusing"},
		},
		Pieces: []entity.TechCardPiece{
			{LineKey: pieceFrnt, Name: "перед", PiecesPerGarment: 1, Grainline: "lengthwise"},
			{LineKey: pieceBack, Name: "спинка", PiecesPerGarment: 1, Grainline: "lengthwise"},
		},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = testDB.ExecContext(context.Background(), "DELETE FROM tech_card WHERE id = ?", tcID) })

	blk := tcicColourway(ctx, t, tcID, "BLK")
	olv := tcicColourway(ctx, t, tcID, "GRY")

	// ── 1. the mapping lands, resolved by line key ──────────────────────────────────
	require.NoError(t, T.ApplyImportedColorwayPieceMaterials(ctx, tcID, blk,
		[]entity.TechCardArchivePieceMaterial{
			{PieceLineKey: pieceFrnt, BomLineKey: bomFabric, FusingBomLineKey: bomFusing, Note: "клеевая по припуску"},
			{PieceLineKey: pieceBack, BomLineKey: bomFabric},
		}))

	got := tcicReadMapping(ctx, t, blk)
	require.Len(t, got, 2)
	require.Equal(t, pieceFrnt, got[0].PieceLineKey)
	require.Equal(t, bomFabric, got[0].BomLineKey.String, "the fabric must resolve to THIS card's line")
	require.Equal(t, bomFusing, got[0].FusingLineKey.String, "the клеевая is a second, independent reference")
	require.Equal(t, "клеевая по припуску", got[0].Note.String)
	require.Equal(t, pieceBack, got[1].PieceLineKey)
	require.False(t, got[1].FusingLineKey.Valid, "an unfused piece names no клеевая, and NULL is what says so")

	// ── 2. re-running REPLACES, and only for this colour ────────────────────────────
	// The neighbour is written first so the scoping has something to get wrong.
	require.NoError(t, T.ApplyImportedColorwayPieceMaterials(ctx, tcID, olv,
		[]entity.TechCardArchivePieceMaterial{{PieceLineKey: pieceFrnt, BomLineKey: bomFabric}}))

	require.NoError(t, T.ApplyImportedColorwayPieceMaterials(ctx, tcID, blk,
		[]entity.TechCardArchivePieceMaterial{{PieceLineKey: pieceFrnt, BomLineKey: bomFusing}}))

	again := tcicReadMapping(ctx, t, blk)
	require.Len(t, again, 1, "a second application replaces this colour's mapping; UNIQUE(piece, colourway) "+
		"would otherwise turn the repeat into a duplicate-key error")
	require.Equal(t, bomFusing, again[0].BomLineKey.String)

	neighbour := tcicReadMapping(ctx, t, olv)
	require.Len(t, neighbour, 1, "the OTHER colour's mapping must survive: the card save's clear wipes "+
		"every colour, and reusing it here would have deleted a colourway nobody asked about")
	require.Equal(t, bomFabric, neighbour[0].BomLineKey.String)

	// ── 3. an empty list is a CLEAR, not a no-op ────────────────────────────────────
	require.NoError(t, T.ApplyImportedColorwayPieceMaterials(ctx, tcID, blk, nil))
	require.Empty(t, tcicReadMapping(ctx, t, blk))
	require.Len(t, tcicReadMapping(ctx, t, olv), 1, "and it is still scoped to one colour")

	// ── 4. the refusals ─────────────────────────────────────────────────────────────
	t.Run("a key that names nothing is an error, not a dropped row", func(t *testing.T) {
		err := T.ApplyImportedColorwayPieceMaterials(ctx, tcID, olv,
			[]entity.TechCardArchivePieceMaterial{{PieceLineKey: "TCIC-PIECE-GONE", BomLineKey: bomFabric}})
		require.Error(t, err, "the caller promised the key exists; writing fewer rows than it was "+
			"handed would make that promise unfalsifiable")
		require.Contains(t, err.Error(), "TCIC-PIECE-GONE")
	})

	t.Run("a BOM key that names nothing is an error too", func(t *testing.T) {
		err := T.ApplyImportedColorwayPieceMaterials(ctx, tcID, olv,
			[]entity.TechCardArchivePieceMaterial{{PieceLineKey: pieceFrnt, BomLineKey: "TCIC-BOM-GONE"}})
		require.Error(t, err)
		require.Contains(t, err.Error(), "TCIC-BOM-GONE")
	})

	t.Run("a colourway of another card is refused", func(t *testing.T) {
		other, err := T.AddTechCard(ctx, &entity.TechCardInsert{
			Name: "Import Colourways Other", Stage: entity.TechCardStageProto,
			StyleNumber:     ns(fmt.Sprintf("TCIC-OTHER-%d", time.Now().UnixNano())),
			MeasurementUnit: entity.TechCardUnitMm, ApprovalState: entity.TechCardApprovalDraft,
		})
		require.NoError(t, err)
		t.Cleanup(func() { _, _ = testDB.ExecContext(context.Background(), "DELETE FROM tech_card WHERE id = ?", other) })
		stranger := tcicColourway(ctx, t, other, "NAV")

		err = T.ApplyImportedColorwayPieceMaterials(ctx, tcID, stranger,
			[]entity.TechCardArchivePieceMaterial{{PieceLineKey: pieceFrnt, BomLineKey: bomFabric}})
		require.Error(t, err, "UNIQUE(piece_id, colorway_id) would happily hold a stranger, and the FK "+
			"is to product(id), which knows nothing about tech_card_id")
	})

	// A failed refusal must not have written anything on its way to failing.
	require.Empty(t, tcicReadMapping(ctx, t, blk))
	require.Len(t, tcicReadMapping(ctx, t, olv), 1)

	// ── 5. the report stamp ─────────────────────────────────────────────────────────
	t.Run("the report stamp rewrites only a committed import", func(t *testing.T) {
		committed := fmt.Sprintf("TCICCOMMITTED%012d", time.Now().UnixNano()%1e12)
		uploaded := fmt.Sprintf("TCICUPLOADED0%012d", time.Now().UnixNano()%1e12)
		for _, row := range []struct{ id, status string }{
			{committed, entity.TechCardImportStatusCommitted},
			{uploaded, entity.TechCardImportStatusUploaded},
		} {
			_, err := testDB.ExecContext(ctx, `INSERT INTO tech_card_import
				(import_id, tech_card_id, object_key, archive_manifest, status, imported_by, report)
				VALUES (?, ?, 'techcard-imports/x.zip', '{}', ?, 'tester', '{"lines":[]}')`,
				row.id, tcID, row.status)
			require.NoError(t, err)
			t.Cleanup(func() {
				_, _ = testDB.ExecContext(context.Background(), "DELETE FROM tech_card_import WHERE import_id = ?", row.id)
			})
		}

		fresh, err := techcardarchive.MarshalReport(techcardarchive.BuildReport(techcardarchive.ReportInput{
			ImportID: committed, StyleNumber: "TCIC", Stage: "proto",
			Counters: techcardarchive.NewCounters(),
		}))
		require.NoError(t, err)

		require.NoError(t, T.StampTechCardImportReport(ctx, committed, fresh))
		var stored string
		require.NoError(t, testDB.QueryRowContext(ctx,
			`SELECT report FROM tech_card_import WHERE import_id = ?`, committed).Scan(&stored))
		require.JSONEq(t, string(fresh), stored)

		// The uploaded row has no card and no report worth revising. Not an error — the caller read
		// its row first — but not a write either.
		require.NoError(t, T.StampTechCardImportReport(ctx, uploaded, fresh))
		require.NoError(t, testDB.QueryRowContext(ctx,
			`SELECT report FROM tech_card_import WHERE import_id = ?`, uploaded).Scan(&stored))
		require.JSONEq(t, `{"lines":[]}`, stored, "an import that never committed must not be rewritten")

		require.Error(t, T.StampTechCardImportReport(ctx, committed, []byte("not json")),
			"the column is JSON and a bare MySQL 3140 names neither the import nor the caller")
	})

	// ── 6. the clear is scoped by the CARD as well as by the colour (R6/N-9) ────────
	//
	// The clear says `colorway_id = :colorway AND piece_id IN (SELECT … WHERE tech_card_id = :card)`,
	// and only the first half had a guard: dropping the card scope left every case above green,
	// because in all of them the colourway's rows and the card's pieces are the same set.
	//
	// The half that was untested is what the write PROMISES: it touches rows about THIS card's
	// pieces and nothing else. tech_card_piece_material's own UNIQUE is (piece_id, colorway_id) and
	// its FKs are to tech_card_piece(id) and product(id) — neither knows about tech_card_id — so a
	// row pairing this colour with another card's piece is storable, and an unscoped clear would
	// delete it while claiming to have re-applied one card's mapping.
	t.Run("the clear touches only rows about THIS card's pieces", func(t *testing.T) {
		const foreignPieceKey = "TCIC-PIECE-FOREIGN"
		foreign, err := T.AddTechCard(ctx, &entity.TechCardInsert{
			Name: "Import Colourways Foreign Pieces", Stage: entity.TechCardStageProto,
			StyleNumber:     ns(fmt.Sprintf("TCIC-FOREIGN-%d", time.Now().UnixNano())),
			MeasurementUnit: entity.TechCardUnitMm, ApprovalState: entity.TechCardApprovalDraft,
			Pieces: []entity.TechCardPiece{
				{LineKey: foreignPieceKey, Name: "чужая деталь", PiecesPerGarment: 1, Grainline: "lengthwise"},
			},
		})
		require.NoError(t, err)
		t.Cleanup(func() { _, _ = testDB.ExecContext(context.Background(), "DELETE FROM tech_card WHERE id = ?", foreign) })

		var foreignPieceID int
		require.NoError(t, testDB.QueryRowContext(ctx,
			`SELECT id FROM tech_card_piece WHERE tech_card_id = ? AND line_key = ?`,
			foreign, foreignPieceKey).Scan(&foreignPieceID))

		// A row that must not exist in production and is storable anyway: our colour, their piece.
		_, err = testDB.ExecContext(ctx, `INSERT INTO tech_card_piece_material
			(piece_id, colorway_id, display_order) VALUES (?, ?, 99)`, foreignPieceID, blk)
		require.NoError(t, err)
		t.Cleanup(func() {
			_, _ = testDB.ExecContext(context.Background(),
				"DELETE FROM tech_card_piece_material WHERE piece_id = ?", foreignPieceID)
		})

		require.NoError(t, T.ApplyImportedColorwayPieceMaterials(ctx, tcID, blk,
			[]entity.TechCardArchivePieceMaterial{{PieceLineKey: pieceFrnt, BomLineKey: bomFabric}}))

		var survived int
		require.NoError(t, testDB.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM tech_card_piece_material WHERE colorway_id = ? AND piece_id = ?`,
			blk, foreignPieceID).Scan(&survived))
		require.Equal(t, 1, survived,
			"the clear is scoped to the pieces of the card it was given; a row about another card's "+
				"piece is not this write's to delete")

		var mine int
		require.NoError(t, testDB.QueryRowContext(ctx, `
			SELECT COUNT(*) FROM tech_card_piece_material pm
			JOIN tech_card_piece p ON p.id = pm.piece_id
			WHERE pm.colorway_id = ? AND p.tech_card_id = ?`, blk, tcID).Scan(&mine))
		require.Equal(t, 1, mine, "and it did re-apply this card's own mapping")
	})
}
