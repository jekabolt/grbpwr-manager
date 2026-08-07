package store

import (
	"context"
	"database/sql"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

// СОСТАВ РАСКЛАДКИ end to end (Ф2, migration 0273): a marker cuts a MAP of size → garments instead
// of one size N times. What this covers, and why each part is here rather than trusted:
//
//	(a) a mixed раскладка round-trips: size_id/sets go NULL, the состав comes back off the child
//	    table, and total_units equals Σ quantity — the invariant the divisor of money rests on;
//	(b) the child rows are FULLY REPLACED on re-save and CASCADE on delete;
//	(c) a состав naming a size off the card is refused, and the refusal lists ALL of them;
//	(d) two markers with a состав and the same name collide — the uniqueness that NULL in size_id
//	    silently switched off and size_key switched back on;
//	(e) a LEGACY row with no children still reads as a состав из одного размера with quantity = sets
//	    (the deploy-overlap shape, and the shape every row had before 0273), and 0273's own backfill
//	    statements — read out of the migration file, not retyped — turn it into real child rows;
//	(f) the mixed раскладка withholds its scalar per-garment norm (Р2).
func TestTechCardMarkerComposition(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	cfg := *testCfg
	cfg.Automigrate = true
	s, err := NewForTest(ctx, cfg)
	require.NoError(t, err)
	defer s.Close()
	T := s.TechCards()

	var szA, szB, szC, szOff int
	require.NoError(t, testDB.QueryRowContext(ctx, "SELECT MIN(id) FROM size").Scan(&szA))
	require.NoError(t, testDB.QueryRowContext(ctx, "SELECT MIN(id) FROM size WHERE id > ?", szA).Scan(&szB))
	require.NoError(t, testDB.QueryRowContext(ctx, "SELECT MIN(id) FROM size WHERE id > ?", szB).Scan(&szC))
	require.NoError(t, testDB.QueryRowContext(ctx, "SELECT MIN(id) FROM size WHERE id > ?", szC).Scan(&szOff))

	ns := func(v string) sql.NullString { return sql.NullString{String: v, Valid: true} }
	fabric := entity.TechCardBomItem{
		LineKey: "01MRKCOMP00000000000000K1", Section: entity.BomSectionFabric, Name: "Основная",
		FabricDirection: ns("any"),
	}
	tcID, err := T.AddTechCard(ctx, &entity.TechCardInsert{
		Name: "Composition Style", Stage: entity.TechCardStageProto, StyleNumber: ns("MCOMP-1"),
		MeasurementUnit: entity.TechCardUnitMm, ApprovalState: entity.TechCardApprovalDraft,
		SizeIds:  []int{szA, szB, szC},
		BomItems: []entity.TechCardBomItem{fabric},
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = testDB.ExecContext(context.Background(), "DELETE FROM tech_card WHERE id = ?", tcID)
	})

	d := func(v string) decimal.Decimal { return decimal.RequireFromString(v) }
	base := func(name string) entity.TechCardMarkerInsert {
		return entity.TechCardMarkerInsert{
			Name: name, Source: entity.MarkerSourceAuto, BomLineKey: fabric.LineKey,
			FabricWidthCm: d("140"), GapCm: d("0.5"), EdgeMarginCm: d("1"),
			UsedLengthCm: d("900"),
			PlacedCount:  8, TotalCount: 8,
			Layout: markerLayoutV1, LayoutFacts: markerLayoutFacts(t, markerLayoutV1),
		}
	}
	markerByName := func(name string) entity.TechCardMarkerSummary {
		c, err := T.GetTechCardById(ctx, tcID)
		require.NoError(t, err)
		for _, m := range c.Markers {
			if m.Name == name {
				return m
			}
		}
		t.Fatalf("no marker %q on card %d", name, tcID)
		return entity.TechCardMarkerSummary{}
	}

	// (a) A mixed раскладка: S×1 M×2 L×1 = four garments off one 900 cm spread.
	mixed := base("смешанная")
	markerMixedSizing(&mixed, entity.MarkerCompositionEntry{SizeId: szA, Quantity: 1},
		entity.MarkerCompositionEntry{SizeId: szB, Quantity: 2},
		entity.MarkerCompositionEntry{SizeId: szC, Quantity: 1})
	mixedID, err := T.SaveMarker(ctx, tcID, 0, mixed, "tester")
	require.NoError(t, err)

	got := markerByName("смешанная")
	require.False(t, got.SizeId.Valid, "a marker with a состав stores NULL, never a representative size")
	require.False(t, got.Sets.Valid)
	require.Equal(t, []entity.MarkerCompositionEntry{
		{SizeId: szA, Quantity: 1}, {SizeId: szB, Quantity: 2}, {SizeId: szC, Quantity: 1},
	}, got.Composition, "the состав rides the summary, ordered by size_id")
	require.True(t, got.TotalUnits.Valid)
	require.Equal(t, int64(4), got.TotalUnits.Int64)
	require.Equal(t, 4, got.TotalUnitsOrLegacy())
	// THE INVARIANT THE DIVISOR OF MONEY RESTS ON: the stored scalar and its own children agree.
	// They are written in one SERIALIZABLE transaction from one ins.Composition precisely so that a
	// lost child row cannot shrink the divisor and silently raise the recorded cost.
	var sum int
	require.NoError(t, testDB.QueryRowContext(ctx,
		"SELECT COALESCE(SUM(quantity),0) FROM tech_card_marker_size WHERE marker_id = ?", mixedID).Scan(&sum))
	require.Equal(t, 4, sum)
	require.Equal(t, "225", got.ConsumptionPerUnitCm().String())

	// (f) …and that mean is nevertheless refused as a recipe norm.
	require.NotEmpty(t, got.ScalarNormRefusal())
	require.Contains(t, got.ScalarNormRefusal(), "Ф2.4")

	// The blob still travels alone on GetMarker, and it carries the состав too.
	full, err := T.GetMarker(ctx, mixedID)
	require.NoError(t, err)
	require.Len(t, full.Composition, 3)
	require.Equal(t, 4, full.TotalUnitsOrLegacy())

	// (b) Full replace: the состав shrinks to two sizes and the dropped child is GONE, not orphaned.
	shrunk := base("смешанная")
	markerMixedSizing(&shrunk, entity.MarkerCompositionEntry{SizeId: szA, Quantity: 3},
		entity.MarkerCompositionEntry{SizeId: szC, Quantity: 1})
	_, err = T.SaveMarker(ctx, tcID, mixedID, shrunk, "editor")
	require.NoError(t, err)
	got = markerByName("смешанная")
	require.Equal(t, []entity.MarkerCompositionEntry{{SizeId: szA, Quantity: 3}, {SizeId: szC, Quantity: 1}},
		got.Composition)
	require.Equal(t, int64(4), got.TotalUnits.Int64)

	// A single-size состав behaves exactly as a legacy marker did — including handing out its scalar.
	single := base("однородная")
	single.UsedLengthCm = d("512.4")
	markerMixedSizing(&single, entity.MarkerCompositionEntry{SizeId: szB, Quantity: 4})
	singleID, err := T.SaveMarker(ctx, tcID, 0, single, "tester")
	require.NoError(t, err)
	one := markerByName("однородная")
	require.Equal(t, "128.1", one.ConsumptionPerUnitCm().String())
	require.Empty(t, one.ScalarNormRefusal(), "one size means the scalar is honest")

	// The list is newest-first now that grouping by size stopped meaning anything (and would have
	// silently reordered itself on NULL size_id).
	card, err := T.GetTechCardById(ctx, tcID)
	require.NoError(t, err)
	require.Equal(t, "однородная", card.Markers[0].Name)

	// (c) A size the card does not carry is refused, and ALL offenders are named — the operator's
	// fix is a table of rows, so one refusal per round trip would make a two-row mistake two saves.
	bad := base("чужие размеры")
	markerMixedSizing(&bad, entity.MarkerCompositionEntry{SizeId: szA, Quantity: 1},
		entity.MarkerCompositionEntry{SizeId: szOff, Quantity: 1})
	var ve *entity.ValidationError
	_, err = T.SaveMarker(ctx, tcID, 0, bad, "tester")
	require.ErrorAs(t, err, &ve)
	require.Contains(t, err.Error(), entity.ReasonCompositionNotOnCard)

	// (d) The name collision NULL turned off. Without size_key both rows would be accepted, and the
	// «a раскладка with this name already exists» message would be a lie.
	dup := base("смешанная")
	markerMixedSizing(&dup, entity.MarkerCompositionEntry{SizeId: szA, Quantity: 1})
	_, err = T.SaveMarker(ctx, tcID, 0, dup, "tester")
	require.Error(t, err)
	require.True(t, s.IsErrUniqueViolation(err), "want uniq_tcm_card_sizekey_name, got %v", err)

	// Children CASCADE with their marker; nothing is left pointing at a gone раскладка.
	require.NoError(t, T.DeleteMarker(ctx, singleID))
	var orphans int
	require.NoError(t, testDB.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM tech_card_marker_size WHERE marker_id = ?", singleID).Scan(&orphans))
	require.Zero(t, orphans)

	// (e) THE LEGACY ROW. Written straight into the table in the pre-Ф2 shape — size_id + sets, no
	// total_units, no children. This is BOTH what every row looked like before 0273 and what the old
	// container can still write during a deploy overlap, which is the one hole the backfill cannot
	// close. It must read as a состав из одного размера either way.
	res, err := testDB.ExecContext(ctx, `
		INSERT INTO tech_card_marker
			(tech_card_id, size_id, name, source, fabric_width_cm, gap_cm, edge_margin_cm,
			 allow_cross_grain, sets, used_length_cm, placed_count, total_count, layout, created_by, updated_by)
		VALUES (?, ?, 'легаси', 'auto', 140, 0.5, 1, 0, 3, 360, 6, 6, ?, 'old-binary', 'old-binary')`,
		tcID, szA, markerLayoutV1)
	require.NoError(t, err)
	legacyID64, err := res.LastInsertId()
	require.NoError(t, err)
	legacyID := int(legacyID64)

	legacy := markerByName("легаси")
	require.Empty(t, legacy.Composition, "the fixture deliberately has no child rows yet")
	require.Equal(t, []entity.MarkerCompositionEntry{{SizeId: szA, Quantity: 3}}, legacy.CompositionOrLegacy())
	require.Equal(t, 3, legacy.TotalUnitsOrLegacy())
	require.Equal(t, "120", legacy.ConsumptionPerUnitCm().String())
	require.Empty(t, legacy.ScalarNormRefusal())

	// …and 0273's own backfill — the statements read out of the migration file rather than retyped,
	// so this cannot pass while the migration drifts — turns it into real child rows.
	for _, stmt := range markerCompositionBackfillStatements(t) {
		_, err := testDB.ExecContext(ctx, stmt)
		require.NoError(t, err, "backfill statement failed: %s", stmt)
	}
	backfilled := markerByName("легаси")
	require.Equal(t, []entity.MarkerCompositionEntry{{SizeId: szA, Quantity: 3}}, backfilled.Composition)
	require.True(t, backfilled.TotalUnits.Valid)
	require.Equal(t, int64(3), backfilled.TotalUnits.Int64)
	require.Equal(t, 3, backfilled.TotalUnitsOrLegacy())

	// Re-running the backfill inserts nothing and changes nothing — the file re-runs from the top
	// after a half-applied failure, and both statements are idempotent by construction.
	for _, stmt := range markerCompositionBackfillStatements(t) {
		_, err := testDB.ExecContext(ctx, stmt)
		require.NoError(t, err)
	}
	var children int
	require.NoError(t, testDB.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM tech_card_marker_size WHERE marker_id = ?", legacyID).Scan(&children))
	require.Equal(t, 1, children)
}

// markerCompositionBackfillStatements lifts the two DML steps of 0273 (the legacy projection and the
// total_units recompute) OUT OF THE MIGRATION FILE. Reading them instead of retyping them is the
// point: a copy in a test proves the copy, and this test's whole claim is about what the migration
// does to rows that already exist. Same idiom as marker_source_drift_test.go, which reads 0257.
func markerCompositionBackfillStatements(t *testing.T) []string {
	t.Helper()
	body, err := os.ReadFile("sql/0273_marker_composition.sql")
	require.NoError(t, err)
	up, _, ok := strings.Cut(string(body), "-- +migrate Down")
	require.True(t, ok, "0273 has no Down section")
	var out []string
	for _, stmt := range strings.Split(up, ";") {
		trimmed := strings.TrimSpace(stmt)
		// Only the two plain DML steps; everything else in the file is guarded DDL that this test
		// must not re-run (it is exercised by the migration itself at TestMain).
		if strings.Contains(trimmed, "INSERT INTO tech_card_marker_size") ||
			strings.Contains(trimmed, "SET m.total_units") {
			// Drop the leading comment lines so the driver gets a bare statement.
			var lines []string
			for _, l := range strings.Split(trimmed, "\n") {
				if !strings.HasPrefix(strings.TrimSpace(l), "--") {
					lines = append(lines, l)
				}
			}
			out = append(out, strings.Join(lines, "\n"))
		}
	}
	require.Len(t, out, 2, "expected the INSERT and the UPDATE of 0273's backfill")
	return out
}
