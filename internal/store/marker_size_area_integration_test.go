package store

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

// РАСХОД ПО ПЛОЩАДИ end to end (Ф2.4, migration 0278). The arithmetic is proved in entity; what only
// a database can witness is that the BASIS survives the round trip and that the column refuses the
// values the derivation is not allowed to store.
//
//	(a) a mixed раскладка saved with per-size areas comes back with them, on the card read AND on
//	    GetMarker, and its per-size расход converges on used_length_cm — the acceptance criterion of
//	    the phase, asserted against numbers that went through MySQL;
//	(b) a раскладка saved WITHOUT areas (every marker taken before Ф2.4) reads back with NULL and
//	    hands out no per-size figure — never the mean;
//	(c) re-saving replaces the areas along with the quantities, so a состав whose sizes moved cannot
//	    keep distributing a length by the geometry of a раскладка it no longer is;
//	(d) chk_tcms_area_pos refuses a zero/negative area, so «измерено как ноль» cannot be stored and
//	    then silently shrink the denominator of every other size.
func TestTechCardMarkerSizeAreas(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	cfg := *testCfg
	cfg.Automigrate = true
	s, err := NewForTest(ctx, cfg)
	require.NoError(t, err)
	defer s.Close()
	T := s.TechCards()

	var szA, szB int
	require.NoError(t, testDB.QueryRowContext(ctx, "SELECT MIN(id) FROM size").Scan(&szA))
	require.NoError(t, testDB.QueryRowContext(ctx, "SELECT MIN(id) FROM size WHERE id > ?", szA).Scan(&szB))

	ns := func(v string) sql.NullString { return sql.NullString{String: v, Valid: true} }
	d := func(v string) decimal.Decimal { return decimal.RequireFromString(v) }
	nd := func(v string) decimal.NullDecimal {
		return decimal.NullDecimal{Decimal: d(v), Valid: true}
	}
	fabric := entity.TechCardBomItem{
		LineKey: "01MRKAREA00000000000000K1", Section: entity.BomSectionFabric, Name: "Основная",
		FabricDirection: ns("any"),
	}
	tcID, err := T.AddTechCard(ctx, &entity.TechCardInsert{
		Name: "Area Style", Stage: entity.TechCardStageProto, StyleNumber: ns("MAREA-1"),
		MeasurementUnit: entity.TechCardUnitMm, ApprovalState: entity.TechCardApprovalDraft,
		SizeIds:  []int{szA, szB},
		BomItems: []entity.TechCardBomItem{fabric},
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = testDB.ExecContext(context.Background(), "DELETE FROM tech_card WHERE id = ?", tcID)
	})

	base := func(name string) entity.TechCardMarkerInsert {
		return entity.TechCardMarkerInsert{
			Name: name, Source: entity.MarkerSourceAuto, BomLineKey: fabric.LineKey,
			FabricWidthCm: d("140"), GapCm: d("0.5"), EdgeMarginCm: d("1"),
			UsedLengthCm: d("1400"), PlacedCount: 8, TotalCount: 8,
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

	// (a) 3 × A and 2 × B off one 1400 cm spread; a_A = 5200 cm², a_B = 6200 cm² ⇒ A = 28000 cm².
	mixed := base("смешанная")
	markerMixedSizing(&mixed,
		entity.MarkerCompositionEntry{SizeId: szA, Quantity: 3, AreaPerGarmentCm2: nd("5200")},
		entity.MarkerCompositionEntry{SizeId: szB, Quantity: 2, AreaPerGarmentCm2: nd("6200")})
	mixedID, err := T.SaveMarker(ctx, tcID, 0, mixed, "tester")
	require.NoError(t, err)

	got := markerByName("смешанная")
	require.Len(t, got.Composition, 2)
	require.True(t, got.Composition[0].AreaPerGarmentCm2.Valid, "the basis must survive the round trip")
	require.Equal(t, "5200", got.Composition[0].AreaPerGarmentCm2.Decimal.String())
	require.Equal(t, "6200", got.Composition[1].AreaPerGarmentCm2.Decimal.String())

	// СХОДИМОСТЬ на цифрах, прошедших через MySQL: Σ(quantity × расход) = used_length_cm.
	rows := got.PerSizeConsumption()
	require.True(t, entity.MarkerPerSizeConsumptionComplete(rows))
	require.Equal(t, "260", rows[0].ConsumptionCm.Decimal.String())
	require.Equal(t, "310", rows[1].ConsumptionCm.Decimal.String())
	sum := decimal.Zero
	for _, r := range rows {
		sum = sum.Add(r.ConsumptionCm.Decimal.Mul(decimal.NewFromInt(int64(r.Quantity))))
	}
	require.Equal(t, got.UsedLengthCm.String(), sum.String(),
		"the distributed lengths must add back up to the length that was measured")

	// …и скалярный отказ на месте: пер-размерная норма — не безразмерная.
	require.NotEmpty(t, got.ScalarNormRefusal())
	require.Contains(t, got.ScalarNormRefusal(), "ПО РАЗМЕРАМ")

	// The blob read carries the same basis — a per-size норма that differed between the card read and
	// GetMarker would be a field whose value depends on which RPC you asked.
	full, err := T.GetMarker(ctx, mixedID)
	require.NoError(t, err)
	require.Equal(t, "5200", full.Composition[0].AreaPerGarmentCm2.Decimal.String())

	// (b) A раскладка taken before Ф2.4: состав, no areas. NULL comes back as «не записано», and the
	// per-size norm is withheld rather than replaced by used_length/total_units.
	old := base("до Ф2.4")
	markerMixedSizing(&old,
		entity.MarkerCompositionEntry{SizeId: szA, Quantity: 3},
		entity.MarkerCompositionEntry{SizeId: szB, Quantity: 2})
	_, err = T.SaveMarker(ctx, tcID, 0, old, "tester")
	require.NoError(t, err)
	stale := markerByName("до Ф2.4")
	for _, c := range stale.Composition {
		require.False(t, c.AreaPerGarmentCm2.Valid, "size %d must read as «площадь не записана»", c.SizeId)
	}
	for _, r := range stale.PerSizeConsumption() {
		require.False(t, r.ConsumptionCm.Valid, "the mean must not stand in for a missing basis")
	}
	require.Contains(t, stale.ScalarNormRefusal(), "Пересохраните")

	// (c) Full replace covers the areas too: the состав shrinks and re-grades, and the row does not
	// keep distributing by the geometry of the раскладка it used to be.
	reshot := base("смешанная")
	markerMixedSizing(&reshot,
		entity.MarkerCompositionEntry{SizeId: szA, Quantity: 4, AreaPerGarmentCm2: nd("5100.25")})
	_, err = T.SaveMarker(ctx, tcID, mixedID, reshot, "editor")
	require.NoError(t, err)
	after := markerByName("смешанная")
	require.Len(t, after.Composition, 1)
	require.Equal(t, "5100.25", after.Composition[0].AreaPerGarmentCm2.Decimal.String())
	require.Empty(t, after.ScalarNormRefusal(), "one size means the scalar is honest again")

	// (d) The column refuses a non-positive area outright. Zero is the dangerous one: it looks like a
	// measurement, and it shrinks the denominator every OTHER size is divided by.
	for _, bad := range []string{"0", "-1"} {
		_, err = testDB.ExecContext(ctx,
			`UPDATE tech_card_marker_size SET area_per_garment_cm2 = ? WHERE marker_id = ?`, bad, mixedID)
		require.Error(t, err, "area %s must be refused by chk_tcms_area_pos", bad)
	}
}
