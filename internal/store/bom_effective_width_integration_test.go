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

func ndec(t *testing.T, s string) decimal.NullDecimal {
	t.Helper()
	d, err := decimal.NewFromString(s)
	require.NoError(t, err)
	return decimal.NullDecimal{Decimal: d, Valid: true}
}

// TestBomEffectiveFabricWidth covers the 0259 width enrichment on the single-card BOM read:
// effective width resolves the line's own snapshot first, then the linked article's CTI width
// (a zero CTI width falls through like preferredDecimal), then the flat catalog column; the
// article's selvedge rides along; an unlinked width-less line stays honestly unset.
func TestBomEffectiveFabricWidth(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	cfg := *testCfg
	cfg.Automigrate = true
	s, err := NewForTest(ctx, cfg)
	require.NoError(t, err)
	defer s.Close()

	// A fabric article with CTI width 150 and selvedge 1.5 per edge.
	matID, err := s.TechCards().CreateMaterial(ctx, &entity.MaterialInsert{
		Name:          "тест-ткань-0259",
		Section:       string(entity.BomSectionFabric),
		MaterialClass: string(entity.MaterialClassFabric),
		FabricAttr: &entity.MaterialFabricAttr{
			WidthCm:    ndec(t, "150"),
			SelvedgeCm: ndec(t, "1.5").Decimal,
		},
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = testDB.ExecContext(context.Background(), `DELETE FROM material WHERE id = ?`, matID)
	})

	var sizeID int
	require.NoError(t, testDB.QueryRowContext(ctx, "SELECT MIN(id) FROM size").Scan(&sizeID))

	mk := func(items ...entity.TechCardBomItem) *entity.TechCardInsert {
		return &entity.TechCardInsert{
			StyleNumber:     sql.NullString{String: "WID-STYLE", Valid: true},
			Name:            "WID",
			Stage:           entity.TechCardStageProto,
			ApprovalState:   entity.TechCardApprovalDraft,
			MeasurementUnit: entity.TechCardUnitMm,
			SizeIds:         []int{sizeID},
			BomItems:        items,
		}
	}

	id, err := s.TechCards().AddTechCard(ctx, mk(
		// Linked, no own width -> article CTI width + selvedge.
		entity.TechCardBomItem{LineKey: "01WIDLINKED000000000000000", Section: entity.BomSectionFabric,
			Name: "основная", MaterialId: sql.NullInt64{Int64: int64(matID), Valid: true}},
		// Linked AND its own width -> the line's snapshot wins.
		entity.TechCardBomItem{LineKey: "01WIDOWNWIDTH000000000000W", Section: entity.BomSectionFabric,
			Name: "своя ширина", MaterialId: sql.NullInt64{Int64: int64(matID), Valid: true},
			FabricWidth: ndec(t, "142")},
		// Unlinked and width-less -> honestly unset.
		entity.TechCardBomItem{LineKey: "01WIDUNLINKED000000000000U", Section: entity.BomSectionFabric,
			Name: "фурнитура-строка"},
	))
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.TechCards().DeleteTechCard(ctx, id) })

	tc, err := s.TechCards().GetTechCardById(ctx, id)
	require.NoError(t, err)
	require.Len(t, tc.BomItems, 3)
	byKey := map[string]entity.TechCardBomItem{}
	for _, b := range tc.BomItems {
		byKey[b.LineKey] = b
	}

	linked := byKey["01WIDLINKED000000000000000"]
	require.True(t, linked.EffectiveFabricWidthCm.Valid)
	require.True(t, linked.EffectiveFabricWidthCm.Decimal.Equal(ndec(t, "150").Decimal))
	require.True(t, linked.SelvedgeCm.Valid)
	require.True(t, linked.SelvedgeCm.Decimal.Equal(ndec(t, "1.5").Decimal))

	own := byKey["01WIDOWNWIDTH000000000000W"]
	require.True(t, own.EffectiveFabricWidthCm.Valid)
	require.True(t, own.EffectiveFabricWidthCm.Decimal.Equal(ndec(t, "142").Decimal))

	unlinked := byKey["01WIDUNLINKED000000000000U"]
	require.False(t, unlinked.EffectiveFabricWidthCm.Valid)
	require.False(t, unlinked.SelvedgeCm.Valid)
}
