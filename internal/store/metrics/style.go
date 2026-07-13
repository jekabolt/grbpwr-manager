package metrics

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/cache"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
	"github.com/shopspring/decimal"
)

// GetMarginByStyle rolls the per-SKU margin breakdown (breakdown.go) up to the STYLE — the
// tech card a product's primary_tech_card_id points at — so a style with several colourway
// SKUs is one row. Products with no primary card collapse into a single "no style" row
// (tech_card_id = 0). Revenue and COGS use the same order_factors apportionment (net-of-VAT,
// per-line refund, settled-vs-list scaling) as every other product breakdown; margins are
// N/A (has_cost=false) when the sold SKUs carry no cost.
func (s *Store) GetMarginByStyle(ctx context.Context, from, to time.Time, limit int) ([]entity.MarginByStyleRow, error) {
	baseCurrency := strings.ToUpper(cache.GetBaseCurrency())
	query := fmt.Sprintf(`
		WITH %s
		SELECT
			COALESCE(p.primary_tech_card_id, 0) AS tech_card_id,
			COALESCE(tc.style_number, '') AS style_number,
			COALESCE(tc.name, '') AS name,
			COALESCE(SUM(COALESCE(oi.product_price_base, pp_base.price) * (1 - COALESCE(oi.product_sale_percentage, 0) / 100.0) * oi.quantity * %s), 0) AS revenue,
			COALESCE(SUM(CASE WHEN COALESCE(oi.cost_price_at_sale, p.cost_price) IS NOT NULL THEN COALESCE(oi.product_price_base, pp_base.price) * (1 - COALESCE(oi.product_sale_percentage, 0) / 100.0) * oi.quantity * %s ELSE 0 END), 0) AS costed_revenue,
			SUM(oi.quantity) AS units_sold,
			COUNT(DISTINCT oi.product_id) AS colorway_count,
			MAX(COALESCE(oi.cost_price_at_sale, p.cost_price)) AS unit_cost,
			COALESCE(SUM(CASE WHEN COALESCE(oi.cost_price_at_sale, p.cost_price) IS NOT NULL THEN COALESCE(oi.cost_price_at_sale, p.cost_price) * oi.quantity * %s ELSE 0 END), 0) AS revenue_cost
		FROM order_item oi
		JOIN product p ON p.id = oi.product_id
		LEFT JOIN tech_card tc ON tc.id = p.primary_tech_card_id
		JOIN order_factors ofac ON ofac.order_id = oi.order_id
		LEFT JOIN product_price pp_base ON oi.product_id = pp_base.product_id AND UPPER(pp_base.currency) = UPPER(:baseCurrency)
		GROUP BY COALESCE(p.primary_tech_card_id, 0), tc.style_number, tc.name
		ORDER BY revenue DESC
		LIMIT :limit
	`, orderFactorsCTE, itemAdjExpr, itemAdjExpr, costAdjExpr)

	rows, err := storeutil.QueryListNamed[struct {
		entity.MarginByStyleRow
		CostedRevenueRaw decimal.Decimal     `db:"costed_revenue"`
		UnitCostRaw      decimal.NullDecimal `db:"unit_cost"`
		RevenueCostRaw   decimal.Decimal     `db:"revenue_cost"`
	}](ctx, s.DB, query, map[string]any{
		"baseCurrency": baseCurrency,
		"statusIds":    cache.OrderStatusIDsForNetRevenue(),
		"from":         from,
		"to":           to,
		"limit":        limit,
	})
	if err != nil {
		return nil, fmt.Errorf("get margin by style: %w", err)
	}
	result := make([]entity.MarginByStyleRow, len(rows))
	for i, r := range rows {
		row := r.MarginByStyleRow
		// Margin is computed over the COSTED SUBSET (costed_revenue − revenue_cost), not the full
		// style revenue: a style that mixes costed and uncosted colourways would otherwise credit
		// the uncosted SKUs' revenue as pure profit and overstate margin. row.Revenue keeps the full
		// style revenue for display; only the margin math uses the costed base. Same pattern as
		// GetSellThroughByDrop.
		rm := computeRowMargin(r.CostedRevenueRaw, r.UnitCostRaw, r.RevenueCostRaw)
		row.HasCost, row.UnitCost, row.RevenueCost = rm.HasCost, rm.UnitCost, rm.RevenueCost
		row.GrossMargin, row.GrossMarginPct = rm.GrossMargin, rm.GrossMarginPct
		result[i] = row
	}
	return result, nil
}

// cogsStructureRaw is the single aggregate row of the COGS-structure query: each cost article
// summed over the period in base currency, plus the unattributed remainder.
type cogsStructureRaw struct {
	Materials    decimal.Decimal `db:"materials"`
	Cmt          decimal.Decimal `db:"cmt"`
	Hardware     decimal.Decimal `db:"hardware"`
	Packaging    decimal.Decimal `db:"packaging"`
	Logistics    decimal.Decimal `db:"logistics"`
	Overhead     decimal.Decimal `db:"overhead"`
	Unattributed decimal.Decimal `db:"unattributed"`
}

// GetCogsStructure breaks the cost of goods SOLD in the period into its components. Each sold
// line's actual COGS (cost_price_at_sale × net qty, refund-adjusted via costAdj) is split by
// the proportions of that product's cost_breakdown snapshot, so the components always sum to
// the reported COGS. Units whose product has no breakdown (manual cost, or seeded before the
// column existed) collect in an "unattributed" component so coverage is visible. Returns the
// non-zero components ordered by amount descending, each with its share of total COGS.
func (s *Store) GetCogsStructure(ctx context.Context, from, to time.Time) ([]entity.CogsStructureRow, error) {
	baseCurrency := strings.ToUpper(cache.GetBaseCurrency())
	// comp_sum is the pre-defect Σ of components; the proportional share comp_i/comp_sum is
	// dimensionless (defect cancels), so cogs × share attributes the actual defect-grossed COGS.
	query := fmt.Sprintf(`
		WITH %s
		SELECT
			COALESCE(SUM(x.cogs * COALESCE(x.mat, 0)  / NULLIF(x.comp_sum, 0)), 0) AS materials,
			COALESCE(SUM(x.cogs * COALESCE(x.cmt, 0)  / NULLIF(x.comp_sum, 0)), 0) AS cmt,
			COALESCE(SUM(x.cogs * COALESCE(x.hw, 0)   / NULLIF(x.comp_sum, 0)), 0) AS hardware,
			COALESCE(SUM(x.cogs * COALESCE(x.pkg, 0)  / NULLIF(x.comp_sum, 0)), 0) AS packaging,
			COALESCE(SUM(x.cogs * COALESCE(x.logi, 0) / NULLIF(x.comp_sum, 0)), 0) AS logistics,
			COALESCE(SUM(x.cogs * COALESCE(x.ovh, 0)  / NULLIF(x.comp_sum, 0)), 0) AS overhead,
			COALESCE(SUM(CASE WHEN x.cb IS NULL OR x.comp_sum <= 0 THEN x.cogs ELSE 0 END), 0) AS unattributed
		FROM (
			SELECT
				COALESCE(oi.cost_price_at_sale, p.cost_price) * oi.quantity * %s AS cogs,
				p.cost_breakdown AS cb,
				CAST(JSON_EXTRACT(p.cost_breakdown, '$.materials') AS DECIMAL(20, 6)) AS mat,
				CAST(JSON_EXTRACT(p.cost_breakdown, '$.cmt')       AS DECIMAL(20, 6)) AS cmt,
				CAST(JSON_EXTRACT(p.cost_breakdown, '$.hardware')  AS DECIMAL(20, 6)) AS hw,
				CAST(JSON_EXTRACT(p.cost_breakdown, '$.packaging') AS DECIMAL(20, 6)) AS pkg,
				CAST(JSON_EXTRACT(p.cost_breakdown, '$.logistics') AS DECIMAL(20, 6)) AS logi,
				CAST(JSON_EXTRACT(p.cost_breakdown, '$.overhead')  AS DECIMAL(20, 6)) AS ovh,
				(COALESCE(CAST(JSON_EXTRACT(p.cost_breakdown, '$.materials') AS DECIMAL(20, 6)), 0)
				 + COALESCE(CAST(JSON_EXTRACT(p.cost_breakdown, '$.cmt')       AS DECIMAL(20, 6)), 0)
				 + COALESCE(CAST(JSON_EXTRACT(p.cost_breakdown, '$.hardware')  AS DECIMAL(20, 6)), 0)
				 + COALESCE(CAST(JSON_EXTRACT(p.cost_breakdown, '$.packaging') AS DECIMAL(20, 6)), 0)
				 + COALESCE(CAST(JSON_EXTRACT(p.cost_breakdown, '$.logistics') AS DECIMAL(20, 6)), 0)
				 + COALESCE(CAST(JSON_EXTRACT(p.cost_breakdown, '$.overhead')  AS DECIMAL(20, 6)), 0)) AS comp_sum
			FROM order_item oi
			JOIN product p ON p.id = oi.product_id
			JOIN order_factors ofac ON ofac.order_id = oi.order_id
			WHERE COALESCE(oi.cost_price_at_sale, p.cost_price) IS NOT NULL
		) x
	`, orderFactorsCTE, costAdjExpr)

	raw, err := storeutil.QueryNamedOne[cogsStructureRaw](ctx, s.DB, query, map[string]any{
		"baseCurrency": baseCurrency,
		"statusIds":    cache.OrderStatusIDsForNetRevenue(),
		"from":         from,
		"to":           to,
	})
	if err != nil {
		return nil, fmt.Errorf("get cogs structure: %w", err)
	}

	components := []entity.CogsStructureRow{
		{Component: "materials", Amount: raw.Materials.Round(2)},
		{Component: "cmt", Amount: raw.Cmt.Round(2)},
		{Component: "hardware", Amount: raw.Hardware.Round(2)},
		{Component: "packaging", Amount: raw.Packaging.Round(2)},
		{Component: "logistics", Amount: raw.Logistics.Round(2)},
		{Component: "overhead", Amount: raw.Overhead.Round(2)},
		{Component: "unattributed", Amount: raw.Unattributed.Round(2)},
	}
	total := decimal.Zero
	for _, c := range components {
		total = total.Add(c.Amount)
	}
	out := make([]entity.CogsStructureRow, 0, len(components))
	for _, c := range components {
		if c.Amount.IsZero() {
			continue
		}
		if total.IsPositive() {
			c.Pct = c.Amount.Div(total).Mul(decimal.NewFromInt(100)).Round(2).InexactFloat64()
		}
		out = append(out, c)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Amount.GreaterThan(out[j].Amount) })
	return out, nil
}
