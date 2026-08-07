package store

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/dto"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"
)

// markerLayoutFacts derives the save-path facts from a layout blob exactly as the API layer does
// (dto.MarkerLayoutFactsFromPb), so a fixture's blob and its facts cannot describe two different
// раскладки. Ф1 made those facts part of what the store judges, and an insert built by hand without
// them is refused on purpose: the zero value is the one that would exempt a layout from the
// directional-cloth policy, so it must not be reachable by forgetting a line of wiring.
// markerLayoutV1 is the empty legacy blob the CRUD fixtures ride on; the direction rules have their
// own file and their own blobs.
const markerLayoutV1 = `{"schemaVersion":1,"pieces":[],"placements":[]}`

func markerLayoutFacts(t *testing.T, blob string) entity.MarkerLayoutFacts {
	t.Helper()
	var l pb_common.TechCardMarkerLayout
	require.NoError(t, (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal([]byte(blob), &l))
	facts, err := dto.MarkerLayoutFactsFromPb(&l)
	require.NoError(t, err)
	return facts
}

// TestTechCardMarkerRoundTrip covers the saved-раскладка CRUD (0257): create with a BOM link,
// summary on the card read, blob on GetMarker, in-place update, the refusals that guard the data
// (incomplete layout, foreign size, unknown BOM key, duplicate name, released card, foreign id),
// and the deliberate ON DELETE SET NULL degradation when the linked BOM slot is removed.
func TestTechCardMarkerRoundTrip(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
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
	card := func(items ...entity.TechCardBomItem) *entity.TechCardInsert {
		return &entity.TechCardInsert{
			Name: "Marker Style", Stage: entity.TechCardStageProto, StyleNumber: ns("MRK-1"),
			MeasurementUnit: entity.TechCardUnitMm, ApprovalState: entity.TechCardApprovalDraft,
			SizeIds:  []int{szA},
			BomItems: items,
		}
	}
	// направление ткани is part of the fixture since Ф1.5: a marker bound to a cloth line whose
	// direction nobody set is refused, so a line without one no longer exercises the CRUD at all.
	// 'any' keeps this test about the CRUD — the direction rules have their own file.
	fabric := entity.TechCardBomItem{
		LineKey: "01MRKFABRIC0000000000000K1", Section: entity.BomSectionFabric, Name: "Основная",
		FabricDirection: ns("any"),
	}

	tcID, err := T.AddTechCard(ctx, card(fabric))
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = testDB.ExecContext(context.Background(), "DELETE FROM tech_card WHERE id = ?", tcID)
	})

	d := func(v string) decimal.Decimal { return decimal.RequireFromString(v) }
	ins := func() entity.TechCardMarkerInsert {
		return entity.TechCardMarkerInsert{
			SizeId: szA, Name: "M · основная", Source: entity.MarkerSourceAuto,
			BomLineKey:    fabric.LineKey,
			FabricWidthCm: d("140"), GapCm: d("0.5"), EdgeMarginCm: d("1"),
			Sets: 4, UsedLengthCm: d("512.4"),
			EfficiencyPct: decimal.NullDecimal{Decimal: d("73.5"), Valid: true},
			PlacedCount:   12, TotalCount: 12,
			Layout: markerLayoutV1, LayoutFacts: markerLayoutFacts(t, markerLayoutV1),
		}
	}

	// Create, then read the summary off the card and the blob off GetMarker.
	id, err := T.SaveMarker(ctx, tcID, 0, ins(), "tester")
	require.NoError(t, err)
	require.Positive(t, id)

	c1, err := T.GetTechCardById(ctx, tcID)
	require.NoError(t, err)
	require.Len(t, c1.Markers, 1)
	sum := c1.Markers[0]
	require.Equal(t, "M · основная", sum.Name)
	require.Equal(t, fabric.LineKey, sum.BomLineKey.String)
	require.Equal(t, "Основная", sum.BomItemName.String)
	require.Equal(t, "128.1", sum.ConsumptionPerUnitCm().String())
	require.Equal(t, "tester", sum.CreatedBy)

	full, err := T.GetMarker(ctx, id)
	require.NoError(t, err)
	require.JSONEq(t, `{"schemaVersion":1,"pieces":[],"placements":[]}`, full.Layout)

	// In-place replace: rename, manual provenance, same id.
	upd := ins()
	upd.Name = "M · основная v2"
	upd.Source = entity.MarkerSourceManual
	upd.UsedLengthCm = d("500")
	id2, err := T.SaveMarker(ctx, tcID, id, upd, "editor")
	require.NoError(t, err)
	require.Equal(t, id, id2)
	full2, err := T.GetMarker(ctx, id)
	require.NoError(t, err)
	require.Equal(t, "M · основная v2", full2.Name)
	require.Equal(t, string(entity.MarkerSourceManual), full2.Source)
	require.Equal(t, "editor", full2.UpdatedBy)
	require.Equal(t, "tester", full2.CreatedBy)

	// Refusals.
	t.Run("incomplete layout", func(t *testing.T) {
		bad := ins()
		bad.Name = "неполная"
		bad.PlacedCount = 11
		_, err := T.SaveMarker(ctx, tcID, 0, bad, "tester")
		require.ErrorIs(t, err, entity.ErrMarkerIncomplete)
	})
	t.Run("size off the card", func(t *testing.T) {
		bad := ins()
		bad.Name = "чужой размер"
		bad.SizeId = szB
		var ve *entity.ValidationError
		_, err := T.SaveMarker(ctx, tcID, 0, bad, "tester")
		require.ErrorAs(t, err, &ve)
	})
	t.Run("unknown bom line", func(t *testing.T) {
		bad := ins()
		bad.Name = "мимо BOM"
		bad.BomLineKey = "01NOSUCHLINE0000000000000K"
		var ve *entity.ValidationError
		_, err := T.SaveMarker(ctx, tcID, 0, bad, "tester")
		require.ErrorAs(t, err, &ve)
	})
	t.Run("duplicate name on the size", func(t *testing.T) {
		dup := ins()
		dup.Name = "M · основная v2"
		_, err := T.SaveMarker(ctx, tcID, 0, dup, "tester")
		require.Error(t, err)
		require.True(t, s.IsErrUniqueViolation(err), "want uniq_tcm_card_size_name, got %v", err)
	})
	t.Run("foreign marker id", func(t *testing.T) {
		_, err := T.SaveMarker(ctx, tcID+1000000, id, ins(), "tester")
		// The card itself does not exist -> the mutable-card guard reports no rows.
		require.Error(t, err)
	})
	t.Run("cross-card marker id is not adopted", func(t *testing.T) {
		// The REAL IDOR case: a marker id of card A addressed through an EXISTING card B must
		// be refused, not silently rebound to B.
		other := card()
		other.StyleNumber = sql.NullString{String: "MRK-2", Valid: true}
		otherID, err := T.AddTechCard(ctx, other)
		require.NoError(t, err)
		t.Cleanup(func() { _ = T.DeleteTechCard(ctx, otherID) })
		_, err = T.SaveMarker(ctx, otherID, id, ins(), "tester")
		require.ErrorIs(t, err, entity.ErrMarkerNotFound)
		_, err = T.GetMarker(ctx, id)
		require.NoError(t, err, "the original marker must be untouched")
	})
	t.Run("byte-identical re-save is not a phantom 404", func(t *testing.T) {
		// RowsAffected counts rows CHANGED, and this UPDATE has no guaranteed-changing column —
		// ownership must resolve via SELECT, or a no-op re-save reads as NotFound.
		cur, err := T.GetMarker(ctx, id)
		require.NoError(t, err)
		again := entity.TechCardMarkerInsert{
			SizeId:          cur.SizeId,
			Name:            cur.Name,
			Source:          entity.MarkerSource(cur.Source),
			BomLineKey:      cur.BomLineKey.String,
			FabricWidthCm:   cur.FabricWidthCm,
			GapCm:           cur.GapCm,
			EdgeMarginCm:    cur.EdgeMarginCm,
			AllowCrossGrain: cur.AllowCrossGrain,
			Sets:            cur.Sets,
			UsedLengthCm:    cur.UsedLengthCm,
			EfficiencyPct:   cur.EfficiencyPct,
			PlacedCount:     cur.PlacedCount,
			TotalCount:      cur.TotalCount,
			Layout:          cur.Layout,
			LayoutFacts:     markerLayoutFacts(t, cur.Layout),
		}
		saved, err := T.SaveMarker(ctx, tcID, id, again, cur.UpdatedBy)
		require.NoError(t, err)
		require.Equal(t, id, saved)
	})

	// The BOM slot is deleted out from under the marker: the link degrades to NULL (SET NULL by
	// design — a RESTRICT would fail the whole card save), the marker survives as geometry.
	c2, err := T.GetTechCardById(ctx, tcID)
	require.NoError(t, err)
	require.NoError(t, T.UpdateTechCard(ctx, tcID, card(), c2.LockVersion))
	orphan, err := T.GetMarker(ctx, id)
	require.NoError(t, err)
	require.False(t, orphan.BomItemId.Valid, "bom_item_id must go NULL with its slot")
	require.False(t, orphan.BomLineKey.Valid)

	// A released card freezes markers like every other card content.
	_, err = testDB.ExecContext(ctx, "UPDATE tech_card SET approval_state = 'released' WHERE id = ?", tcID)
	require.NoError(t, err)
	_, err = T.SaveMarker(ctx, tcID, id, upd, "tester")
	require.ErrorIs(t, err, entity.ErrTechCardReleased)
	require.ErrorIs(t, T.DeleteMarker(ctx, id), entity.ErrTechCardReleased)
	_, err = testDB.ExecContext(ctx, "UPDATE tech_card SET approval_state = 'draft' WHERE id = ?", tcID)
	require.NoError(t, err)

	// Delete, then the id answers NotFound everywhere.
	require.NoError(t, T.DeleteMarker(ctx, id))
	_, err = T.GetMarker(ctx, id)
	require.ErrorIs(t, err, entity.ErrMarkerNotFound)
	require.ErrorIs(t, T.DeleteMarker(ctx, id), entity.ErrMarkerNotFound)
}
