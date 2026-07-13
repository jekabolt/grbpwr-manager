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

// TestMaterialCatalog exercises the task-10 material catalog + append-only price history against
// a real MySQL: create/get/update/archive, the current-price join (latest valid_from <= today,
// ignoring future-dated rows), and the full history read. Cleans up via ON DELETE CASCADE.
func TestMaterialCatalog(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cfg := *testCfg
	cfg.Automigrate = true
	s, err := NewForTest(ctx, cfg)
	require.NoError(t, err)
	defer s.Close()

	T := s.TechCards()
	d := func(v string) decimal.Decimal { return decimal.RequireFromString(v) }
	nd := func(v string) decimal.NullDecimal { return decimal.NullDecimal{Decimal: d(v), Valid: true} }
	ns := func(v string) sql.NullString { return sql.NullString{String: v, Valid: true} }
	day := func(y int, m time.Month, dd int) time.Time { return time.Date(y, m, dd, 0, 0, 0, 0, time.UTC) }

	var matID int
	defer func() {
		if matID != 0 {
			_, _ = testDB.ExecContext(ctx, "DELETE FROM material WHERE id = ?", matID) // material_price cascades
		}
	}()

	// create
	matID, err = T.CreateMaterial(ctx, &entity.MaterialInsert{
		Name: "Wool 300gsm", Section: "fabric", Supplier: ns("MillCo"), FabricWeightGsm: nd("300"),
	})
	require.NoError(t, err)
	require.Greater(t, matID, 0)

	// get — no price yet
	m, err := T.GetMaterial(ctx, matID)
	require.NoError(t, err)
	require.Equal(t, "Wool 300gsm", m.Name)
	require.Equal(t, "fabric", m.Section)
	require.Nil(t, m.LatestPrice, "no price history yet")

	// price history: two effective points + one future (must be ignored by "current")
	require.NoError(t, T.AddMaterialPrice(ctx, entity.MaterialPrice{MaterialId: matID, Price: d("12.50"), Currency: "EUR", ValidFrom: day(2026, 1, 1)}))
	require.NoError(t, T.AddMaterialPrice(ctx, entity.MaterialPrice{MaterialId: matID, Price: d("14.00"), Currency: "EUR", ValidFrom: day(2026, 6, 1)}))
	require.NoError(t, T.AddMaterialPrice(ctx, entity.MaterialPrice{MaterialId: matID, Price: d("99.00"), Currency: "EUR", ValidFrom: day(2099, 1, 1)}))

	m, err = T.GetMaterial(ctx, matID)
	require.NoError(t, err)
	require.NotNil(t, m.LatestPrice)
	require.True(t, m.LatestPrice.Price.Equal(d("14.00")), "current price is the latest effective, not the future one: got %s", m.LatestPrice.Price)

	// same-day correction upserts (not duplicates)
	require.NoError(t, T.AddMaterialPrice(ctx, entity.MaterialPrice{MaterialId: matID, Price: d("14.25"), Currency: "EUR", ValidFrom: day(2026, 6, 1)}))
	hist, err := T.ListMaterialPrices(ctx, matID)
	require.NoError(t, err)
	require.Len(t, hist, 3, "same (date,currency) upserts rather than appends")
	require.True(t, hist[0].ValidFrom.Equal(day(2099, 1, 1)), "history is newest-first")

	// list by section (current price attached)
	list, err := T.ListMaterials(ctx, "fabric", false)
	require.NoError(t, err)
	found := false
	for _, mm := range list {
		if mm.Id == matID {
			found = true
			require.NotNil(t, mm.LatestPrice)
			require.True(t, mm.LatestPrice.Price.Equal(d("14.25")))
		}
	}
	require.True(t, found, "material present in section list")

	// update descriptive fields
	require.NoError(t, T.UpdateMaterial(ctx, matID, &entity.MaterialInsert{Name: "Wool 320gsm", Section: "fabric"}))
	m, _ = T.GetMaterial(ctx, matID)
	require.Equal(t, "Wool 320gsm", m.Name)

	// archive removes it from the default list but not the include-archived list
	require.NoError(t, T.ArchiveMaterial(ctx, matID, true))
	list, _ = T.ListMaterials(ctx, "fabric", false)
	require.False(t, containsMaterial(list, matID), "archived excluded by default")
	list, _ = T.ListMaterials(ctx, "fabric", true)
	require.True(t, containsMaterial(list, matID), "archived included when requested")
}

func containsMaterial(list []entity.MaterialWithPrice, id int) bool {
	for _, m := range list {
		if m.Id == id {
			return true
		}
	}
	return false
}
