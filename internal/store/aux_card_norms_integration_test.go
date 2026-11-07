package store

import (
	"context"
	"database/sql"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
)

// TestAuxCardNormsRoundTrip covers 0456 through the store: tech_card.aux_made_by and
// tech_card_bom_item.consumption_per_unit reach their columns and come back, an omitted value keeps
// the stored one (the admin saves the whole card on every edit), an explicit empty clears, three
// decimals survive, and a card that stops being auxiliary drops aux_made_by.
func TestAuxCardNormsRoundTrip(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	cfg := *testCfg
	cfg.Automigrate = true
	s, err := NewForTest(ctx, cfg)
	require.NoError(t, err)
	defer s.Close()
	T := s.TechCards()

	const (
		lineRope    = "01J0AXR0PE0000000000000S31"
		lineStopper = "01J0AXST0PPER000000000S341"
	)
	ns := func(v string) sql.NullString { return sql.NullString{String: v, Valid: v != ""} }
	dec := func(v string) decimal.NullDecimal {
		return decimal.NullDecimal{Decimal: decimal.RequireFromString(v), Valid: true}
	}
	bom := func() []entity.TechCardBomItem {
		return []entity.TechCardBomItem{
			{LineKey: lineRope, Section: entity.BomSectionTrim, Name: "rope", Unit: ns("m"), ConsumptionPerUnit: dec("1.1")},
			{LineKey: lineStopper, Section: entity.BomSectionHardware, Name: "stopper", Unit: ns("pcs"), QtyPerGarment: dec("2")},
		}
	}
	mk := func(items []entity.TechCardBomItem) *entity.TechCardInsert {
		return &entity.TechCardInsert{
			Name: "AUX NORMS", StyleNumber: ns("AUXN-1"),
			Stage: entity.TechCardStageProto, ApprovalState: entity.TechCardApprovalDraft,
			MeasurementUnit: entity.TechCardUnitMm,
			Purpose:         entity.TechCardPurposeAuxiliary, AuxSubtype: ns(string(entity.AuxSubtypeBrandLabel)),
			BomItems: items,
		}
	}

	tcID, err := T.AddTechCard(ctx, mk(bom()))
	require.NoError(t, err)
	t.Cleanup(func() { _ = T.DeleteTechCard(context.Background(), tcID) })

	read := func() (*entity.TechCard, map[string]entity.TechCardBomItem) {
		t.Helper()
		tc, err := T.GetTechCardById(ctx, tcID)
		require.NoError(t, err)
		m := make(map[string]entity.TechCardBomItem, len(tc.BomItems))
		for _, b := range tc.BomItems {
			m[b.LineKey] = b
		}
		require.Len(t, m, 2)
		return tc, m
	}
	save := func(ins *entity.TechCardInsert) {
		t.Helper()
		tc, err := T.GetTechCardById(ctx, tcID)
		require.NoError(t, err)
		require.NoError(t, T.UpdateTechCard(ctx, tcID, ins, tc.LockVersion))
	}

	tc, got := read()
	require.Equal(t, ns(entity.AuxMadeByUs), tc.AuxMadeBy, "an aux card created with none is made by us")
	require.Equal(t, "1.1", got[lineRope].ConsumptionPerUnit.Decimal.String())
	require.False(t, got[lineStopper].ConsumptionPerUnit.Valid, "no norm reads empty, not zero")

	// Supplier, then a stale tab that sends neither field while editing a neighbour.
	ins := mk(bom())
	ins.AuxMadeBy = ns(entity.AuxMadeBySupplier)
	save(ins)
	tc, _ = read()
	require.Equal(t, ns(entity.AuxMadeBySupplier), tc.AuxMadeBy)

	stale := mk(bom())
	stale.AuxMadeByOmitted = true
	for i := range stale.BomItems {
		stale.BomItems[i].ConsumptionPerUnit = decimal.NullDecimal{}
		stale.BomItems[i].ConsumptionPerUnitOmitted = true
	}
	stale.BomItems[1].Name = "stopper (cord lock)"
	save(stale)
	tc, got = read()
	require.Equal(t, ns(entity.AuxMadeBySupplier), tc.AuxMadeBy, "omitted keeps the stored value")
	require.Equal(t, "1.1", got[lineRope].ConsumptionPerUnit.Decimal.String(), "omitted keeps the stored value")
	require.Equal(t, "stopper (cord lock)", got[lineStopper].Name)

	// Explicit empty clears; a new value lands; the third decimal survives.
	cleared := mk(bom())
	cleared.AuxMadeBy = ns(entity.AuxMadeByUs)
	cleared.BomItems[0].ConsumptionPerUnit = decimal.NullDecimal{}
	save(cleared)
	tc, got = read()
	require.Equal(t, ns(entity.AuxMadeByUs), tc.AuxMadeBy)
	require.False(t, got[lineRope].ConsumptionPerUnit.Valid, "an explicit empty clears the column")
	precise := mk(bom())
	precise.BomItems[0].ConsumptionPerUnit = dec("0.125")
	save(precise)
	_, got = read()
	require.Equal(t, "0.125", got[lineRope].ConsumptionPerUnit.Decimal.String())

	// A card that stops being auxiliary drops aux_made_by.
	sell := mk(nil)
	sell.Purpose, sell.AuxSubtype = entity.TechCardPurposeSellable, sql.NullString{}
	sell.AuxMadeByOmitted = false
	tc, err = T.GetTechCardById(ctx, tcID)
	require.NoError(t, err)
	require.NoError(t, T.UpdateTechCard(ctx, tcID, sell, tc.LockVersion))
	var stored sql.NullString
	require.NoError(t, testDB.QueryRowContext(ctx, "SELECT aux_made_by FROM tech_card WHERE id = ?", tcID).Scan(&stored))
	require.False(t, stored.Valid, "a sellable card stores NULL")
}

// TestAuxMadeByMigrationIsIdempotentAndBackfills: the boot applied 0456 on a fresh schema; re-running
// its Up body (as a mid-file crash would on the next boot) under ANSI_QUOTES — prod's global sql_mode —
// is a no-op for the schema, and the backfill fills only aux rows that are NULL: 'us' when the card has
// a BOM line with a material, else 'supplier'.
func TestAuxMadeByMigrationIsIdempotentAndBackfills(t *testing.T) {
	if os.Getenv("CI") == "" &&
		!strings.Contains(testCfg.DSN, "127.0.0.1") &&
		!strings.Contains(testCfg.DSN, "localhost") {
		t.Skip("skipping outside CI unless the DSN targets a local container (avoids the configured prod DB)")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	cfg := *testCfg
	cfg.Automigrate = true
	s, err := NewForTest(ctx, cfg)
	require.NoError(t, err)
	defer s.Close()
	T := s.TechCards()
	ns := func(v string) sql.NullString { return sql.NullString{String: v, Valid: true} }

	res, err := testDB.ExecContext(ctx, "INSERT INTO material (name, section, unit) VALUES ('AUXN shell', 'fabric', 'm')")
	require.NoError(t, err)
	matID, err := res.LastInsertId()
	require.NoError(t, err)

	mkCard := func(styleNo string, purpose entity.TechCardPurpose, withMaterial bool) int {
		t.Helper()
		ins := &entity.TechCardInsert{
			Name: styleNo, Stage: entity.TechCardStageProto, StyleNumber: ns(styleNo),
			MeasurementUnit: entity.TechCardUnitMm, ApprovalState: entity.TechCardApprovalDraft,
			Purpose: purpose,
		}
		if purpose == entity.TechCardPurposeAuxiliary {
			ins.AuxSubtype = ns(string(entity.AuxSubtypeBrandLabel))
		}
		if withMaterial {
			ins.BomItems = []entity.TechCardBomItem{{
				Section: entity.BomSectionFabric, Name: "shell", Unit: ns("m"),
				MaterialId: sql.NullInt64{Int64: matID, Valid: true},
			}}
		}
		id, err := T.AddTechCard(ctx, ins)
		require.NoError(t, err)
		t.Cleanup(func() { _ = T.DeleteTechCard(context.Background(), id) })
		return id
	}
	made := mkCard("AUXN-MADE", entity.TechCardPurposeAuxiliary, true)
	bought := mkCard("AUXN-BOUGHT", entity.TechCardPurposeAuxiliary, false)
	kept := mkCard("AUXN-KEPT", entity.TechCardPurposeAuxiliary, true)
	garment := mkCard("AUXN-GARMENT", entity.TechCardPurposeSellable, true)
	t.Cleanup(func() { _, _ = testDB.ExecContext(context.Background(), "DELETE FROM material WHERE id = ?", matID) })

	// The state an older binary leaves: aux cards with no value. One explicit value must survive.
	_, err = testDB.ExecContext(ctx, "UPDATE tech_card SET aux_made_by = NULL WHERE id IN (?, ?)", made, bought)
	require.NoError(t, err)
	_, err = testDB.ExecContext(ctx, "UPDATE tech_card SET aux_made_by = 'supplier' WHERE id = ?", kept)
	require.NoError(t, err)

	conn, err := testDB.Conn(ctx)
	require.NoError(t, err)
	defer func() { _ = conn.Close() }()
	_, err = conn.ExecContext(ctx, "SET SESSION sql_mode = CONCAT(@@SESSION.sql_mode, ',ANSI_QUOTES')")
	require.NoError(t, err)
	up := migrationUpBody(t, "0456_aux_made_by_and_bom_consumption.sql")
	for i := 0; i < 2; i++ {
		_, err = conn.ExecContext(ctx, up)
		require.NoError(t, err, "re-run %d", i+1)
	}

	madeBy := func(id int) sql.NullString {
		t.Helper()
		var v sql.NullString
		require.NoError(t, testDB.QueryRowContext(ctx, "SELECT aux_made_by FROM tech_card WHERE id = ?", id).Scan(&v))
		return v
	}
	require.Equal(t, ns(entity.AuxMadeByUs), madeBy(made), "a BOM line with a material: made by us")
	require.Equal(t, ns(entity.AuxMadeBySupplier), madeBy(bought), "no material anywhere: bought finished")
	require.Equal(t, ns(entity.AuxMadeBySupplier), madeBy(kept), "only NULL rows are backfilled")
	require.False(t, madeBy(garment).Valid, "a sellable card stays NULL")

	var n int
	require.NoError(t, conn.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM information_schema.table_constraints
		WHERE table_schema = DATABASE() AND table_name = 'tech_card' AND constraint_name = 'chk_tech_card_aux_made_by'`).Scan(&n))
	require.Equal(t, 1, n)
	require.NoError(t, conn.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM information_schema.COLUMNS
		WHERE TABLE_SCHEMA = DATABASE()
		  AND ((TABLE_NAME = 'tech_card' AND COLUMN_NAME = 'aux_made_by' AND IS_NULLABLE = 'YES')
		    OR (TABLE_NAME = 'tech_card_bom_item' AND COLUMN_NAME = 'consumption_per_unit' AND IS_NULLABLE = 'YES'
		        AND NUMERIC_PRECISION = 10 AND NUMERIC_SCALE = 3))`).Scan(&n))
	require.Equal(t, 2, n)

	_, err = testDB.ExecContext(ctx, "UPDATE tech_card SET aux_made_by = 'robot' WHERE id = ?", made)
	require.Error(t, err, "the value set is held by chk_tech_card_aux_made_by")
}
