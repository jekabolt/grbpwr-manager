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

// TestBomPriceProvenanceAndReprice pins the Phase 3 provenance chain end-to-end against a real
// schema (full migrate incl. 0242–0244): a new priced BOM line saves as 'manual'; the reprice
// action pulls the catalog price and restamps 'catalog' (skipping unlinked lines, counting them);
// a subsequent save that round-trips the repriced value KEEPS the 'catalog' history instead of
// rewriting it to 'manual'; a released card refuses to reprice; and the Phase 2 migration
// exception report reads back with the card's identity joined on.
func TestBomPriceProvenanceAndReprice(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	cfg := *testCfg
	cfg.Automigrate = true
	s, err := NewForTest(ctx, cfg)
	require.NoError(t, err)
	defer s.Close()

	// Catalog material with a current EUR price.
	matID, err := s.TechCards().CreateMaterial(ctx, &entity.MaterialInsert{
		Name: "P3 lining", Section: "lining",
	})
	require.NoError(t, err)
	require.NoError(t, s.TechCards().AddMaterialPrice(ctx, entity.MaterialPrice{
		MaterialId: matID, Price: decimal.RequireFromString("9.99"), Currency: "EUR",
		ValidFrom: time.Now().UTC().AddDate(0, 0, -1), Source: "manual",
	}))

	const linkedKey = "P3LINKEDLINE00000000000001"
	const freeKey = "P3FREETEXTLINE000000000001"
	card := &entity.TechCardInsert{
		StyleNumber: sql.NullString{String: "P3-PROV", Valid: true}, Name: "P3-PROV",
		Stage: entity.TechCardStageProto, ApprovalState: entity.TechCardApprovalDraft,
		MeasurementUnit: entity.TechCardUnitMm,
		Costing:         &entity.TechCardCosting{Currency: sql.NullString{String: "EUR", Valid: true}},
		BomItems: []entity.TechCardBomItem{
			{
				LineKey: linkedKey, Section: entity.BomSectionLining, Name: "P3 lining",
				MaterialId: sql.NullInt64{Int64: int64(matID), Valid: true},
				UnitPrice:  decimal.NullDecimal{Decimal: decimal.RequireFromString("5.00"), Valid: true},
				Currency:   sql.NullString{String: "EUR", Valid: true},
			},
			{
				LineKey: freeKey, Section: entity.BomSectionPackaging, Name: "free-text box",
				UnitPrice: decimal.NullDecimal{Decimal: decimal.RequireFromString("2.00"), Valid: true},
				Currency:  sql.NullString{String: "EUR", Valid: true},
			},
		},
	}
	tcID, err := s.TechCards().AddTechCard(ctx, card)
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.TechCards().DeleteTechCard(context.Background(), tcID) })

	readProv := func(lineKey string) (string, sql.NullTime, string, string) {
		var src sql.NullString
		var at sql.NullTime
		var price, cur string
		require.NoError(t, testDB.QueryRowContext(ctx, `
			SELECT COALESCE(price_source, ''), price_snapshot_at, CAST(unit_price AS CHAR), COALESCE(currency, '')
			FROM tech_card_bom_item WHERE tech_card_id = ? AND line_key = ?`, tcID, lineKey).
			Scan(&src, &at, &price, &cur))
		return src.String, at, price, cur
	}

	// 1. A new priced line is 'manual' as of the save.
	src, at, price, _ := readProv(linkedKey)
	require.Equal(t, entity.BomPriceSourceManual, src)
	require.True(t, at.Valid)
	require.Equal(t, "5.0000", price)
	manualStamp := at.Time

	// 2. Reprice pulls the catalog price into the linked line and counts the free-text one.
	// It also moves the optimistic-lock fence: a form opened BEFORE the reprice must not be able
	// to save the stale prices back over it (adversarial #4).
	staleCard, err := s.TechCards().GetTechCardById(ctx, tcID)
	require.NoError(t, err)
	var lockBefore int
	require.NoError(t, testDB.QueryRowContext(ctx,
		"SELECT lock_version FROM tech_card WHERE id = ?", tcID).Scan(&lockBefore))
	lines, skippedUnlinked, err := s.TechCards().RepriceTechCardBom(ctx, tcID, "EUR")
	require.NoError(t, err)
	var lockAfter int
	require.NoError(t, testDB.QueryRowContext(ctx,
		"SELECT lock_version FROM tech_card WHERE id = ?", tcID).Scan(&lockAfter))
	require.Equal(t, lockBefore+1, lockAfter, "reprice changes content, so it must bump lock_version")
	require.ErrorIs(t, s.TechCards().UpdateTechCard(ctx, tcID, &staleCard.TechCardInsert, staleCard.LockVersion),
		entity.ErrTechCardConflict, "a pre-reprice form must conflict, not silently revert the reprice")
	require.Equal(t, 1, skippedUnlinked)
	require.Len(t, lines, 1)
	require.Equal(t, linkedKey, lines[0].LineKey)
	require.True(t, lines[0].Changed)
	require.True(t, lines[0].NewPrice.Valid)
	require.True(t, lines[0].NewPrice.Decimal.Equal(decimal.RequireFromString("9.99")))

	src, at, price, cur := readProv(linkedKey)
	require.Equal(t, entity.BomPriceSourceCatalog, src)
	require.Equal(t, "9.9900", price)
	require.Equal(t, "EUR", cur)
	catalogStamp := at.Time

	// The free-text line was not touched.
	src, _, price, _ = readProv(freeKey)
	require.Equal(t, entity.BomPriceSourceManual, src)
	require.Equal(t, "2.0000", price)

	// 3. A save that round-trips the repriced value keeps the 'catalog' history.
	stored, err := s.TechCards().GetTechCardById(ctx, tcID)
	require.NoError(t, err)
	require.Len(t, stored.BomItems, 2)
	require.NoError(t, s.TechCards().UpdateTechCard(ctx, tcID, &stored.TechCardInsert, stored.LockVersion))
	src, at, price, _ = readProv(linkedKey)
	require.Equal(t, entity.BomPriceSourceCatalog, src, "a round-tripping save must not rewrite catalog provenance")
	require.Equal(t, "9.9900", price)
	require.Equal(t, catalogStamp.Unix(), at.Time.Unix())

	// 4. An EDITED price through a save restamps 'manual'.
	stored.BomItems[0].UnitPrice = decimal.NullDecimal{Decimal: decimal.RequireFromString("11.50"), Valid: true}
	stored2, err := s.TechCards().GetTechCardById(ctx, tcID)
	require.NoError(t, err)
	edited := stored2.TechCardInsert
	for i := range edited.BomItems {
		if edited.BomItems[i].LineKey == linkedKey {
			edited.BomItems[i].UnitPrice = decimal.NullDecimal{Decimal: decimal.RequireFromString("11.50"), Valid: true}
		}
	}
	require.NoError(t, s.TechCards().UpdateTechCard(ctx, tcID, &edited, stored2.LockVersion))
	src, _, price, _ = readProv(linkedKey)
	require.Equal(t, entity.BomPriceSourceManual, src)
	require.Equal(t, "11.5000", price)
	_ = manualStamp

	// 5. Only RELEASED freezes the reprice — an in_review card is price-editable through a normal
	// save (storeutil.RequireMutableTechCard), so it reprices too (adversarial #7).
	_, err = testDB.ExecContext(ctx, "UPDATE tech_card SET approval_state = 'in_review' WHERE id = ?", tcID)
	require.NoError(t, err)
	_, _, err = s.TechCards().RepriceTechCardBom(ctx, tcID, "EUR")
	require.NoError(t, err, "in_review must reprice like any other mutable state")
	_, err = testDB.ExecContext(ctx, "UPDATE tech_card SET approval_state = 'released' WHERE id = ?", tcID)
	require.NoError(t, err)
	_, _, err = s.TechCards().RepriceTechCardBom(ctx, tcID, "EUR")
	require.ErrorIs(t, err, entity.ErrTechCardReleased)

	// 6. The Phase 2 exception report reads back with the card identity joined on; card filter works.
	_, err = testDB.ExecContext(ctx, `
		INSERT INTO tech_card_costing_migration_exception
			(tech_card_id, article, kind, amount, currency, approval_state)
		VALUES (?, 'packaging', 'not_draft', 3.50, 'EUR', 'released')`, tcID)
	require.NoError(t, err)
	exceptions, err := s.TechCards().ListCostingMigrationExceptions(ctx, tcID)
	require.NoError(t, err)
	require.Len(t, exceptions, 1)
	require.Equal(t, "P3-PROV", exceptions[0].TechCardName)
	require.Equal(t, "packaging", exceptions[0].Article)
	require.True(t, exceptions[0].Amount.Equal(decimal.RequireFromString("3.50")))
	all, err := s.TechCards().ListCostingMigrationExceptions(ctx, 0)
	require.NoError(t, err)
	require.NotEmpty(t, all)
	none, err := s.TechCards().ListCostingMigrationExceptions(ctx, tcID+999999)
	require.NoError(t, err)
	require.Empty(t, none)
}
