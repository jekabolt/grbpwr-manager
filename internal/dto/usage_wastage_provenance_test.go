package dto

import (
	"database/sql"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
	pb_decimal "google.golang.org/genproto/googleapis/type/decimal"
)

func provUsage(mut func(*pb_common.TechCardColorwayUsage)) []*pb_common.TechCardColorwayUsage {
	u := &pb_common.TechCardColorwayUsage{
		BomLineKey:  "RK1",
		Consumption: &pb_decimal.Decimal{Value: "1.5"},
	}
	if mut != nil {
		mut(u)
	}
	return []*pb_common.TechCardColorwayUsage{u}
}

func strPtr(s string) *string { return &s }

// The provenance triple's write protocol (Ф9.4): absent = stale client, preserve; present =
// normalised and validated; pcts only travel with marker.
func TestParseRecipeUsagesProvenance(t *testing.T) {
	t.Run("absent stays invalid for the store to carry forward", func(t *testing.T) {
		out, err := ParseRecipeUsages(provUsage(nil))
		require.NoError(t, err)
		require.False(t, out[0].ConsumptionSource.Valid)
	})
	t.Run("empty normalises to manual", func(t *testing.T) {
		out, err := ParseRecipeUsages(provUsage(func(u *pb_common.TechCardColorwayUsage) {
			u.ConsumptionSource = strPtr("  ")
		}))
		require.NoError(t, err)
		require.Equal(t, sql.NullString{String: entity.ConsumptionSourceManual, Valid: true}, out[0].ConsumptionSource)
	})
	t.Run("marker carries rounded pcts", func(t *testing.T) {
		out, err := ParseRecipeUsages(provUsage(func(u *pb_common.TechCardColorwayUsage) {
			u.ConsumptionSource = strPtr("marker")
			u.WasteSelvedgePct = &pb_decimal.Decimal{Value: "1.6512"}
			u.WasteCutPct = &pb_decimal.Decimal{Value: "21.949"}
		}))
		require.NoError(t, err)
		require.Equal(t, "1.65", out[0].WasteSelvedgePct.Decimal.String())
		require.Equal(t, "21.95", out[0].WasteCutPct.Decimal.String())
	})
	t.Run("manual with pcts is a provenance mismatch", func(t *testing.T) {
		_, err := ParseRecipeUsages(provUsage(func(u *pb_common.TechCardColorwayUsage) {
			u.ConsumptionSource = strPtr("manual")
			u.WasteCutPct = &pb_decimal.Decimal{Value: "5"}
		}))
		require.Error(t, err)
	})
	t.Run("unknown source rejected", func(t *testing.T) {
		_, err := ParseRecipeUsages(provUsage(func(u *pb_common.TechCardColorwayUsage) {
			u.ConsumptionSource = strPtr("estimate")
		}))
		require.Error(t, err)
	})
	t.Run("above 100 pct is accepted", func(t *testing.T) {
		// A раскладка under 50% efficiency wastes more cloth than it turns into pieces, and the
		// inter-piece component (1/eff − 1, of the piece area) then exceeds 100. It is a real
		// marker, not bad input — the pre-0263 bound rejected the whole recipe save on it.
		out, err := ParseRecipeUsages(provUsage(func(u *pb_common.TechCardColorwayUsage) {
			u.ConsumptionSource = strPtr("marker")
			u.WasteCutPct = &pb_decimal.Decimal{Value: "122.22"}
		}))
		require.NoError(t, err)
		require.Equal(t, "122.22", out[0].WasteCutPct.Decimal.String())
	})
	t.Run("pct out of range rejected", func(t *testing.T) {
		_, err := ParseRecipeUsages(provUsage(func(u *pb_common.TechCardColorwayUsage) {
			u.ConsumptionSource = strPtr("marker")
			u.WasteCutPct = &pb_decimal.Decimal{Value: "1001"}
		}))
		require.Error(t, err)
	})
	t.Run("negative pct rejected", func(t *testing.T) {
		_, err := ParseRecipeUsages(provUsage(func(u *pb_common.TechCardColorwayUsage) {
			u.ConsumptionSource = strPtr("marker")
			u.WasteSelvedgePct = &pb_decimal.Decimal{Value: "-0.01"}
		}))
		require.Error(t, err)
	})
}

// The gross-up skip at the entity layer — the authoritative proof that a marker-sourced norm's
// cost is consumption × price with NO wastage factor, while manual rows keep today's math
// bit-identically. Every higher read path (estimate, plan, run override) reduces to this rule.
func TestUsageWastageGrossUpSkip(t *testing.T) {
	bom := &entity.TechCardBomItem{
		UnitPrice:      decimal.NewNullDecimal(decimal.RequireFromString("10")),
		WastagePercent: decimal.NewNullDecimal(decimal.RequireFromString("8")),
	}
	base := entity.TechCardColorwayUsage{
		Consumption: decimal.NewNullDecimal(decimal.RequireFromString("2.5")),
	}

	manual := base
	manual.ConsumptionSource = sql.NullString{String: entity.ConsumptionSourceManual, Valid: true}
	got := manual.LineTotal(bom, nil)
	require.True(t, got.Valid)
	require.Equal(t, "27", got.Decimal.String(), "manual keeps the 8%% gross-up: 2.5×10×1.08")

	marker := base
	marker.ConsumptionSource = sql.NullString{String: entity.ConsumptionSourceMarker, Valid: true}
	got = marker.LineTotal(bom, nil)
	require.True(t, got.Valid)
	require.Equal(t, "25", got.Decimal.String(), "marker is never grossed: 2.5×10")

	// Per-size run totals obey the same rule.
	perSize := entity.TechCardColorwayUsage{
		ConsumptionSource: sql.NullString{String: entity.ConsumptionSourceMarker, Valid: true},
		SizeConsumptions: []entity.TechCardBomSizeConsumption{
			{SizeId: 1, Consumption: decimal.RequireFromString("2")},
		},
	}
	rt := perSize.SizeRunTotal(bom, map[int]int{1: 10})
	require.True(t, rt.Valid)
	require.Equal(t, "200", rt.Decimal.String(), "marker per-size: 2×10×10, no gross-up")

	perSize.ConsumptionSource = sql.NullString{}
	rt = perSize.SizeRunTotal(bom, map[int]int{1: 10})
	require.Equal(t, "216", rt.Decimal.String(), "unset provenance behaves as manual: ×1.08")
}
