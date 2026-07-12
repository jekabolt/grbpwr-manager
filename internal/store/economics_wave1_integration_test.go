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

// TestEconomicsWave1CostProvenance exercises the task-02 cost-provenance store methods against
// a real MySQL: deterministic primary-card assignment (first card wins), seeding only via the
// primary card, manual costs never overwritten by a seed, and the explicit force override.
// Throwaway harness — cleans up in reverse-dependency order.
func TestEconomicsWave1CostProvenance(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	cfg := *testCfg
	cfg.Automigrate = true
	s, err := NewForTest(ctx, cfg)
	require.NoError(t, err)
	defer s.Close()

	var mediaID, prodID, tc1, tc2 int
	defer func() {
		if tc1 != 0 {
			_ = s.TechCards().DeleteTechCard(ctx, tc1)
		}
		if tc2 != 0 {
			_ = s.TechCards().DeleteTechCard(ctx, tc2)
		}
		if prodID != 0 {
			_, _ = testDB.ExecContext(ctx, "DELETE FROM product WHERE id = ?", prodID)
		}
		if mediaID != 0 {
			_ = s.Media().DeleteMediaById(ctx, mediaID)
		}
	}()

	nd := func(v string) decimal.NullDecimal {
		return decimal.NullDecimal{Decimal: decimal.RequireFromString(v), Valid: true}
	}

	// media (thumbnail FK target)
	mediaID, err = s.Media().AddMedia(ctx, &entity.MediaItem{
		FullSizeMediaURL: "https://x/f.jpg", FullSizeWidth: 100, FullSizeHeight: 100,
		ThumbnailMediaURL: "https://x/t.jpg", ThumbnailWidth: 10, ThumbnailHeight: 10,
		CompressedMediaURL: "https://x/c.jpg", CompressedWidth: 50, CompressedHeight: 50,
	})
	require.NoError(t, err)

	// minimal product with no cost yet (category id 1 is seeded by migrations)
	res, err := testDB.ExecContext(ctx, `INSERT INTO product
		(sku, brand, color, color_hex, country_of_origin, thumbnail_id, top_category_id, target_gender, version)
		VALUES ('ECOTEST', 'b', 'c', '#000000', 'US', ?, 1, 'unisex', 'v1')`, mediaID)
	require.NoError(t, err)
	pid64, err := res.LastInsertId()
	require.NoError(t, err)
	prodID = int(pid64)

	mkCard := func(style string) int {
		id, err := s.TechCards().AddTechCard(ctx, &entity.TechCardInsert{
			StyleNumber:     style,
			Name:            "n",
			Stage:           entity.TechCardStageProto,
			ApprovalState:   entity.TechCardApprovalDraft,
			MeasurementUnit: entity.TechCardUnitMm,
			SizeIds:         []int{4},
			ProductIds:      []int{prodID},
			Costing:         &entity.TechCardCosting{CmtCost: nd("10"), Currency: sql.NullString{String: "EUR", Valid: true}},
		})
		require.NoError(t, err)
		return id
	}
	tc1 = mkCard("ECO-1")
	tc2 = mkCard("ECO-2")

	P := s.Products()

	// primary assignment: the first card to claim an unset product wins; the second is a no-op.
	require.NoError(t, P.AssignPrimaryTechCardIfUnset(ctx, tc1, []int{prodID}))
	require.NoError(t, P.AssignPrimaryTechCardIfUnset(ctx, tc2, []int{prodID}))
	ci, err := P.GetProductCostInfo(ctx, prodID)
	require.NoError(t, err)
	require.Equal(t, int32(tc1), ci.PrimaryTechCardID.Int32, "first card is primary")

	// seed via the primary card updates the product; via a non-primary card it is a no-op.
	n, err := P.SeedProductsCostPriceFromTechCard(ctx, tc1, decimal.RequireFromString("10"))
	require.NoError(t, err)
	require.Equal(t, int64(1), n)
	ci, err = P.GetProductCostInfo(ctx, prodID)
	require.NoError(t, err)
	require.True(t, ci.CostPrice.Valid)
	require.Equal(t, "10", ci.CostPrice.Decimal.String())
	require.Equal(t, "tech_card", ci.CostPriceSource.String)
	require.Equal(t, int32(tc1), ci.CostPriceTechCardID.Int32)

	n, err = P.SeedProductsCostPriceFromTechCard(ctx, tc2, decimal.RequireFromString("20"))
	require.NoError(t, err)
	require.Equal(t, int64(0), n, "non-primary card seeds nothing")
	ci, _ = P.GetProductCostInfo(ctx, prodID)
	require.Equal(t, "10", ci.CostPrice.Decimal.String(), "cost unchanged by non-primary card")

	// a manually-set cost is never overwritten by a seed.
	_, err = testDB.ExecContext(ctx, "UPDATE product SET cost_price_source='manual' WHERE id=?", prodID)
	require.NoError(t, err)
	n, err = P.SeedProductsCostPriceFromTechCard(ctx, tc1, decimal.RequireFromString("30"))
	require.NoError(t, err)
	require.Equal(t, int64(0), n, "manual cost not overwritten by seed")
	ci, _ = P.GetProductCostInfo(ctx, prodID)
	require.Equal(t, "10", ci.CostPrice.Decimal.String())

	// the explicit force override does overwrite a manual cost.
	require.NoError(t, P.ForceSetProductCostPriceFromTechCard(ctx, prodID, tc1, decimal.RequireFromString("30")))
	ci, _ = P.GetProductCostInfo(ctx, prodID)
	require.Equal(t, "30", ci.CostPrice.Decimal.String())
	require.Equal(t, "tech_card", ci.CostPriceSource.String)

	// link existence check used by the sync RPC.
	linked, err := P.IsProductLinkedToTechCard(ctx, prodID, tc1)
	require.NoError(t, err)
	require.True(t, linked)
	linked, err = P.IsProductLinkedToTechCard(ctx, prodID, 99999999)
	require.NoError(t, err)
	require.False(t, linked)
}

// TestCostingFxRates exercises the task-04 manual FX rate store methods: upsert-by-key, the
// latest-effective-rate-per-currency read (ignoring future-dated rows), and update-in-place.
func TestCostingFxRates(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cfg := *testCfg
	cfg.Automigrate = true
	s, err := NewForTest(ctx, cfg)
	require.NoError(t, err)
	defer s.Close()
	defer func() { _, _ = testDB.ExecContext(ctx, "DELETE FROM costing_fx_rate WHERE currency IN ('USD','CNY')") }()

	T := s.TechCards()
	d := func(v string) decimal.Decimal { return decimal.RequireFromString(v) }
	day := func(y int, m time.Month, dd int) time.Time { return time.Date(y, m, dd, 0, 0, 0, 0, time.UTC) }

	require.NoError(t, T.UpsertCostingFxRates(ctx, []entity.CostingFxRate{
		{Currency: "USD", RateToBase: d("0.90"), ValidFrom: day(2026, 1, 1)},
		{Currency: "USD", RateToBase: d("0.95"), ValidFrom: day(2026, 6, 1)},
		{Currency: "USD", RateToBase: d("1.00"), ValidFrom: day(2099, 1, 1)}, // future — ignored
		{Currency: "CNY", RateToBase: d("0.13"), ValidFrom: day(2026, 1, 1)},
	}))

	rates, err := T.GetCostingFxRatesToBase(ctx)
	require.NoError(t, err)
	require.True(t, rates["USD"].Equal(d("0.95")), "latest effective USD rate, not the future one: got %s", rates["USD"])
	require.True(t, rates["CNY"].Equal(d("0.13")))

	all, err := T.ListCostingFxRates(ctx)
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(all), 4)

	// update-in-place by (currency, valid_from)
	require.NoError(t, T.UpsertCostingFxRates(ctx, []entity.CostingFxRate{
		{Currency: "USD", RateToBase: d("0.96"), ValidFrom: day(2026, 6, 1)},
	}))
	rates, err = T.GetCostingFxRatesToBase(ctx)
	require.NoError(t, err)
	require.True(t, rates["USD"].Equal(d("0.96")), "updated in place: got %s", rates["USD"])
}
