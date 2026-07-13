package admin

import (
	"testing"

	authsrv "github.com/jekabolt/grbpwr-manager/internal/apisrv/auth"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/rbac"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/stretchr/testify/require"
	pb_decimal "google.golang.org/genproto/googleapis/type/decimal"
)

// TestCostingAccessFor pins the access decision (task 19): missing authz and super/legacy
// tokens are full access; a scoped account gets exactly what its costing grant covers.
func TestCostingAccessFor(t *testing.T) {
	scoped := func(sec string, lvl entity.AccessLevel) authsrv.AdminAuthz {
		return authsrv.AdminAuthz{Perms: map[string]entity.AccessLevel{sec: lvl}}
	}
	cases := []struct {
		name               string
		az                 authsrv.AdminAuthz
		present            bool
		wantRead, wantWrit bool
	}{
		{"missing authz → full", authsrv.AdminAuthz{}, false, true, true},
		{"super → full", authsrv.AdminAuthz{Super: true}, true, true, true},
		{"legacy → full", authsrv.AdminAuthz{Legacy: true}, true, true, true},
		{"costing:read → read only", scoped(rbac.SectionCosting, entity.AccessRead), true, true, false},
		{"costing:write → read+write", scoped(rbac.SectionCosting, entity.AccessWrite), true, true, true},
		{"no costing grant → none", scoped(rbac.SectionTechCards, entity.AccessWrite), true, false, false},
		{"empty perms → none", authsrv.AdminAuthz{Perms: map[string]entity.AccessLevel{}}, true, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			read, write := costingAccessFor(tc.az, tc.present)
			require.Equal(t, tc.wantRead, read, "read")
			require.Equal(t, tc.wantWrit, write, "write")
		})
	}
}

func dec(s string) *pb_decimal.Decimal { return &pb_decimal.Decimal{Value: s} }

// TestStripTechCardCosting verifies every money field is redacted while structure survives.
func TestStripTechCardCosting(t *testing.T) {
	tc := &pb_common.TechCard{
		TechCard: &pb_common.TechCardInsert{
			Name:    "coat",
			Costing: &pb_common.TechCardCosting{Currency: "USD"},
			BomItems: []*pb_common.TechCardBomItem{
				{Name: "wool", UnitPrice: dec("12.50"), Currency: "USD"},
			},
			Colorways: []*pb_common.TechCardColorway{
				{Name: "black", Usages: []*pb_common.TechCardColorwayUsage{
					{Placement: "outer", LineTotal: dec("3.00"), SizeRunTotal: dec("30.00")},
				}},
			},
		},
	}
	stripTechCardCosting(tc)
	require.Nil(t, tc.TechCard.Costing, "costing block removed")
	require.Equal(t, "coat", tc.TechCard.Name, "non-cost field kept")
	require.Nil(t, tc.TechCard.BomItems[0].UnitPrice, "bom price removed")
	require.Empty(t, tc.TechCard.BomItems[0].Currency, "bom currency removed")
	require.Equal(t, "wool", tc.TechCard.BomItems[0].Name, "bom name kept")
	u := tc.TechCard.Colorways[0].Usages[0]
	require.Nil(t, u.LineTotal, "usage line total removed")
	require.Nil(t, u.SizeRunTotal, "usage run total removed")
	require.Equal(t, "outer", u.Placement, "usage placement kept")

	stripTechCardCosting(nil)                    // no panic on nil
	stripTechCardCosting(&pb_common.TechCard{})  // no panic on nil inner
}

// TestStripReleaseMetaCosting clears the planned unit cost on a release header.
func TestStripReleaseMetaCosting(t *testing.T) {
	m := &pb_common.TechCardReleaseMeta{Version: "v3", UnitCost: dec("40.00"), Currency: "EUR"}
	stripReleaseMetaCosting(m)
	require.Nil(t, m.UnitCost)
	require.Empty(t, m.Currency)
	require.Equal(t, "v3", m.Version, "non-cost field kept")
	stripReleaseMetaCosting(nil) // no panic
}

// TestStripMetricsCosting redacts margin/COGS while commerce/traffic/email survive.
func TestStripMetricsCosting(t *testing.T) {
	resp := &pb_admin.GetMetricsResponse{
		Business: &pb_admin.BusinessMetrics{
			Commerce: &pb_admin.CommerceCoreMetrics{
				// ProductMetric carries cost/margin AND non-cost (revenue) — mixed row, nested deep.
				TopProductsByRevenue: []*pb_admin.ProductMetric{
					{ProductName: "coat", Value: dec("500.00"), UnitCost: dec("40.00"), GrossMargin: dec("100.00"), GrossMarginPct: 20},
				},
			},
			Margin: &pb_admin.MarginMetrics{},
		},
		MarginByStyle:      []*pb_admin.MarginByStyleRow{{}},
		CogsStructure:      []*pb_admin.CogsStructureRow{{}},
		InventoryValuation: &pb_admin.InventoryValuation{},
		// Mixed reports that the old flat strip MISSED — must keep revenue/units, redact cost/margin.
		RevenuePareto: []*pb_admin.RevenueParetoRow{
			{ProductName: "p", Revenue: dec("500.00"), UnitCost: dec("40.00"), RevenueCost: dec("200.00"), GrossMargin: dec("300.00"), GrossMarginPct: 60},
		},
		SlowMovers: []*pb_admin.SlowMoverRow{
			{ProductName: "s", Revenue: dec("50.00"), UnitCost: dec("10.00"), GrossMargin: dec("20.00"), GrossMarginPct: 40},
		},
		SellThroughByDrop: []*pb_admin.SellThroughByDropRow{
			{Collection: "SS27", Revenue: dec("300.00"), GrossMargin: dec("70.00"), GrossMarginPct: 35},
		},
	}
	stripMetricsCosting(resp)
	require.NotNil(t, resp.Business.Commerce, "commerce kept")
	require.Nil(t, resp.Business.Margin, "margin redacted")
	require.Nil(t, resp.MarginByStyle, "margin-by-style redacted")
	require.Nil(t, resp.CogsStructure, "cogs structure redacted")
	require.Nil(t, resp.InventoryValuation, "inventory valuation redacted")

	// Mixed reports: cost/margin fields cleared, non-cost fields kept (the HIGH leak the review found).
	tp := resp.Business.Commerce.TopProductsByRevenue[0]
	require.Equal(t, "coat", tp.ProductName, "product name kept")
	require.Equal(t, "500.00", tp.Value.GetValue(), "revenue kept")
	require.Nil(t, tp.UnitCost, "top-product unit_cost redacted")
	require.Nil(t, tp.GrossMargin, "top-product gross_margin redacted")
	require.Zero(t, tp.GrossMarginPct, "top-product gross_margin_pct redacted")

	rp := resp.RevenuePareto[0]
	require.Equal(t, "500.00", rp.Revenue.GetValue(), "pareto revenue kept")
	require.Nil(t, rp.UnitCost, "pareto unit_cost redacted")
	require.Nil(t, rp.RevenueCost, "pareto revenue_cost redacted")
	require.Nil(t, rp.GrossMargin, "pareto gross_margin redacted")
	require.Zero(t, rp.GrossMarginPct, "pareto gross_margin_pct redacted")

	sm := resp.SlowMovers[0]
	require.Equal(t, "50.00", sm.Revenue.GetValue(), "slow-mover revenue kept")
	require.Nil(t, sm.UnitCost, "slow-mover unit_cost redacted")
	require.Nil(t, sm.GrossMargin, "slow-mover gross_margin redacted")

	sd := resp.SellThroughByDrop[0]
	require.Equal(t, "SS27", sd.Collection, "drop label kept")
	require.Equal(t, "300.00", sd.Revenue.GetValue(), "sell-through revenue kept")
	require.Nil(t, sd.GrossMargin, "sell-through gross_margin redacted")
	require.Zero(t, sd.GrossMarginPct, "sell-through gross_margin_pct redacted")
}

// TestStripDashboardCosting redacts margins while revenue/orders survive.
func TestStripDashboardCosting(t *testing.T) {
	resp := &pb_admin.GetDashboardResponse{
		Revenue:            dec("1000.00"),
		Orders:             12,
		GrossMargin:        dec("400.00"),
		GrossMarginPct:     40,
		ContributionMargin: dec("300.00"),
		TopByMargin:        []*pb_admin.ProductMetric{{}},
		OperatingResult:    dec("100.00"),
		OpexTotal:          dec("150.00"),
		MarketingSpend:     dec("50.00"),
		OpexCaveat:         "no opex",
	}
	stripDashboardCosting(resp)
	require.Equal(t, "1000.00", resp.Revenue.GetValue(), "revenue kept")
	require.EqualValues(t, 12, resp.Orders, "orders kept")
	require.Nil(t, resp.GrossMargin, "gross margin redacted")
	require.Zero(t, resp.GrossMarginPct, "gross margin pct redacted")
	require.Nil(t, resp.ContributionMargin, "contribution margin redacted")
	require.Nil(t, resp.TopByMargin, "top-by-margin redacted")
	require.Nil(t, resp.OperatingResult, "operating result redacted")
	require.Nil(t, resp.OpexTotal, "opex total redacted")
	require.Nil(t, resp.MarketingSpend, "marketing spend redacted")
	require.Empty(t, resp.OpexCaveat, "opex caveat redacted")
}

// TestStripStyleEconomicsCosting redacts cost/margin from a style-economics card while identity,
// revenue, units, fitting rounds and production quantities survive.
func TestStripStyleEconomicsCosting(t *testing.T) {
	resp := &pb_admin.GetStyleEconomicsResponse{
		Economics: &pb_admin.StyleEconomics{
			TechCardId:    7,
			StyleNumber:   "S-1",
			Name:          "coat",
			FittingRounds: 3,
			Sales: &pb_admin.MarginByStyleRow{
				Name: "coat", Revenue: dec("200.00"), UnitsSold: 2, ColorwayCount: 2,
				UnitCost: dec("10.00"), RevenueCost: dec("20.00"), GrossMargin: dec("180.00"), GrossMarginPct: 90, HasCost: true,
			},
			DevCost:     &pb_common.TechCardDevCostSummary{TotalBase: dec("50.00")},
			Production:  &pb_admin.StyleProductionSummary{Runs: 2, PlannedQtyTotal: 30, ReceivedQtyTotal: 8, PlannedCostBase: dec("300.00"), ActualCostBase: dec("330.00"), CostVariance: dec("30.00"), HasActuals: true},
			NetAfterDev: dec("130.00"),
		},
	}
	stripStyleEconomicsCosting(resp)
	e := resp.Economics
	// Non-cost kept.
	require.Equal(t, int32(7), e.TechCardId)
	require.Equal(t, "coat", e.Name)
	require.EqualValues(t, 3, e.FittingRounds)
	require.Equal(t, "200.00", e.Sales.Revenue.GetValue(), "sales revenue kept")
	require.EqualValues(t, 2, e.Sales.UnitsSold, "units kept")
	require.EqualValues(t, 2, e.Sales.ColorwayCount, "colourway count kept")
	require.NotNil(t, e.Production, "production overview kept")
	require.EqualValues(t, 30, e.Production.PlannedQtyTotal, "planned qty kept")
	require.EqualValues(t, 8, e.Production.ReceivedQtyTotal, "received qty kept")
	// Cost/margin redacted.
	require.Nil(t, e.DevCost, "dev cost redacted")
	require.Nil(t, e.NetAfterDev, "net result redacted")
	require.Nil(t, e.Sales.UnitCost, "sales unit_cost redacted")
	require.Nil(t, e.Sales.GrossMargin, "sales gross_margin redacted")
	require.Zero(t, e.Sales.GrossMarginPct, "sales gross_margin_pct redacted")
	require.Nil(t, e.Production.PlannedCostBase, "production planned cost redacted")
	require.Nil(t, e.Production.ActualCostBase, "production actual cost redacted")
	require.Nil(t, e.Production.CostVariance, "production variance redacted")
	require.False(t, e.Production.HasActuals, "production has_actuals cleared")

	stripStyleEconomicsCosting(nil)                                   // no panic
	stripStyleEconomicsCosting(&pb_admin.GetStyleEconomicsResponse{}) // no panic on nil economics
}

// TestCostingWriteDetectors pin the write-gate predicates.
func TestCostingWriteDetectors(t *testing.T) {
	require.False(t, techCardInsertHasCostingData(nil))
	require.False(t, techCardInsertHasCostingData(&pb_common.TechCardInsert{Name: "x"}))
	require.True(t, techCardInsertHasCostingData(&pb_common.TechCardInsert{Costing: &pb_common.TechCardCosting{}}))
	require.True(t, techCardInsertHasCostingData(&pb_common.TechCardInsert{
		BomItems: []*pb_common.TechCardBomItem{{Name: "wool", UnitPrice: dec("1.00")}},
	}), "a BOM purchase price is cost data")
	require.False(t, techCardInsertHasCostingData(&pb_common.TechCardInsert{
		BomItems: []*pb_common.TechCardBomItem{{Name: "wool"}},
	}), "a priceless BOM line is not cost data")

	require.False(t, productInsertHasCostPrice(nil))
	require.False(t, productInsertHasCostPrice(&pb_common.ProductInsert{}))
	require.False(t, productInsertHasCostPrice(&pb_common.ProductInsert{CostPrice: dec("")}), "empty = leave unchanged")
	require.True(t, productInsertHasCostPrice(&pb_common.ProductInsert{CostPrice: dec("9.99")}))
}
