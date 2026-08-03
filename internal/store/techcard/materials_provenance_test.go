package techcard

import (
	"database/sql"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

// TestBomPriceProvenance pins the save-path provenance rules (Phase 3, plan 11): a new or edited
// price is 'manual' as of now; an unchanged price keeps its stored provenance — including the
// reprice action's 'catalog' stamp and the honest NULL of a pre-provenance row; a cleared price
// clears the provenance with it. The one subtle rule worth pinning hard: a save that merely
// round-trips a catalog-stamped price must NOT rewrite its history to 'manual'.
func TestBomPriceProvenance(t *testing.T) {
	now := time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)
	then := time.Date(2026, 8, 1, 9, 0, 0, 0, time.UTC)
	d := func(s string) decimal.NullDecimal {
		return decimal.NullDecimal{Decimal: decimal.RequireFromString(s), Valid: true}
	}
	line := func(price decimal.NullDecimal, cur string) *entity.TechCardBomItem {
		return &entity.TechCardBomItem{UnitPrice: price, Currency: sql.NullString{String: cur, Valid: cur != ""}}
	}
	existing := func(price decimal.NullDecimal, cur, src string, at time.Time) *bomExistingRow {
		r := &bomExistingRow{UnitPrice: price, Currency: sql.NullString{String: cur, Valid: cur != ""}}
		if src != "" {
			r.PriceSource = sql.NullString{String: src, Valid: true}
			r.PriceSnapshotAt = sql.NullTime{Time: at, Valid: true}
		}
		return r
	}

	t.Run("new priced line is manual now", func(t *testing.T) {
		src, at := bomPriceProvenance(nil, line(d("5.00"), "EUR"), now)
		require.Equal(t, entity.BomPriceSourceManual, src.String)
		require.Equal(t, now, at.Time)
	})

	t.Run("unpriced line carries no provenance", func(t *testing.T) {
		src, at := bomPriceProvenance(nil, line(decimal.NullDecimal{}, ""), now)
		require.False(t, src.Valid)
		require.False(t, at.Valid)
	})

	t.Run("cleared price clears provenance", func(t *testing.T) {
		src, at := bomPriceProvenance(existing(d("5.00"), "EUR", "catalog", then), line(decimal.NullDecimal{}, ""), now)
		require.False(t, src.Valid)
		require.False(t, at.Valid)
	})

	t.Run("round-tripped catalog price keeps its catalog history", func(t *testing.T) {
		src, at := bomPriceProvenance(existing(d("5.00"), "EUR", "catalog", then), line(d("5.00"), "EUR"), now)
		require.Equal(t, entity.BomPriceSourceCatalog, src.String)
		require.Equal(t, then, at.Time)
	})

	t.Run("round-tripped pre-provenance price stays honestly unknown", func(t *testing.T) {
		src, at := bomPriceProvenance(existing(d("5.00"), "EUR", "", time.Time{}), line(d("5.00"), "EUR"), now)
		require.False(t, src.Valid)
		require.False(t, at.Valid)
	})

	t.Run("edited price restamps manual", func(t *testing.T) {
		src, at := bomPriceProvenance(existing(d("5.00"), "EUR", "catalog", then), line(d("6.00"), "EUR"), now)
		require.Equal(t, entity.BomPriceSourceManual, src.String)
		require.Equal(t, now, at.Time)
	})

	t.Run("changed currency alone restamps manual", func(t *testing.T) {
		src, _ := bomPriceProvenance(existing(d("5.00"), "EUR", "catalog", then), line(d("5.00"), "USD"), now)
		require.Equal(t, entity.BomPriceSourceManual, src.String)
	})

	t.Run("equal value different scale is not an edit", func(t *testing.T) {
		// decimal.Equal(5.00, 5.0000) — DECIMAL(12,4) round-trips with trailing zeros the client
		// payload may not carry; scale alone must not rewrite provenance.
		src, at := bomPriceProvenance(existing(d("5.0000"), "EUR", "catalog", then), line(d("5.00"), "EUR"), now)
		require.Equal(t, entity.BomPriceSourceCatalog, src.String)
		require.Equal(t, then, at.Time)
	})
}
