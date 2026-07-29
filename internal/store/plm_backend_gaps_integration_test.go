package store

import (
	"context"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/cache"
	"github.com/jekabolt/grbpwr-manager/internal/currency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

// gapsFixture is the minimum a PLM-gap test needs: a style with one colourway, plus two sizes and two
// measurement names from the dictionary. Built through the real write paths so the assertions are
// about behaviour, not about hand-inserted rows.
type gapsFixture struct {
	styleID   int
	productID int
	sizeA     int
	sizeB     int
	measA     int
	measB     int
	mediaID   int
	langID    int
	prices    []entity.ColorwayPriceInsert
}

func newGapsFixture(ctx context.Context, t *testing.T, s *MYSQLStore) gapsFixture {
	t.Helper()

	di, err := s.Cache().GetDictionaryInfo(ctx)
	require.NoError(t, err)
	hf, err := s.Hero().GetHero(ctx)
	require.NoError(t, err)
	require.NoError(t, cache.InitConsts(ctx, di, hf))

	ids := queryInts(ctx, t, `SELECT id FROM size WHERE sku_system = 'apparel' ORDER BY sku_ord LIMIT 2`)
	require.GreaterOrEqual(t, len(ids), 2)
	mids := queryInts(ctx, t, `SELECT id FROM measurement_name ORDER BY id LIMIT 2`)
	require.GreaterOrEqual(t, len(mids), 2)

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
		prices = append(prices, entity.ColorwayPriceInsert{Currency: c, Price: decimal.NewFromInt(20000)})
	}
	if len(prices) == 0 {
		prices = append(prices, entity.ColorwayPriceInsert{Currency: "EUR", Price: decimal.NewFromInt(20000)})
	}
	prodID, err := s.Products().AddProduct(ctx, &entity.ColorwayNew{
		Product: &entity.ColorwayInsert{
			ProductBodyInsert: entity.ColorwayBodyInsert{
				Brand: "ACME", Color: "black", ColorCode: "BLK", CountryOfOrigin: "IT",
				TopCategoryId: 1, TargetGender: entity.Unisex, Season: entity.SeasonSS,
			},
			ThumbnailMediaID: mediaID,
			Translations:     []entity.ColorwayTranslationInsert{{LanguageId: langID, Name: "GAPS", Description: "d"}},
			Prices:           prices,
		},
		SizeMeasurements: []entity.SizeWithMeasurementInsert{{ProductSize: entity.VariantInsert{SizeId: ids[0], Quantity: decimal.NewFromInt(1)}}},
		MediaIds:         []int{mediaID},
		Tags:             []entity.ColorwayTagInsert{},
		Prices:           prices,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = testDB.ExecContext(ctx, "DELETE FROM product WHERE id = ?", prodID) })

	var styleID int
	require.NoError(t, testDB.QueryRowContext(ctx, `SELECT style_id FROM product WHERE id = ?`, prodID).Scan(&styleID))

	return gapsFixture{styleID: styleID, productID: prodID, sizeA: ids[0], sizeB: ids[1], measA: mids[0], measB: mids[1], mediaID: mediaID, langID: langID, prices: prices}
}

func queryInts(ctx context.Context, t *testing.T, q string) []int {
	t.Helper()
	rows, err := testDB.QueryContext(ctx, q)
	require.NoError(t, err)
	defer rows.Close()
	out := []int{}
	for rows.Next() {
		var id int
		require.NoError(t, rows.Scan(&id))
		out = append(out, id)
	}
	return out
}

func newGapsStore(ctx context.Context, t *testing.T) *MYSQLStore {
	t.Helper()
	cfg := *testCfg
	cfg.Automigrate = true
	s, err := NewForTest(ctx, cfg)
	require.NoError(t, err)
	t.Cleanup(func() { s.Close() })
	return s
}

// TestStyleGradeRulePersists covers PLM gap 4: the base size + per-measurement step behind an
// expanded size chart are stored and read back, and clearing them is expressible.
func TestStyleGradeRulePersists(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	s := newGapsStore(ctx, t)
	f := newGapsFixture(ctx, t, s)

	chart0, err := s.TechCards().GetStyleSizeChart(ctx, f.styleID)
	require.NoError(t, err)
	require.Zero(t, chart0.GradeBaseSizeID, "a chart with no rule authored reports no base")
	require.Empty(t, chart0.GradeSteps)

	cells := []entity.StyleSizeChartCell{
		{SizeID: f.sizeA, MeasurementNameID: f.measA, Value: decimal.NewFromInt(50)},
		{SizeID: f.sizeB, MeasurementNameID: f.measA, Value: decimal.NewFromInt(54)},
	}
	steps := []entity.StyleSizeChartGradeStep{{MeasurementNameID: f.measA, Step: decimal.NewFromInt(4)}}

	saved, err := s.TechCards().UpdateStyleSizeChart(ctx, f.styleID, chart0.LockVersion, cells, f.sizeA, steps)
	require.NoError(t, err)
	require.Equal(t, f.sizeA, saved.GradeBaseSizeID)
	require.Len(t, saved.GradeSteps, 1)
	require.Equal(t, f.measA, saved.GradeSteps[0].MeasurementNameID)
	require.True(t, saved.GradeSteps[0].Step.Equal(decimal.NewFromInt(4)))

	// The rule survives a fresh read — the whole point of the gap.
	reread, err := s.TechCards().GetStyleSizeChart(ctx, f.styleID)
	require.NoError(t, err)
	require.Equal(t, f.sizeA, reread.GradeBaseSizeID)
	require.Len(t, reread.GradeSteps, 1)

	// A negative step is a real grade direction, not an error.
	negative := []entity.StyleSizeChartGradeStep{{MeasurementNameID: f.measA, Step: decimal.NewFromInt(-2)}}
	saved2, err := s.TechCards().UpdateStyleSizeChart(ctx, f.styleID, saved.LockVersion, cells, f.sizeB, negative)
	require.NoError(t, err)
	require.Equal(t, f.sizeB, saved2.GradeBaseSizeID)
	require.True(t, saved2.GradeSteps[0].Step.Equal(decimal.NewFromInt(-2)))

	// Clearing the rule leaves the expanded chart alone: the grid is the source of truth, the rule is
	// only how it was authored.
	cleared, err := s.TechCards().UpdateStyleSizeChart(ctx, f.styleID, saved2.LockVersion, cells, 0, nil)
	require.NoError(t, err)
	require.Zero(t, cleared.GradeBaseSizeID)
	require.Empty(t, cleared.GradeSteps)
	require.Len(t, cleared.Cells, 2)
}

// TestColorwayLabDipRoundsJournal covers PLM gap 1 twice over: that the development block is WRITTEN
// at all (it used to be accepted on the wire and silently dropped), and that each round is kept
// instead of being overwritten by the next one.
func TestColorwayLabDipRoundsJournal(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	s := newGapsStore(ctx, t)
	f := newGapsFixture(ctx, t, s)

	var lockVersion int
	require.NoError(t, testDB.QueryRowContext(ctx, `SELECT lock_version FROM tech_card WHERE id = ?`, f.styleID).Scan(&lockVersion))

	// The merch payload is re-sent unchanged on every call: the point under test is that the
	// development block beside it is no longer discarded.
	merch := newColorwayInsert("BLK", "black", "GAPS", f.mediaID, f.langID, f.prices)

	submit := func(round int, status entity.TechCardLabDipStatus, reason string, version int) int {
		t.Helper()
		st := status
		r := round
		rr := reason
		patch := &entity.ColorwayDevelopmentPatch{LabDipStatus: &st, LabDipRound: &r, LabDipRejectReason: &rr}
		next, err := s.Products().UpdateColorway(ctx, f.productID, version, merch, nil, nil, nil, patch)
		require.NoError(t, err)
		return next
	}

	lockVersion = submit(1, entity.LabDipRejected, "too warm", lockVersion)
	lockVersion = submit(2, entity.LabDipRejected, "still warm", lockVersion)
	_ = submit(3, entity.LabDipApproved, "", lockVersion)

	// The scalars on the colourway are the LATEST round...
	var curRound int
	var curStatus string
	require.NoError(t, testDB.QueryRowContext(ctx,
		`SELECT lab_dip_round, lab_dip_status FROM product WHERE id = ?`, f.productID).Scan(&curRound, &curStatus))
	require.Equal(t, 3, curRound)
	require.Equal(t, string(entity.LabDipApproved), curStatus)

	// ...and every earlier round survives underneath them, which is the gap this closes.
	rounds, err := s.Products().LabDipRoundsByStyleID(ctx, f.styleID)
	require.NoError(t, err)
	require.Len(t, rounds[f.productID], 3)
	require.Equal(t, 1, rounds[f.productID][0].RoundNumber)
	require.Equal(t, entity.LabDipRejected, rounds[f.productID][0].Status)
	require.Equal(t, "too warm", rounds[f.productID][0].RejectReason.String)
	require.Equal(t, 3, rounds[f.productID][2].RoundNumber)
	require.Equal(t, entity.LabDipApproved, rounds[f.productID][2].Status)

	// Re-deciding the SAME round corrects it in place rather than opening a fourth.
	var v int
	require.NoError(t, testDB.QueryRowContext(ctx, `SELECT lock_version FROM tech_card WHERE id = ?`, f.styleID).Scan(&v))
	_ = submit(3, entity.LabDipRejected, "reopened", v)
	rounds, err = s.Products().LabDipRoundsByStyleID(ctx, f.styleID)
	require.NoError(t, err)
	require.Len(t, rounds[f.productID], 3)
	require.Equal(t, entity.LabDipRejected, rounds[f.productID][2].Status)
	require.Equal(t, "reopened", rounds[f.productID][2].RejectReason.String)
}

// TestColorwayRefCarriesCostAndPrices covers PLM gaps 2 and 3: the style read now exposes each
// colourway's COGS and retail price list, so a per-colourway margin needs no second round-trip.
func TestColorwayRefCarriesCostAndPrices(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	s := newGapsStore(ctx, t)
	f := newGapsFixture(ctx, t, s)

	_, err := testDB.ExecContext(ctx,
		`UPDATE product SET cost_price = 42.50, cost_price_source = 'manual', cost_price_updated_at = NOW() WHERE id = ?`,
		f.productID)
	require.NoError(t, err)

	card, err := s.TechCards().GetTechCardById(ctx, f.styleID)
	require.NoError(t, err)
	require.NotNil(t, card)
	require.Len(t, card.Colorways, 1)

	cw := card.Colorways[0]
	require.True(t, cw.CostPrice.Valid, "the colourway ref carries its own COGS")
	require.True(t, cw.CostPrice.Decimal.Equal(decimal.RequireFromString("42.50")))
	require.Equal(t, "manual", cw.CostPriceSource.String)
	require.True(t, cw.CostPriceUpdatedAt.Valid)
	require.NotEmpty(t, cw.Prices, "the colourway ref carries its retail price list")
	for _, p := range cw.Prices {
		require.NotEmpty(t, p.Currency)
		require.True(t, p.Price.IsPositive())
	}
}

// TestTechCardListFacts covers PLM gaps 6 and 7: a list row knows its category and how many live
// colourways the style has, and the category filter matches at any level of the taxonomy.
func TestTechCardListFacts(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	s := newGapsStore(ctx, t)
	f := newGapsFixture(ctx, t, s)

	find := func(cards []entity.TechCard) *entity.TechCard {
		for i := range cards {
			if cards[i].Id == f.styleID {
				return &cards[i]
			}
		}
		return nil
	}

	cards, _, err := s.TechCards().ListTechCards(ctx, 100, 0, entity.Descending, entity.TechCardListFilter{})
	require.NoError(t, err)
	row := find(cards)
	require.NotNil(t, row, "the fixture style is in the unfiltered list")
	require.Equal(t, 1, row.ColorwayCount, "one live colourway")
	require.True(t, row.TopCategoryId.Valid, "the derived taxonomy is on the list row")

	// Filtering by the style's TOP category finds it, even though the filter is one id and the row
	// stores the whole triple — a category browser passes whichever node the operator picked.
	top := int(row.TopCategoryId.Int32)
	filtered, _, err := s.TechCards().ListTechCards(ctx, 100, 0, entity.Descending,
		entity.TechCardListFilter{CategoryIds: []int{top}})
	require.NoError(t, err)
	require.NotNil(t, find(filtered))

	// A category the style is not under excludes it rather than being ignored.
	others := queryInts(ctx, t, `SELECT id FROM category WHERE id <> `+itoa(top)+` ORDER BY id LIMIT 50`)
	require.NotEmpty(t, others)
	excluded := []int{}
	for _, id := range others {
		if id != int(row.CategoryId.Int32) && id != int(row.SubCategoryId.Int32) && id != int(row.TypeId.Int32) {
			excluded = append(excluded, id)
		}
	}
	require.NotEmpty(t, excluded)
	none, _, err := s.TechCards().ListTechCards(ctx, 100, 0, entity.Descending,
		entity.TechCardListFilter{CategoryIds: excluded})
	require.NoError(t, err)
	require.Nil(t, find(none))

	// An archived colourway stops counting: the badge means "live colourways", like every other
	// colourway read on the style.
	_, err = testDB.ExecContext(ctx, `UPDATE product SET lifecycle_status = 4 WHERE id = ?`, f.productID)
	require.NoError(t, err)
	cards, _, err = s.TechCards().ListTechCards(ctx, 100, 0, entity.Descending, entity.TechCardListFilter{})
	require.NoError(t, err)
	require.Equal(t, 0, find(cards).ColorwayCount)
}

func itoa(v int) string {
	return decimal.NewFromInt(int64(v)).String()
}
