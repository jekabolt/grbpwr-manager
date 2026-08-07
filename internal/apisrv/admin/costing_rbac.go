package admin

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"strings"

	authsrv "github.com/jekabolt/grbpwr-manager/internal/apisrv/auth"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/rbac"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	pb_decimal "google.golang.org/genproto/googleapis/type/decimal"
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
// A missing authz (present=false) means the call did not pass the RBAC interceptor, and it FAILS
// CLOSED: no cost access. The interceptor populates authz for every admin RPC, so in production
// nothing reaches these handlers without it — the case is unreachable rather than benign, and
// treating it as full access (as it used to) meant any future path that skipped the interceptor —
// an in-process caller, a background job reusing a handler — would silently publish costing.
// Super/legacy tokens are full access. A scoped account gets exactly what its costing grant covers;
// no grant → no cost access.
func costingAccessFor(az authsrv.AdminAuthz, present bool) (read, write bool) {
	if !present {
		return false, false
	}
	if az.FullAccess() {
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
	// H1 fix: colourway refs now carry their recipe (Usages), each with a derived per-line
	// line_total/size_run_total — money computed from the (now redacted) BOM unit_price. Strip it the
	// same way GetColorwayByID does for the same message type.
	for _, c := range tc.Colorways {
		if c == nil {
			continue
		}
		stripTechCardColorwayUsageCosting(c.Usages)
		// The colourway ref now also carries its own COGS and its retail price list. Both are money
		// and both go: cost_price is confidential by the same rule as BOM unit prices, and the retail
		// list is only on the ref so a margin can be drawn from it — which is exactly the figure
		// costing:read gates. The lab-dip state next to them is not money and stays.
		c.CostPrice = nil
		c.CostPriceSource = ""
		c.CostPriceUpdatedAt = nil
		c.Prices = nil
		c.NetPrices = nil
	}
}

// stripTechCardColorwayUsageCosting clears the derived per-line money (line_total/size_run_total) on
// a colourway recipe for an account without costing:read, keeping the structure (article reference,
// placement, colour, consumption/quantity) — the same "structure stays, money goes" shape as
// stripStyleCostEstimate. Shared by stripTechCardCosting (nested under TechCard.colorways) and
// GetColorwayByID (top-level usages field, H1 fix). Safe on nil entries.
func stripTechCardColorwayUsageCosting(usages []*pb_common.TechCardColorwayUsage) {
	for _, u := range usages {
		if u == nil {
			continue
		}
		u.LineTotal = nil
		u.SizeRunTotal = nil
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

// stripProductionRunCosting redacts the confidential money on a production run for an account
// without costing:read (Q5 RBAC symmetry / A3.2-#3). It closes the last asymmetry in the costing
// boundary: TechCard/Material/DevExpense money was already field-shaped, but a run's ACTUAL cost
// (the plan snapshot, the cost articles, and the computed actual/variance/materials-from-stock
// summary) was gated only by the production section — so a `production:read` role without costing
// saw more money about the fact than a `tech_cards:read` role without costing saw about the plan.
// Quantities (planned/received/defect), the defect rate, marker data and provenance flags stay —
// a production role still sees "how many units / how many defects", just not "at what cost". Safe
// on nil; symmetric with the strip* helpers above.
func stripProductionRunCosting(r *pb_common.ProductionRun) {
	if r == nil {
		return
	}
	r.PlannedUnitCost = nil // frozen plan snapshot (money)
	r.PlannedCurrency = ""
	if r.Run != nil {
		r.Run.Costs = nil // actual cost articles (amount / amount_base) are confidential
	}
	stripProductionRunActualsCosting(r.Actuals)
	// Receipts (Phase 4): the frozen valuation is money; the receiving facts (who/when/quantities
	// per line) stay — that is exactly what a warehouse role needs from a receipt. has_base is a
	// provenance bool and stays, matching the actuals above.
	for _, rc := range r.Receipts {
		if rc == nil {
			continue
		}
		rc.UnitCostBase = nil
		rc.BaseCurrency = ""
	}
	// Recon checks (Phase 8): the units check is quantities and stays; the money checks carry the
	// run's capitalised totals in expected/actual — confidential (final-review HIGH: they leaked
	// the full actual cost to production:read).
	if len(r.Recon) > 0 {
		kept := r.Recon[:0]
		for _, c := range r.Recon {
			if c == nil {
				continue
			}
			if c.Key == "units_receipts_vs_stock_journal" {
				kept = append(kept, c)
			}
		}
		r.Recon = kept
	}
	// Event payloads (Phase 8): they can carry money (compensated_fg_base, absorbed cost
	// estimates). The event stream itself (who/what/when/reason) stays; the JSON payload goes.
	for _, ev := range r.Events {
		if ev == nil {
			continue
		}
		ev.Payload = ""
	}
}

// stripProductionRunActualsCosting clears the money-bearing figures of a run's computed plan/fact
// summary, keeping the quantity totals, the defect rate and the provenance flags. Safe on nil.
func stripProductionRunActualsCosting(a *pb_common.ProductionRunActuals) {
	if a == nil {
		return
	}
	a.ActualTotalBase = nil
	a.ActualUnitCost = nil
	a.ByKind = nil
	a.PlannedTotalBase = nil
	a.UnitCostVariance = nil
	a.TotalVariance = nil
	a.MaterialsFromStockBase = nil
	a.ByColorway = nil
	a.UnattributedMaterialsBase = nil
	// Kept (not money): base_currency, planned/received/defect qty totals, defect_pct_actual,
	// has_base, mixed_materials_sources, has_uncosted_issues.
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

// stripChannelRoasCosting redacts the media-spend side of the settled-ROAS report for an account
// without costing:read. Spend and the figures DERIVED from it (roas, cac) are the same class the
// dashboard redacts by name via costingRedactedFieldNames (marketing_spend, blended_cac, cpo) — the
// per-channel report is simply not routed through that denylist, since its fields are named `spend`
// / `roas` / `cac`. has_spend goes with them (its "N/A" meaning is exactly what a blanked row
// should read as, and leaving it true would render a spend flag with no number, cf. the has_base /
// has_actuals precedent above). Settled revenue, orders and new customers are revenue-side and stay.
func stripChannelRoasCosting(resp *pb_admin.GetChannelRoasSettledResponse) {
	if resp == nil {
		return
	}
	for _, r := range resp.Rows {
		if r == nil {
			continue
		}
		r.Spend = nil
		r.Roas = 0
		r.Cac = 0
		r.HasSpend = false
	}
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

// stripStyleCostEstimate redacts every money figure from a cost-estimate response for an account
// without costing:read (task 19 / Q4). The material and article STRUCTURE stays — line identity,
// section, unit, per-garment consumption, and the price-provenance label — so a cost-blind
// constructor still sees WHAT the estimate is built from, just not the prices, totals, or the
// plan/fact comparison (which is entirely cost data). Safe on nil.
func stripStyleCostEstimate(resp *pb_admin.GetStyleCostEstimateResponse) {
	if resp == nil || resp.Estimate == nil {
		return
	}
	e := resp.Estimate
	for _, ml := range e.Materials {
		if ml == nil {
			continue
		}
		ml.UnitPrice = nil
		ml.Currency = ""
		ml.LineTotalBase = nil
		ml.HasBase = false
		// price_source (a provenance label) and consumption/section/unit stay; price_date is tied to
		// the redacted catalog price, so drop it too.
		ml.PriceDate = nil
	}
	e.MaterialsPerUnitBase = nil
	for _, al := range e.Articles {
		if al == nil {
			continue
		}
		al.Amount = nil
		al.Currency = ""
		al.AmountBase = nil
		al.HasBase = false
	}
	e.DefectPct = nil
	e.UnitCostBase = nil
	e.OrderCostBase = nil
	e.Comparison = nil // estimate-vs-actual-vs-snapshot is all money
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

// productionRunInsertHasCostingData reports whether a run write payload carries confidential cost
// input: an actual cost article (CMT, freight, …). The planned unit cost is not client-supplied (the
// service snapshots it) and quantities/markers are not money, so the cost articles are the only cost
// input on this payload. Mirrors techCardInsertHasCostingData.
func productionRunInsertHasCostingData(ins *pb_common.ProductionRunInsert) bool {
	return ins != nil && len(ins.Costs) > 0
}

// costPriceProvided reports whether a colourway write payload is trying to SET a product cost_price (a
// confidential figure). An absent/empty value means "leave the stored cost unchanged" (see
// nullDecimalFromPb) — not a cost write — so it is not gated. Because cost_price is write-only (never
// serialized on the product read path), a cost-stripped account's resave omits it and preserves the
// stored value: no anti-erase logic needed here.
func costPriceProvided(cost *pb_decimal.Decimal) bool {
	return strings.TrimSpace(cost.GetValue()) != ""
}

// preserveStoredCosting restores confidential cost fields onto an incoming tech-card update
// from the stored card, so a full-replace save by an account WITHOUT costing:write (whose read
// was cost-stripped, so the payload carries no costing) cannot blank the costing block or BOM
// purchase prices it never saw. The costing block is preserved wholesale; BOM prices follow the
// stable line_key, with the old natural-key FIFO retained only for legacy pairs where either side
// has no line_key. A reload failure is fatal to the update: proceeding would full-replace confidential
// prices with the cost-blind payload. Only call this after confirming the payload carries no costing data
// (techCardInsertHasCostingData is false), i.e. the caller isn't trying to set costs — this path is
// purely anti-erase, not a way to smuggle changes.
func (s *Server) preserveStoredCosting(ctx context.Context, techCardID int, incoming *entity.TechCardInsert) error {
	stored, err := s.repo.TechCards().GetTechCardByIdConsistent(ctx, techCardID)
	if err != nil {
		return fmt.Errorf("reload stored tech card %d for costing preservation: %w", techCardID, err)
	}
	if err := preserveStoredCostingFrom(stored, incoming); err != nil {
		return fmt.Errorf("reload stored tech card %d for costing preservation: %w", techCardID, err)
	}
	return nil
}

// preserveStoredCostingFrom applies the anti-erase restore from an already-consistent card reload.
// UpdateTechCard shares that reload with sign-off reconciliation so both security decisions observe
// the same stored snapshot; preserveStoredCosting remains as the standalone wrapper used elsewhere.
func preserveStoredCostingFrom(stored *entity.TechCard, incoming *entity.TechCardInsert) error {
	if stored == nil {
		return fmt.Errorf("empty stored card")
	}
	if incoming == nil {
		return fmt.Errorf("empty incoming card")
	}
	incoming.Costing = stored.Costing
	if len(stored.BomItems) == 0 || len(incoming.BomItems) == 0 {
		return nil
	}
	preservePrice := func(dst *entity.TechCardBomItem, src entity.TechCardBomItem) {
		dst.UnitPrice = src.UnitPrice
		dst.Currency = src.Currency
	}

	// Match every modern line by its stable identity before constructing the legacy pool. This makes
	// the match order-insensitive and keeps a keyed line's price attached through renames or supplier
	// edits. Marking both sides also ensures the fallback cannot steal a keyed match.
	matchedStored := make([]bool, len(stored.BomItems))
	matchedIncoming := make([]bool, len(incoming.BomItems))
	byLineKey := make(map[string]int, len(stored.BomItems))
	for i := range stored.BomItems {
		if key := stored.BomItems[i].LineKey; key != "" {
			byLineKey[key] = i
		}
	}
	for i := range incoming.BomItems {
		key := incoming.BomItems[i].LineKey
		storedIndex, ok := byLineKey[key]
		if key == "" || !ok || matchedStored[storedIndex] {
			continue
		}
		preservePrice(&incoming.BomItems[i], stored.BomItems[storedIndex])
		matchedStored[storedIndex] = true
		matchedIncoming[i] = true
	}

	// Natural keys are not guaranteed unique, so unmatched legacy candidates remain FIFO. A fallback
	// pair is legal only when at least one side lacks line_key; two different modern keys must never
	// match merely because their editable descriptive fields happen to agree.
	byNaturalKey := make(map[string][]int, len(stored.BomItems))
	for i := range stored.BomItems {
		if !matchedStored[i] {
			key := bomNaturalKey(stored.BomItems[i])
			byNaturalKey[key] = append(byNaturalKey[key], i)
		}
	}
	for i := range incoming.BomItems {
		if matchedIncoming[i] {
			continue
		}
		key := bomNaturalKey(incoming.BomItems[i])
		candidates := byNaturalKey[key]
		for candidateIndex, storedIndex := range candidates {
			if incoming.BomItems[i].LineKey != "" && stored.BomItems[storedIndex].LineKey != "" {
				continue
			}
			preservePrice(&incoming.BomItems[i], stored.BomItems[storedIndex])
			byNaturalKey[key] = append(candidates[:candidateIndex], candidates[candidateIndex+1:]...)
			break
		}
	}
	return nil
}

// bomNaturalKey identifies a legacy BOM article across a full-replace by its human identity
// (section + name + supplier ref), case/space-insensitive. It is used only when the stored or
// incoming line lacks the stable line_key.
func bomNaturalKey(b entity.TechCardBomItem) string {
	norm := func(s string) string { return strings.ToLower(strings.TrimSpace(s)) }
	// A LINKED line keys on its material id, never on its name. The name of a linked line is RESOLVED
	// from the catalog material on read (enrichMaterials), so the STORED side of this comparison
	// carries the material's name while the INCOMING payload may legitimately carry an empty one --
	// the admin client sends name:'' for a linked line that never had its own. Keying on the name
	// would make those two sides fail to match, and because this function drives the anti-erase
	// restore, a non-costing account's save would then silently blank the purchase price on every
	// linked BOM line. The material id is present and identical on both sides, so it is the stable
	// identity. Free-text lines have no material and keep keying on their own name.
	ident := norm(b.Name)
	if b.MaterialId.Valid && b.MaterialId.Int64 > 0 {
		ident = "material#" + strconv.FormatInt(b.MaterialId.Int64, 10)
	}
	return norm(string(b.Section)) + "\x1f" + ident + "\x1f" + norm(b.SupplierRef.String)
}

// carryOmittedFabricDirectionFrom copies the STORED направление onto every incoming BOM line that did
// not send the field, matched by line_key — the same shape as preserveStoredCostingFrom above and for
// a related reason: what a client did not say must not read as what it erased.
//
// It matters only for the DIGEST. The write already ignores this value (the upsert guards the column
// with IF(:fabric_direction_omitted, …)), so this cannot change what is stored; it changes what the
// MATERIALS sign-off is fingerprinted over, so a sign-off made from a tab that does not send the
// field is not born stale against the row it just approved.
//
// Keyed only: a legacy line with no line_key cannot be matched safely, and guessing by position
// would attach one line's cloth direction to another's.
func carryOmittedFabricDirectionFrom(stored *entity.TechCard, incoming *entity.TechCardInsert) {
	if stored == nil || incoming == nil {
		return
	}
	byKey := make(map[string]sql.NullString, len(stored.BomItems))
	for _, b := range stored.BomItems {
		if k := strings.TrimSpace(b.LineKey); k != "" {
			byKey[strings.ToLower(k)] = b.FabricDirection
		}
	}
	for i := range incoming.BomItems {
		if !incoming.BomItems[i].FabricDirectionOmitted {
			continue
		}
		if d, ok := byKey[strings.ToLower(strings.TrimSpace(incoming.BomItems[i].LineKey))]; ok {
			incoming.BomItems[i].FabricDirection = d
		}
	}
}

// carryOmittedPieceCutSymmetryFrom is the twin of carryOmittedFabricDirectionFrom for КАК КРОИТСЯ
// (0275), and exists for the same reason: what a client did not SAY must not read as what it ERASED.
//
// It matters only for the DIGEST. The write already ignores this value (the piece upsert guards the
// column with IF(:cut_symmetry_omitted, …)), so this cannot change what is stored; it changes what
// the CONSTRUCTION sign-off is fingerprinted over. Without it, an approval made from a tab that does
// not speak the field would hash "unmarked" while the column keeps `mirrored`, and that approval
// would read «changed since sign-off» the instant it was made — and forever, because re-approving
// from the same tab hashes the same absence. This exact failure already happened once, with
// направление ткани, which is why the twin exists rather than a second discovery.
//
// Keyed only, and keyed the way the STORE keys: the incoming line_key trimmed, compared verbatim
// against the stored one. Deliberately stricter than the fabric-direction twin above, which folds
// case — here a key that differs only in case is a row the store will INSERT as new, so folding case
// would hand a brand-new piece the pairing of a different one. Guessing by POSITION would do the
// same thing more often; a piece's pairing must never be inferred from a neighbour.
func carryOmittedPieceCutSymmetryFrom(stored *entity.TechCard, incoming *entity.TechCardInsert) {
	if stored == nil || incoming == nil {
		return
	}
	byKey := make(map[string]sql.NullString, len(stored.Pieces))
	for _, p := range stored.Pieces {
		if k := strings.TrimSpace(p.LineKey); k != "" {
			byKey[k] = p.CutSymmetry
		}
	}
	for i := range incoming.Pieces {
		if !incoming.Pieces[i].CutSymmetryOmitted {
			continue
		}
		if cs, ok := byKey[strings.TrimSpace(incoming.Pieces[i].LineKey)]; ok {
			incoming.Pieces[i].CutSymmetry = cs
		}
	}
}
