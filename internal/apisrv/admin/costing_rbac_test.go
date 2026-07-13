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
			Commerce: &pb_admin.CommerceCoreMetrics{},
			Margin:   &pb_admin.MarginMetrics{},
		},
		MarginByStyle:      []*pb_admin.MarginByStyleRow{{}},
		CogsStructure:      []*pb_admin.CogsStructureRow{{}},
		InventoryValuation: &pb_admin.InventoryValuation{},
	}
	stripMetricsCosting(resp)
	require.NotNil(t, resp.Business.Commerce, "commerce kept")
	require.Nil(t, resp.Business.Margin, "margin redacted")
	require.Nil(t, resp.MarginByStyle, "margin-by-style redacted")
	require.Nil(t, resp.CogsStructure, "cogs structure redacted")
	require.Nil(t, resp.InventoryValuation, "inventory valuation redacted")
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
