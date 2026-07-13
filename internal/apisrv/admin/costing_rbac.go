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

// stripReleaseMetaCosting clears the planned unit cost on a release header. Safe on nil.
func stripReleaseMetaCosting(m *pb_common.TechCardReleaseMeta) {
	if m == nil {
		return
	}
	m.UnitCost = nil
	m.Currency = ""
}

// stripMetricsCosting redacts the cost/margin sections of a GetMetrics response, leaving
// commerce, traffic and email intact. Applied when the caller lacks costing:read.
func stripMetricsCosting(resp *pb_admin.GetMetricsResponse) {
	if resp == nil {
		return
	}
	if resp.Business != nil {
		resp.Business.Margin = nil // COGS/margin sub-message; commerce/traffic/email stay
	}
	resp.MarginByStyle = nil
	resp.CogsStructure = nil
	resp.InventoryValuation = nil
}

// stripDashboardCosting redacts the margin figures of the decision dashboard, keeping
// revenue, order count, inventory action lists and alerts. Applied without costing:read.
func stripDashboardCosting(resp *pb_admin.GetDashboardResponse) {
	if resp == nil {
		return
	}
	resp.GrossMargin = nil
	resp.GrossMarginPct = 0
	resp.ContributionMargin = nil
	resp.TopByMargin = nil // products re-ranked by gross margin € — leaks relative margins
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
	byKey := make(map[string]bomPrice, len(stored.BomItems))
	for _, b := range stored.BomItems {
		byKey[bomNaturalKey(b)] = bomPrice{b.UnitPrice, b.Currency}
	}
	for i := range incoming.BomItems {
		if p, ok := byKey[bomNaturalKey(incoming.BomItems[i])]; ok {
			incoming.BomItems[i].UnitPrice = p.unit
			incoming.BomItems[i].Currency = p.cur
		}
	}
}

// bomNaturalKey identifies a BOM article across a full-replace by its human identity
// (section + name + supplier ref), case/space-insensitive, so a preserved price re-attaches
// to the same line even though row ids are reassigned on replace.
func bomNaturalKey(b entity.TechCardBomItem) string {
	norm := func(s string) string { return strings.ToLower(strings.TrimSpace(s)) }
	return norm(string(b.Section)) + "\x1f" + norm(b.Name) + "\x1f" + norm(b.SupplierRef.String)
}
