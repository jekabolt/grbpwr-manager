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

// TestThreadUnitBirthAndReprice proves both server-owned price write boundaries against MySQL. A
// new line copied from a cone article is born as a metre slot with a per-metre price; explicit
// reprice does the same conversion. A package article without length is born unpriced and reprice
// leaves it untouched rather than claiming a catalog price with the wrong denominator.
func TestThreadUnitBirthAndReprice(t *testing.T) {
	requireCutStripIntegrationDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	cfg := *testCfg
	cfg.Automigrate = true
	s, err := NewForTest(ctx, cfg)
	require.NoError(t, err)
	defer s.Close()
	tc := s.TechCards()

	createThread := func(name string, withLength bool) int {
		attr := &entity.MaterialThreadAttr{}
		if withLength {
			attr.LengthPerConeM = decimal.NewNullDecimal(decimal.RequireFromString("5000"))
		}
		id, err := tc.CreateMaterial(ctx, &entity.MaterialInsert{
			Name: name, Section: "thread", MaterialClass: string(entity.MaterialClassThread),
			Unit: sql.NullString{String: "cone", Valid: true}, ThreadAttr: attr,
		})
		require.NoError(t, err)
		require.NoError(t, tc.AddMaterialPrice(ctx, entity.MaterialPrice{
			MaterialId: id, Price: decimal.RequireFromString("5"), Currency: "EUR",
			ValidFrom: time.Now().UTC().AddDate(0, 0, -1), Source: "manual",
		}))
		return id
	}
	convertibleID := createThread("thread cone 5000", true)
	missingID := createThread("thread cone missing length", false)

	const (
		birthKey   = "01JCVT00000000000000THR001"
		repriceKey = "01JCVT00000000000000THR002"
		missingKey = "01JCVT00000000000000THR003"
	)
	cardID, err := tc.AddTechCard(ctx, &entity.TechCardInsert{
		StyleNumber:     sql.NullString{String: "THREAD-UNITS", Valid: true},
		Name:            "THREAD-UNITS",
		Stage:           entity.TechCardStageProto,
		ApprovalState:   entity.TechCardApprovalDraft,
		MeasurementUnit: entity.TechCardUnitMm,
		Costing:         &entity.TechCardCosting{Currency: sql.NullString{String: "EUR", Valid: true}},
		BomItems: []entity.TechCardBomItem{
			{
				LineKey: birthKey, Section: entity.BomSectionThread, Name: "birth",
				MaterialId: sql.NullInt64{Int64: int64(convertibleID), Valid: true},
				Unit:       sql.NullString{String: "cone", Valid: true},
				UnitPrice:  decimal.NewNullDecimal(decimal.RequireFromString("5")),
				Currency:   sql.NullString{String: "EUR", Valid: true},
			},
			{
				LineKey: repriceKey, Section: entity.BomSectionThread, Name: "reprice",
				MaterialId: sql.NullInt64{Int64: int64(convertibleID), Valid: true},
				Unit:       sql.NullString{String: "m", Valid: true},
			},
			{
				LineKey: missingKey, Section: entity.BomSectionThread, Name: "missing",
				MaterialId: sql.NullInt64{Int64: int64(missingID), Valid: true},
				Unit:       sql.NullString{String: "cone", Valid: true},
				UnitPrice:  decimal.NewNullDecimal(decimal.RequireFromString("5")),
				Currency:   sql.NullString{String: "EUR", Valid: true},
			},
		},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = tc.DeleteTechCard(context.Background(), cardID) })

	readStored := func(key string) (unit string, price sql.NullString, currency sql.NullString) {
		require.NoError(t, testDB.QueryRowContext(ctx, `
			SELECT COALESCE(unit, ''), CAST(unit_price AS CHAR), currency
			FROM tech_card_bom_item WHERE tech_card_id = ? AND line_key = ?`, cardID, key).
			Scan(&unit, &price, &currency))
		return unit, price, currency
	}
	unit, price, _ := readStored(birthKey)
	require.Equal(t, "m", unit)
	require.Equal(t, "0.0010", price.String)
	unit, price, currency := readStored(missingKey)
	require.Equal(t, "m", unit)
	require.False(t, price.Valid)
	require.False(t, currency.Valid)

	lines, _, err := tc.RepriceTechCardBom(ctx, cardID, "EUR")
	require.NoError(t, err)
	byKey := make(map[string]entity.RepricedBomLine, len(lines))
	for _, line := range lines {
		byKey[line.LineKey] = line
	}
	require.True(t, byKey[repriceKey].Changed)
	require.True(t, byKey[repriceKey].NewPrice.Valid)
	require.Equal(t, "0.001", byKey[repriceKey].NewPrice.Decimal.String())
	require.False(t, byKey[missingKey].Changed)
	require.False(t, byKey[missingKey].NewPrice.Valid)
	_, price, _ = readStored(repriceKey)
	require.Equal(t, "0.0010", price.String)
	_, price, _ = readStored(missingKey)
	require.False(t, price.Valid)
}
