package store

import (
	"context"
	"database/sql"
	"os"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/currency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/rubenv/sql-migrate/sqlparse"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

// TestCostingHardwarePackagingBackfillMigration replays 0237's Up statements from the real file
// over the four populations plan 01 §1.1 defines and pins each rule: a draft card with colourways
// gets a synthetic BOM line (LEGACY line_key) + one usage per colourway and its scalar cleared; a
// draft card whose section already has an AUTHORED row is recorded double_counted (scalar cleared,
// BOM wins, no synthetic row); a draft card with no colourways and a non-draft card keep their
// scalars in the retained columns and land in the exception report. Replaying everything twice
// proves the mid-file-crash re-run path adds nothing.
func TestCostingHardwarePackagingBackfillMigration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	cfg := *testCfg
	cfg.Automigrate = true
	s, err := NewForTest(ctx, cfg)
	require.NoError(t, err)
	defer s.Close()

	mkCard := func(name, approval string) int {
		id, err := s.TechCards().AddTechCard(ctx, &entity.TechCardInsert{
			StyleNumber: sql.NullString{String: name, Valid: true}, Name: name, Stage: entity.TechCardStageProto,
			ApprovalState: entity.TechCardApprovalDraft, MeasurementUnit: entity.TechCardUnitMm,
		})
		require.NoError(t, err)
		t.Cleanup(func() { _ = s.TechCards().DeleteTechCard(context.Background(), id) })
		if approval != "draft" {
			_, err := testDB.ExecContext(ctx, "UPDATE tech_card SET approval_state = ? WHERE id = ?", approval, id)
			require.NoError(t, err)
		}
		return id
	}
	// The retained columns are invisible to the app now — seed them the way legacy data holds them.
	seedScalar := func(cardID int, column string, v string) {
		_, err := testDB.ExecContext(ctx,
			"INSERT INTO tech_card_costing (tech_card_id, "+column+", currency) VALUES (?, ?, 'EUR') "+
				"ON DUPLICATE KEY UPDATE "+column+" = VALUES("+column+")", cardID, v)
		require.NoError(t, err)
	}

	cardMigrate := mkCard("P2-MIGRATE", "draft")
	cardDouble := mkCard("P2-DOUBLE", "draft")
	cardUnwired := mkCard("P2-UNWIRED", "draft")
	cardZeroCw := mkCard("P2-ZEROCW", "draft")
	cardFrozen := mkCard("P2-FROZEN", "released")

	seedScalar(cardMigrate, "hardware_cost", "5.00")
	seedScalar(cardDouble, "packaging_cost", "2.00")
	seedScalar(cardUnwired, "packaging_cost", "1.20")
	seedScalar(cardZeroCw, "hardware_cost", "3.00")
	seedScalar(cardFrozen, "hardware_cost", "7.00")

	// cardDouble's authored packaging row — WIRED below (priced + carried by a usage), so it really
	// contributes money and the scalar really double-counts.
	_, err = testDB.ExecContext(ctx, `
		INSERT INTO tech_card_bom_item (tech_card_id, section, name, unit_price, currency, line_key)
		VALUES (?, 'packaging', 'authored polybag', 0.80, 'EUR', 'AUTHOREDPOLYBAG00000000001')`, cardDouble)
	require.NoError(t, err)

	// cardUnwired's authored packaging row: priced but carried by NO usage — it contributes nothing
	// to any colourway cost (the live-beta shape), so the scalar is the section's only money.
	_, err = testDB.ExecContext(ctx, `
		INSERT INTO tech_card_bom_item (tech_card_id, section, name, unit_price, currency, line_key)
		VALUES (?, 'packaging', 'unwired shipping box', 0.60, 'EUR', 'UNWIREDBOX0000000000000001')`, cardUnwired)
	require.NoError(t, err)

	// Colourways: post-R1 a colourway is a product with style_id = the card.
	var sizeA int
	require.NoError(t, testDB.QueryRowContext(ctx, `SELECT id FROM size WHERE sku_ord != 0 ORDER BY id LIMIT 1`).Scan(&sizeA))
	var langID int
	require.NoError(t, testDB.QueryRowContext(ctx, "SELECT MIN(id) FROM language").Scan(&langID))
	mediaID, err := s.Media().AddMedia(ctx, &entity.MediaItem{
		FullSizeMediaURL: "https://x/f.jpg", FullSizeWidth: 100, FullSizeHeight: 100,
		ThumbnailMediaURL: "https://x/t.jpg", ThumbnailWidth: 10, ThumbnailHeight: 10,
		CompressedMediaURL: "https://x/c.jpg", CompressedWidth: 50, CompressedHeight: 50,
	})
	require.NoError(t, err)
	prices := make([]entity.ColorwayPriceInsert, 0)
	for _, c := range currency.RequiredCurrencies() {
		prices = append(prices, entity.ColorwayPriceInsert{Currency: c, Price: decimal.NewFromInt(100)})
	}
	if len(prices) == 0 {
		prices = append(prices, entity.ColorwayPriceInsert{Currency: "EUR", Price: decimal.NewFromInt(100)})
	}
	// "BLK" is the one color code the seed guarantees — the colour identity is irrelevant here.
	mkColorway := func(cardID int, tag string) int {
		id, err := s.Products().AddProduct(ctx, &entity.ColorwayNew{
			Product: &entity.ColorwayInsert{
				ProductBodyInsert: entity.ColorwayBodyInsert{
					Brand: "ACME", Color: "black", ColorCode: "BLK", CountryOfOrigin: "IT",
					TopCategoryId: 1, TargetGender: entity.Unisex, Season: entity.SeasonSS,
				},
				ThumbnailMediaID: mediaID,
				Translations:     []entity.ColorwayTranslationInsert{{LanguageId: langID, Name: "P2-CW-" + tag, Description: "d"}},
				Prices:           prices,
			},
			SizeMeasurements: []entity.SizeWithMeasurementInsert{
				{ProductSize: entity.VariantInsert{SizeId: sizeA, Quantity: decimal.NewFromInt(1)}},
			},
			MediaIds: []int{mediaID}, Tags: []entity.ColorwayTagInsert{}, Prices: prices,
		})
		require.NoError(t, err)
		t.Cleanup(func() { _, _ = testDB.ExecContext(context.Background(), "DELETE FROM product WHERE id = ?", id) })
		_, err = testDB.ExecContext(ctx, "UPDATE product SET style_id = ? WHERE id = ?", cardID, id)
		require.NoError(t, err)
		return id
	}
	prodID := mkColorway(cardMigrate, "MIG")
	cwDouble := mkColorway(cardDouble, "DBL")
	mkColorway(cardUnwired, "UNW")

	// Wire cardDouble's authored row into its colourway recipe: this is what makes it MONEY.
	_, err = testDB.ExecContext(ctx, `
		INSERT INTO tech_card_colorway_usage (colorway_id, bom_item_id, consumption, display_order)
		SELECT ?, id, 1, 0 FROM tech_card_bom_item WHERE tech_card_id = ? AND line_key = 'AUTHOREDPOLYBAG00000000001'`,
		cwDouble, cardDouble)
	require.NoError(t, err)

	replay := func() {
		f, err := os.Open("sql/0237_costing_hardware_packaging_to_bom.sql")
		require.NoError(t, err)
		parsed, err := sqlparse.ParseMigration(f)
		require.NoError(t, f.Close())
		require.NoError(t, err)
		for i, stmt := range parsed.UpStatements {
			_, err := testDB.ExecContext(ctx, stmt)
			require.NoError(t, err, "0237 statement %d", i)
		}
	}
	replay()

	assertState := func() {
		// cardMigrate: synthetic row + LEGACY key + usage on the product + scalar cleared, no exception.
		var bomID int
		var lineKey string
		var unitPrice decimal.Decimal
		require.NoError(t, testDB.QueryRowContext(ctx, `
			SELECT id, COALESCE(line_key,''), unit_price FROM tech_card_bom_item
			WHERE tech_card_id = ? AND section = 'hardware'`, cardMigrate).Scan(&bomID, &lineKey, &unitPrice))
		require.Regexp(t, `^LEGACY[0-9]{20}$`, lineKey)
		require.True(t, unitPrice.Equal(decimal.RequireFromString("5.00")))
		var usages int
		require.NoError(t, testDB.QueryRowContext(ctx, `
			SELECT COUNT(*) FROM tech_card_colorway_usage WHERE colorway_id = ? AND bom_item_id = ?`,
			prodID, bomID).Scan(&usages))
		require.Equal(t, 1, usages, "one usage per colourway")
		var hw sql.NullString
		require.NoError(t, testDB.QueryRowContext(ctx,
			"SELECT hardware_cost FROM tech_card_costing WHERE tech_card_id = ?", cardMigrate).Scan(&hw))
		require.False(t, hw.Valid, "migrated scalar cleared")

		var cnt int
		require.NoError(t, testDB.QueryRowContext(ctx,
			"SELECT COUNT(*) FROM tech_card_costing_migration_exception WHERE tech_card_id = ?", cardMigrate).Scan(&cnt))
		require.Zero(t, cnt, "clean migration records no exception")

		// cardDouble: no synthetic row, scalar cleared, double_counted recorded with the amount.
		require.NoError(t, testDB.QueryRowContext(ctx, `
			SELECT COUNT(*) FROM tech_card_bom_item WHERE tech_card_id = ? AND section = 'packaging'`,
			cardDouble).Scan(&cnt))
		require.Equal(t, 1, cnt, "only the authored row")
		var pkg sql.NullString
		require.NoError(t, testDB.QueryRowContext(ctx,
			"SELECT packaging_cost FROM tech_card_costing WHERE tech_card_id = ?", cardDouble).Scan(&pkg))
		require.False(t, pkg.Valid, "double-counted scalar cleared (BOM wins)")
		var amount decimal.Decimal
		require.NoError(t, testDB.QueryRowContext(ctx, `
			SELECT amount FROM tech_card_costing_migration_exception
			WHERE tech_card_id = ? AND article='packaging' AND kind='double_counted'`, cardDouble).Scan(&amount))
		require.True(t, amount.Equal(decimal.RequireFromString("2.00")), "the double-counted value is preserved in the report")
		require.NoError(t, testDB.QueryRowContext(ctx, `
			SELECT COUNT(*) FROM tech_card_colorway_usage u
			JOIN tech_card_bom_item b ON b.id = u.bom_item_id
			WHERE b.tech_card_id = ?`, cardDouble).Scan(&cnt))
		require.Equal(t, 1, cnt, "the authored wired usage is untouched and nothing new is attached")

		// cardUnwired (the live-beta shape): the authored row is priced but carried by no usage, so
		// it contributes nothing — the scalar is the section's only money and migrates into a
		// synthetic row BESIDE the descriptive authored one. No exception: nothing double-counted.
		var synthID int
		var synthKey string
		var synthPrice decimal.Decimal
		require.NoError(t, testDB.QueryRowContext(ctx, `
			SELECT id, COALESCE(line_key,''), unit_price FROM tech_card_bom_item
			WHERE tech_card_id = ? AND section = 'packaging' AND name = 'Packaging (migrated from costing)'`,
			cardUnwired).Scan(&synthID, &synthKey, &synthPrice))
		require.Regexp(t, `^LEGACY[0-9]{20}$`, synthKey)
		require.True(t, synthPrice.Equal(decimal.RequireFromString("1.20")), "the scalar's money lives on the synthetic row")
		require.NoError(t, testDB.QueryRowContext(ctx, `
			SELECT COUNT(*) FROM tech_card_bom_item WHERE tech_card_id = ? AND section = 'packaging'`,
			cardUnwired).Scan(&cnt))
		require.Equal(t, 2, cnt, "authored descriptive row + synthetic row")
		require.NoError(t, testDB.QueryRowContext(ctx, `
			SELECT COUNT(*) FROM tech_card_colorway_usage WHERE bom_item_id = ?`, synthID).Scan(&cnt))
		require.Equal(t, 1, cnt, "one usage per colourway on the synthetic row")
		require.NoError(t, testDB.QueryRowContext(ctx, `
			SELECT COUNT(*) FROM tech_card_colorway_usage u
			JOIN tech_card_bom_item b ON b.id = u.bom_item_id
			WHERE b.tech_card_id = ? AND b.id <> ?`, cardUnwired, synthID).Scan(&cnt))
		require.Zero(t, cnt, "no usage hung on the authored unwired row")
		var upk sql.NullString
		require.NoError(t, testDB.QueryRowContext(ctx,
			"SELECT packaging_cost FROM tech_card_costing WHERE tech_card_id = ?", cardUnwired).Scan(&upk))
		require.False(t, upk.Valid, "migrated scalar cleared")
		require.NoError(t, testDB.QueryRowContext(ctx, `
			SELECT COUNT(*) FROM tech_card_costing_migration_exception WHERE tech_card_id = ?`, cardUnwired).Scan(&cnt))
		require.Zero(t, cnt, "an unwired authored row is not a double-count")

		// cardZeroCw: nothing migrated, scalar kept, zero_colorways recorded.
		require.NoError(t, testDB.QueryRowContext(ctx, `
			SELECT COUNT(*) FROM tech_card_bom_item WHERE tech_card_id = ?`, cardZeroCw).Scan(&cnt))
		require.Zero(t, cnt)
		var zh sql.NullString
		require.NoError(t, testDB.QueryRowContext(ctx,
			"SELECT hardware_cost FROM tech_card_costing WHERE tech_card_id = ?", cardZeroCw).Scan(&zh))
		require.True(t, zh.Valid, "zero-colourway scalar retained for manual migration")
		require.NoError(t, testDB.QueryRowContext(ctx, `
			SELECT COUNT(*) FROM tech_card_costing_migration_exception
			WHERE tech_card_id = ? AND kind='zero_colorways'`, cardZeroCw).Scan(&cnt))
		require.Equal(t, 1, cnt)

		// cardFrozen: untouched by SQL, not_draft recorded.
		require.NoError(t, testDB.QueryRowContext(ctx, `
			SELECT COUNT(*) FROM tech_card_bom_item WHERE tech_card_id = ?`, cardFrozen).Scan(&cnt))
		require.Zero(t, cnt)
		require.NoError(t, testDB.QueryRowContext(ctx, `
			SELECT COUNT(*) FROM tech_card_costing_migration_exception
			WHERE tech_card_id = ? AND kind='not_draft'`, cardFrozen).Scan(&cnt))
		require.Equal(t, 1, cnt)
	}
	assertState()

	// The mid-file-crash re-run: everything replays; nothing doubles.
	replay()
	assertState()
	var usageTotal int
	require.NoError(t, testDB.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM tech_card_colorway_usage u
		JOIN tech_card_bom_item b ON b.id = u.bom_item_id
		WHERE b.tech_card_id = ?`, cardMigrate).Scan(&usageTotal))
	require.Equal(t, 1, usageTotal, "re-run adds no duplicate usages")
}
