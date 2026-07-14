package admin

import (
	"context"
	"database/sql"
	"log/slog"
	"strings"

	authsrv "github.com/jekabolt/grbpwr-manager/internal/apisrv/auth"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/rbac"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/shopspring/decimal"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// Costing (task 19) is confidential: purchase prices, CMT/overhead, and the margins
// derived from them. RBAC protects it by SHAPING responses rather than gating whole
// methods — a content manager keeps tech_cards:read for sketches/sizes without seeing
// money. The helpers below decide access and the strip* functions redact the fields;
// they are applied by the read handlers (GetTechCard, GetProductByID, GetMetrics,
// GetDashboard, tech-card releases) and enforced on writes (UpsertProduct cost_price,
// Create/UpdateTechCard costing block, SyncProductCostFromTechCard).

// costingAccessFor is the pure access decision, split out so it is unit-testable
// without a request context. present is whether an AdminAuthz was found in context.
//
// A missing authz (present=false) means the call did not pass the RBAC interceptor —
// an internal/test call, never a real scoped account (the interceptor populates authz
// for every admin RPC in production) — so it is treated as full access, matching how a
// pre-RBAC legacy token is treated. Super/legacy tokens are full access. A scoped
// account gets exactly what its costing grant covers; no grant → no cost access.
func costingAccessFor(az authsrv.AdminAuthz, present bool) (read, write bool) {
	if !present || az.FullAccess() {
		return true, true
	}
	lvl, ok := az.Perms[rbac.SectionCosting]
	if !ok {
		return false, false
	}
	return lvl.Covers(entity.AccessRead), lvl.Covers(entity.AccessWrite)
}

// costingAccess resolves the caller's costing read/write access from context.
func (s *Server) costingAccess(ctx context.Context) (read, write bool) {
	az, ok := authsrv.GetAdminAuthz(ctx)
	return costingAccessFor(az, ok)
}

// stripTechCardCosting redacts every confidential cost field from a resolved tech card:
// the whole costing block, per-BOM-line purchase prices, and the derived per-usage line
// totals. Structure the account is allowed to see (articles, placements, consumption)
// stays; only money is removed. Safe on nil.
func stripTechCardCosting(tc *pb_common.TechCard) {
	if tc == nil || tc.TechCard == nil {
		return
	}
	ins := tc.TechCard
	ins.Costing = nil
	for _, b := range ins.BomItems {
		if b == nil {
			continue
		}
		b.UnitPrice = nil
		b.Currency = ""
	}
	for _, cw := range ins.Colorways {
		if cw == nil {
			continue
		}
		for _, u := range cw.Usages {
			if u == nil {
				continue
			}
			u.LineTotal = nil
			u.SizeRunTotal = nil
		}
	}
}

// stripMaterialCosting clears a catalog material's current price. The material's descriptive
// identity (name, section, supplier, spec) stays; only the money goes. Safe on nil.
func stripMaterialCosting(m *pb_common.Material) {
	if m == nil {
		return
	}
	m.LatestPrice = nil
}

// stripMaterialMovementCosting clears the confidential money on a stock-ledger row (unit costs +
// currency). Quantities and on-hand balances stay — a warehouse role sees "how many metres" but
// not "at what value" (new-flow NF-01). Safe on nil.
func stripMaterialMovementCosting(m *pb_common.MaterialMovement) {
	if m == nil {
		return
	}
	m.UnitCost = nil
	m.UnitCostBase = nil
	m.Currency = ""
}

// stripMaterialStockCosting clears the moving-average cost on a stock balance, keeping on-hand.
func stripMaterialStockCosting(st *pb_common.MaterialStock) {
	if st == nil {
		return
	}
	st.AvgUnitCostBase = nil
}

// stripMaterialStockRowCosting clears the valuation on a warehouse list row (average, stock value,
// and the nested material's confidential price), keeping quantity, min-stock and the low-stock flag.
func stripMaterialStockRowCosting(r *pb_common.MaterialStockRow) {
	if r == nil {
		return
	}
	r.AvgUnitCostBase = nil
	r.StockValueBase = nil
	stripMaterialCosting(r.Material)
}

// stripReleaseMetaCosting clears the planned unit cost on a release header. Safe on nil.
func stripReleaseMetaCosting(m *pb_common.TechCardReleaseMeta) {
	if m == nil {
		return
	}
	m.UnitCost = nil
	m.Currency = ""
}

// costingRedactedFieldNames are proto field names carrying confidential COGS/margin. They are
// cleared RECURSIVELY from GetMetrics/GetDashboard responses — a denylist, not a hand-maintained
// per-report list, because the flat strip missed several reports that also carry these fields
// (revenue pareto, slow movers, sell-through-by-drop, commerce top-products), and any future
// report with one of these fields is now redacted automatically. Scoped to the metrics/dashboard
// responses, where these names are ALWAYS confidential (production-run `unit_cost`, which shares a
// name but is gated by the production section, is handled separately). `has_cost` (a provenance
// bool) and shipping costs (`avg/total_shipping_cost`, not COGS) are deliberately NOT listed.
var costingRedactedFieldNames = map[string]bool{
	"unit_cost":           true,
	"revenue_cost":        true,
	"gross_margin":        true,
	"gross_margin_pct":    true,
	"contribution_margin": true,
	"operating_result":    true,
	"opex_total":          true,
	"marketing_spend":     true,
	"opex_caveat":         true,
	// analytics-v2 task 07: acquisition economics derived from confidential media spend. `ltv`
	// (revenue-side) and the discount/refund fields (already visible in the commerce section) are
	// deliberately NOT redacted — only cost-derived figures are.
	"cpo":                       true,
	"blended_cac":               true,
	"ltv_cac_ratio":             true,
	"fulfilment_cost_per_order": true,
	// analytics-v2 task 08: per-country cost/margin. `revenue_cost`, `gross_margin`,
	// `gross_margin_pct`, `contribution_margin` are already covered above; add the two names unique to
	// the country row. `shipping_cost` stays visible (precedent: avg/total_shipping_cost are logistics,
	// not COGS) and `ltv_avg` is revenue-side.
	"profit_per_order": true,
	"payment_fees":     true,
	// Dashboard period-over-period comparison (DashboardComparison): the value fields reuse the
	// names above and are cleared by them, but the DERIVED margin-change fields have distinct names
	// and would otherwise leak the margin trend (direction + magnitude) with the value redacted.
	"gross_margin_change_pct":        true,
	"gross_margin_pct_change_pp":     true,
	"contribution_margin_change_pct": true,
	"operating_result_change_pct":    true,
}

// redactCostingFieldsDeep clears every confidential cost/margin field (by name) anywhere in the
// message tree, leaving all non-cost fields (revenue, units, sell-through, has_cost, …) intact.
func redactCostingFieldsDeep(m protoreflect.Message) {
	if m == nil || !m.IsValid() {
		return
	}
	m.Range(func(fd protoreflect.FieldDescriptor, v protoreflect.Value) bool {
		if costingRedactedFieldNames[string(fd.Name())] {
			m.Clear(fd)
			return true
		}
		switch {
		case fd.IsMap():
			// no confidential maps in these responses
		case fd.IsList() && fd.Kind() == protoreflect.MessageKind:
			l := v.List()
			for i := 0; i < l.Len(); i++ {
				redactCostingFieldsDeep(l.Get(i).Message())
			}
		case fd.Kind() == protoreflect.MessageKind:
			redactCostingFieldsDeep(v.Message())
		}
		return true
	})
}

// stripMetricsCosting redacts the cost/margin content of a GetMetrics response, leaving commerce,
// traffic and email intact. Applied when the caller lacks costing:read.
func stripMetricsCosting(resp *pb_admin.GetMetricsResponse) {
	if resp == nil {
		return
	}
	// Nil the cost-CENTRIC reports whole: they exist only to show cost/margin, so even their
	// presence and row ordering (e.g. styles ranked by margin) leaks. Field-level redaction below
	// handles the MIXED reports (revenue pareto, slow movers, top products, …) that also carry
	// useful non-cost data worth keeping.
	if resp.Business != nil {
		resp.Business.Margin = nil
	}
	resp.MarginByStyle = nil
	resp.CogsStructure = nil
	resp.InventoryValuation = nil
	redactCostingFieldsDeep(resp.ProtoReflect())
}

// stripDashboardCosting redacts the margin and operating-result figures of the decision dashboard,
// keeping revenue, order count, inventory action lists and alerts. Applied without costing:read.
func stripDashboardCosting(resp *pb_admin.GetDashboardResponse) {
	if resp == nil {
		return
	}
	resp.TopByMargin = nil // products ranked by gross margin € — the ordering itself leaks
	redactCostingFieldsDeep(resp.ProtoReflect())
}

// stripStyleEconomicsCosting redacts confidential cost/margin from a style-economics card for an
// account without costing:read (task 19): the dev-cost roll-up, production costs, the net result,
// and the cost/margin fields on the sales row. Identity, revenue, units, colourway count, fitting
// rounds and production quantities remain — the non-cost half of the business case still shows.
func stripStyleEconomicsCosting(resp *pb_admin.GetStyleEconomicsResponse) {
	if resp == nil || resp.Economics == nil {
		return
	}
	e := resp.Economics
	e.DevCost = nil         // whole R&D journal roll-up is money
	e.NetAfterDev = nil     // derived from margin
	e.SamplesCostBase = nil // NF-09: warehouse cost the samples consumed (samples_count is kept)
	if e.Production != nil {
		e.Production.PlannedCostBase = nil
		e.Production.ActualCostBase = nil
		e.Production.CostVariance = nil
		e.Production.MaterialsFromStockBase = nil // NF-09 material actuals are cost data
		e.Production.HasActuals = false
	}
	if e.Sales != nil {
		// Clears unit_cost/revenue_cost/gross_margin/gross_margin_pct; keeps revenue/units/has_cost.
		redactCostingFieldsDeep(e.Sales.ProtoReflect())
	}
}

// techCardInsertHasCostingData reports whether a write payload carries confidential cost
// input: a costing block, or a BOM line with a purchase price. Used to reject a write
// from an account without costing:write instead of silently accepting cost changes.
func techCardInsertHasCostingData(ins *pb_common.TechCardInsert) bool {
	if ins == nil {
		return false
	}
	if ins.Costing != nil {
		return true
	}
	for _, b := range ins.BomItems {
		if b != nil && b.UnitPrice != nil {
			return true
		}
	}
	return false
}

// productInsertHasCostPrice reports whether an UpsertProduct payload is trying to SET a
// product cost_price (a confidential figure). An absent/empty value means "leave the stored
// cost unchanged" (see nullDecimalFromPb) — not a cost write — so it is not gated. Because
// cost_price is write-only (never serialized on the product read path), a cost-stripped
// account's resave omits it and preserves the stored value: no anti-erase logic needed here.
func productInsertHasCostPrice(p *pb_common.ProductInsert) bool {
	return strings.TrimSpace(p.GetCostPrice().GetValue()) != ""
}

// preserveStoredCosting restores confidential cost fields onto an incoming tech-card update
// from the stored card, so a full-replace save by an account WITHOUT costing:write (whose read
// was cost-stripped, so the payload carries no costing) cannot blank the costing block or BOM
// purchase prices it never saw. The costing block is preserved wholesale; BOM prices are matched
// back by the article's natural key (section+name+supplier_ref), so an unchanged line keeps its
// price and a genuinely new line simply has none. Best-effort: a reload failure leaves the payload
// as-is (the write still proceeds) — logged, never fatal. Only call this after confirming the
// payload carries no costing data (techCardInsertHasCostingData is false), i.e. the caller isn't
// trying to set costs — this path is purely anti-erase, not a way to smuggle changes.
func (s *Server) preserveStoredCosting(ctx context.Context, techCardID int, incoming *entity.TechCardInsert) {
	stored, err := s.repo.TechCards().GetTechCardById(ctx, techCardID)
	if err != nil || stored == nil {
		if err != nil {
			slog.Default().WarnContext(ctx, "costing preserve: can't reload stored tech card; leaving payload as-is",
				slog.Int("tech_card_id", techCardID), slog.String("err", err.Error()))
		}
		return
	}
	incoming.Costing = stored.Costing
	if len(stored.BomItems) == 0 || len(incoming.BomItems) == 0 {
		return
	}
	type bomPrice struct {
		unit decimal.NullDecimal
		cur  sql.NullString
	}
	// Natural keys are not guaranteed unique (a card may carry two lines with the same
	// section+name+supplier_ref — e.g. the same fabric in two colours). Keep a FIFO queue per key
	// and consume it in order so N stored lines feed N incoming lines with that key, instead of a
	// last-wins map that would stamp every colliding line with a single price.
	byKey := make(map[string][]bomPrice, len(stored.BomItems))
	for _, b := range stored.BomItems {
		k := bomNaturalKey(b)
		byKey[k] = append(byKey[k], bomPrice{b.UnitPrice, b.Currency})
	}
	for i := range incoming.BomItems {
		k := bomNaturalKey(incoming.BomItems[i])
		q := byKey[k]
		if len(q) == 0 {
			continue // a genuinely new line (or more duplicates than stored) simply has no price
		}
		incoming.BomItems[i].UnitPrice = q[0].unit
		incoming.BomItems[i].Currency = q[0].cur
		byKey[k] = q[1:]
	}
}

// bomNaturalKey identifies a BOM article across a full-replace by its human identity
// (section + name + supplier ref), case/space-insensitive, so a preserved price re-attaches
// to the same line even though row ids are reassigned on replace.
func bomNaturalKey(b entity.TechCardBomItem) string {
	norm := func(s string) string { return strings.ToLower(strings.TrimSpace(s)) }
	return norm(string(b.Section)) + "\x1f" + norm(b.Name) + "\x1f" + norm(b.SupplierRef.String)
}
