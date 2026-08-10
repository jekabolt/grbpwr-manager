package dto

import (
	"database/sql"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	"github.com/shopspring/decimal"
	pb_decimal "google.golang.org/genproto/googleapis/type/decimal"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// money2 formats a base-currency total at money scale (always 2 decimals), matching the admin
// comparison's StringFixed(2) so every figure in the estimate response reads consistently. Unit
// prices/consumption/percentages keep their raw precision (pbDecimalFromDecimal) instead.
func money2(d decimal.Decimal) *pb_decimal.Decimal {
	return &pb_decimal.Decimal{Value: d.StringFixed(2)}
}

// estimateArticleKinds is the fixed, ordered set of typed manual cost articles surfaced on the
// estimate (labour = cmt; overhead is a structural field, never free-form). Their sum + materials,
// grossed by defect%, is the estimated unit cost — the same decomposition
// ComputeTechCardCostBreakdownBase produces, now line-by-line with provenance (Q4).
// hardware/packaging left this list in Phase 2: those articles are BOM-priced per colourway now,
// so the estimate carries them inside its materials section instead of as flat manual kinds.
var estimateArticleKinds = []string{"cmt", "logistics", "overhead"}

// ComputeStyleCostEstimate builds the transparent estimated (plan) cost of one colourway (Q4). Each
// material line resolves its plan unit price via the ladder bom_item.unit_price → latest
// material_price (from `catalog`, keyed by material_id) → folded to base via `fx`, and carries WHERE
// the number came from (price_source/date/currency). This is a READ PROJECTION: it never writes
// product.cost_price and never touches the actual channel. It equals the legacy techCardCostingToPb
// unit_cost whenever every line has a BOM snapshot (no fallback) — the catalog fallback is the only
// point where the estimate can diverge from the saved document, and that line is flagged so the
// divergence is always explained.
//
// colorwayID<=0 prices the primary colourway (index 0). `catalog` need only contain the materials
// whose BOM line lacks a snapshot price; a missing entry means "no catalog price" → the line has no
// price and is flagged. The comparison block is filled by the caller (it needs production/snapshot
// data); this function leaves it nil.
func ComputeStyleCostEstimate(tc *entity.TechCard, colorwayID int, catalog map[int64]*entity.MaterialPrice, fx CostingFx) *pb_admin.StyleCostEstimate {
	if tc == nil {
		return nil
	}
	out := &pb_admin.StyleCostEstimate{
		TechCardId:   int32(tc.Id),
		StyleNumber:  tc.StyleNumber.String,
		Name:         tc.Name,
		BaseCurrency: fx.Base,
	}

	// baseSizeID is the costing basis (see entity.TechCardColorwayUsage.UnitTotal); totalOrderQty is
	// only the illustrative run the estimate multiplies the finished unit cost by. Keeping them apart
	// is the whole point of this phase — the declared mix no longer divides anything.
	baseSizeID := tc.CostingBaseSizeID()
	totalOrderQty := 0
	for _, q := range tc.SizeQuantities {
		if q.OrderQty > 0 {
			totalOrderQty += q.OrderQty
		}
	}
	out.OrderQty = int32(totalOrderQty)

	cw := pickColorway(tc, colorwayID)
	if cw != nil {
		// Expose the PRODUCT id (post-PR6 a colourway is a product) so the caller can look up the
		// colourway's cost_price snapshot; fall back to the tech_card_colorway id if unlinked.
		if cw.ProductId.Valid {
			out.ColorwayId = int64(cw.ProductId.Int32)
		} else {
			out.ColorwayId = int64(cw.Id)
		}
	}

	costingCcy := ""
	if tc.Costing != nil && tc.Costing.Currency.Valid {
		costingCcy = tc.Costing.Currency.String
	}

	var (
		usedCatalogFallback bool
		hasUnpricedLine     bool
		hasUnconvertibleMat bool
		// hasNoBaseSizeNorm is kept apart from hasUnpricedLine because the operator's next action is
		// a different one: "no price" sends them to the material, "no norm on the base size" sends
		// them to the size grading (or to naming a base size at all). Lumping the two under one
		// caveat sent people hunting for a price that was never the problem.
		hasNoBaseSizeNorm bool
		// hasNoNormLine is the third case, previously also swallowed by "unpriced": a priced article
		// on a usage that states no consumption and no quantity at all.
		hasNoNormLine bool
	)
	materialsBase := decimal.Zero
	if cw != nil {
		for i := range cw.Usages {
			u := &cw.Usages[i]
			// A piece-bound row (entity.IsPieceMaterialAssignment) assigns a material to a
			// cut-piece and carries no norm: no estimate line, no total contribution and no
			// caveat — an empty piece row is not a «line with no norm», and a legacy number on
			// one is not part of the garment's plan cost.
			if u.IsPieceMaterialAssignment() {
				continue
			}
			bom := resolveUsageBom(tc.BomItems, u)
			line := &pb_admin.StyleCostMaterialLine{}
			markerSourced := u.ConsumptionSource.String == entity.ConsumptionSourceMarker
			if bom != nil {
				line.BomItemId = int64(bom.Id)
				line.MaterialName = bom.Name
				line.Section = string(bom.Section)
				line.Unit = bom.Unit.String
				if markerSourced {
					// Marker-sourced norm: nothing is grossed (the measured length already
					// contains the waste). wastage_pct keeps meaning «эффективный итог» for old
					// clients: selvedge+cut, decomposed in the two dedicated fields.
					line.WastageSource = "marker"
					line.WastageSelvedgePct = pbDecimalFromNull(u.WasteSelvedgePct)
					line.WastageCutPct = pbDecimalFromNull(u.WasteCutPct)
					total := decimal.Zero
					if u.WasteSelvedgePct.Valid {
						total = total.Add(u.WasteSelvedgePct.Decimal)
					}
					if u.WasteCutPct.Valid {
						total = total.Add(u.WasteCutPct.Decimal)
					}
					// Always affirmative on marker rows: a marker with no recorded decomposition
					// means zero EXTRA wastage in costing, and old clients must read 0, not
					// "absent" (absent renders as the field missing, which looks like unknown).
					line.WastagePct = pbDecimalFromDecimal(total)
				} else {
					line.WastageSource = "bom_estimate"
					line.WastagePct = pbDecimalFromNull(bom.WastagePercent)
				}
			}

			qty, applyWaste, ok := usagePerGarmentQty(u, baseSizeID)
			if ok {
				line.Consumption = pbDecimalFromDecimal(qty)
			}

			price, ccy, source, priceDate := resolvePlanUnitPrice(u, bom, catalog)
			line.PriceSource = source
			line.Currency = ccy
			if source == pb_admin.StyleCostPriceSource_STYLE_COST_PRICE_SOURCE_CATALOG_LATEST {
				usedCatalogFallback = true
			}
			// Whatever the source resolved to: BOM_SNAPSHOT rows carry the stored provenance date
			// (when the price was typed or last repriced — Phase 3), CATALOG_LATEST rows the quote's
			// valid_from. Assigning only on the catalog branch discarded the snapshot date entirely.
			if priceDate.Valid {
				line.PriceDate = timestamppb.New(priceDate.Time)
			}
			if price.Valid {
				line.UnitPrice = pbDecimalFromDecimal(price.Decimal)
			}

			// Per-garment line total in the line's own currency: qty × unit_price, grossed by
			// wastage for measured/size-graded usage (countable trims take no wastage) — identical to
			// entity.UnitTotal with the resolved price substituted.
			//
			// The three failure modes are told APART, not merged into "unpriced". A line can be
			// uncostable because nobody priced the article, because a size-graded norm has no value
			// on the base sample size, or because the usage carries no norm at all — and each sends
			// the operator somewhere different. Reporting all three as "no price" (which is what a
			// single else-branch did) sent people hunting for a price that was already there.
			switch {
			case ok && price.Valid:
				lineTotal := qty.Mul(price.Decimal)
				// Marker-sourced rows are never grossed: the marker length already pays for the
				// waste (PIECES-WASTAGE-DESIGN §2.3) — grossing again is the double-count trap.
				if applyWaste && bom != nil && !markerSourced {
					lineTotal = grossByWastage(lineTotal, bom.WastagePercent)
				}
				if base, conv := fx.toBase(lineTotal, ccy); conv {
					line.HasBase = true
					line.LineTotalBase = money2(base)
					materialsBase = materialsBase.Add(base)
				} else {
					hasUnconvertibleMat = true
				}
			case !price.Valid:
				hasUnpricedLine = true
			case len(u.SizeConsumptions) > 0:
				hasNoBaseSizeNorm = true
			default:
				hasNoNormLine = true
			}

			out.Materials = append(out.Materials, line)
		}
	}
	out.MaterialsPerUnitBase = money2(materialsBase)

	// Typed manual articles (cmt/hardware/packaging/logistics/overhead), each folded to base.
	articlesBase := decimal.Zero
	hasUnconvertibleArt := false
	if tc.Costing != nil {
		for _, kind := range estimateArticleKinds {
			amt := articleAmount(tc.Costing, kind)
			if !amt.Valid {
				continue
			}
			al := &pb_admin.StyleCostArticleLine{
				Kind:     kind,
				Amount:   pbDecimalFromDecimal(amt.Decimal),
				Currency: costingCcy,
			}
			if base, conv := fx.toBase(amt.Decimal, costingCcy); conv {
				al.HasBase = true
				al.AmountBase = money2(base)
				articlesBase = articlesBase.Add(base)
			} else {
				hasUnconvertibleArt = true
			}
			out.Articles = append(out.Articles, al)
		}
	}

	defectPct := decimal.Zero
	if tc.Costing != nil && tc.Costing.DefectPercent.Valid {
		defectPct = tc.Costing.DefectPercent.Decimal
	}
	out.DefectPct = pbDecimalFromDecimal(defectPct)

	defectMul := decimal.NewFromInt(1).Add(defectPct.Div(decimal.NewFromInt(100)))
	unitBase := materialsBase.Add(articlesBase).Mul(defectMul)
	out.UnitCostBase = money2(unitBase)
	out.OrderCostBase = money2(unitBase.Mul(decimal.NewFromInt(int64(totalOrderQty))))

	out.Caveat = strings.Join(estimateCaveats(usedCatalogFallback, hasUnpricedLine, hasUnconvertibleMat,
		hasUnconvertibleArt, hasNoBaseSizeNorm, hasNoNormLine), "; ")
	return out
}

// pickColorway returns the requested colourway or the primary (index 0). A caller may identify the
// colourway by its product id (the public colourway identity post-PR6) or the tech_card_colorway id;
// both are matched. An explicit, unknown id yields nil (never silently swapped for the primary).
func pickColorway(tc *entity.TechCard, colorwayID int) *entity.TechCardColorway {
	if len(tc.Colorways) == 0 {
		return nil
	}
	if colorwayID > 0 {
		for i := range tc.Colorways {
			cw := &tc.Colorways[i]
			if cw.Id == colorwayID || (cw.ProductId.Valid && int(cw.ProductId.Int32) == colorwayID) {
				return cw
			}
		}
		return nil
	}
	return &tc.Colorways[0]
}

// resolveUsageBom finds the BOM line a usage consumes, preferring the durable FK (BomItemId, S2/S3)
// and falling back to the legacy positional index so the estimate resolves the same line the read
// path does during the transition.
func resolveUsageBom(bomItems []entity.TechCardBomItem, u *entity.TechCardColorwayUsage) *entity.TechCardBomItem {
	if u.BomItemId.Valid && u.BomItemId.Int64 > 0 {
		for i := range bomItems {
			if int64(bomItems[i].Id) == u.BomItemId.Int64 {
				return &bomItems[i]
			}
		}
	}
	return bomItemAtIndex(bomItems, u.BomItemIndex)
}

// usagePerGarmentQty returns the usage's per-garment quantity (price-free), whether wastage applies,
// and ok=false when there is no usable quantity. It mirrors entity.UnitTotal exactly with the price
// factored out: countable Quantity (no wastage); measured Consumption (wastage); a size-graded
// consumption taken at the style's BASE SAMPLE SIZE (wastage).
//
// ok=false on a size-graded usage means the card names no base size (baseSizeID<=0) or this usage
// has no norm on it. The estimate must then show the line WITHOUT a consumption and say so in the
// caveat — the previous behaviour, averaging the graded norms over the card's declared typical
// run, is exactly the invented denominator this phase removed.
func usagePerGarmentQty(u *entity.TechCardColorwayUsage, baseSizeID int) (decimal.Decimal, bool, bool) {
	if len(u.SizeConsumptions) == 0 {
		if u.Quantity.Valid {
			return u.Quantity.Decimal, false, true
		}
		if u.Consumption.Valid {
			return u.Consumption.Decimal, true, true
		}
		return decimal.Zero, false, false
	}
	if baseSizeID <= 0 {
		return decimal.Zero, false, false
	}
	for _, sc := range u.SizeConsumptions {
		if sc.SizeId == baseSizeID {
			return sc.Consumption, true, true
		}
	}
	return decimal.Zero, false, false
}

// resolvePlanUnitPrice applies the Q4 price ladder for one usage line. A colourway that PINS a
// different article than the slot default is priced from the PINNED article's catalog price
// alone — the slot's snapshot price describes the default article, so falling back to it would
// silently cost the wrong article; an unpriced pin surfaces as an unpriced line instead. An
// unpinned usage keeps the original ladder: the line's own snapshot price wins; else the latest
// catalog price for the linked material; else none. Returns the price, its currency, the
// provenance, and (for a catalog price) its effective date.
func resolvePlanUnitPrice(u *entity.TechCardColorwayUsage, bom *entity.TechCardBomItem, catalog map[int64]*entity.MaterialPrice) (decimal.NullDecimal, string, pb_admin.StyleCostPriceSource, sql.NullTime) {
	if bom == nil {
		return decimal.NullDecimal{}, "", pb_admin.StyleCostPriceSource_STYLE_COST_PRICE_SOURCE_NONE, sql.NullTime{}
	}
	if u != nil && u.MaterialId.Valid && u.MaterialId.Int64 > 0 &&
		!(bom.MaterialId.Valid && bom.MaterialId.Int64 == u.MaterialId.Int64) {
		if mp, ok := catalog[u.MaterialId.Int64]; ok && mp != nil {
			return decimal.NullDecimal{Decimal: mp.Price, Valid: true},
				mp.Currency,
				pb_admin.StyleCostPriceSource_STYLE_COST_PRICE_SOURCE_CATALOG_LATEST,
				sql.NullTime{Time: mp.ValidFrom, Valid: true}
		}
		return decimal.NullDecimal{}, "", pb_admin.StyleCostPriceSource_STYLE_COST_PRICE_SOURCE_NONE, sql.NullTime{}
	}
	if bom.UnitPrice.Valid {
		// The snapshot's own stored provenance date (Phase 3): when this price was typed or last
		// repriced from the catalog — the "snapshot / дата" evidence plan 11 promised. NULL on a
		// pre-provenance row, and the table then shows the badge alone, exactly as before.
		return bom.UnitPrice, bom.Currency.String, pb_admin.StyleCostPriceSource_STYLE_COST_PRICE_SOURCE_BOM_SNAPSHOT, bom.PriceSnapshotAt
	}
	if bom.MaterialId.Valid {
		if mp, ok := catalog[bom.MaterialId.Int64]; ok && mp != nil {
			return decimal.NullDecimal{Decimal: mp.Price, Valid: true},
				mp.Currency,
				pb_admin.StyleCostPriceSource_STYLE_COST_PRICE_SOURCE_CATALOG_LATEST,
				sql.NullTime{Time: mp.ValidFrom, Valid: true}
		}
	}
	return decimal.NullDecimal{}, "", pb_admin.StyleCostPriceSource_STYLE_COST_PRICE_SOURCE_NONE, sql.NullTime{}
}

// articleAmount returns one typed cost article's stored per-unit amount.
func articleAmount(c *entity.TechCardCosting, kind string) decimal.NullDecimal {
	switch kind {
	case "cmt":
		return c.CmtCost
	case "logistics":
		return c.LogisticsCost
	case "overhead":
		return c.OverheadCost
	}
	return decimal.NullDecimal{}
}

// grossByWastage grosses a base cost up by wastage_percent when set (× (1 + pct/100)) — the dto-side
// mirror of entity.applyWastage (which is unexported).
func grossByWastage(base decimal.Decimal, wastagePercent decimal.NullDecimal) decimal.Decimal {
	if !wastagePercent.Valid {
		return base
	}
	return base.Mul(decimal.NewFromInt(1).Add(wastagePercent.Decimal.Div(decimal.NewFromInt(100))))
}

func estimateCaveats(usedCatalogFallback, hasUnpricedLine, hasUnconvertibleMat, hasUnconvertibleArt,
	hasNoBaseSizeNorm, hasNoNormLine bool) []string {
	var c []string
	if usedCatalogFallback {
		c = append(c, "some material lines use the latest catalog price (no BOM snapshot); the estimate may drift from the saved plan document")
	}
	if hasNoBaseSizeNorm {
		c = append(c, "some size-graded material lines have no consumption on the style's base sample size (or the style names none) — those lines are NOT costed and the estimate understates")
	}
	if hasNoNormLine {
		c = append(c, "some material lines state no consumption or quantity at all — those lines are NOT costed and the estimate understates")
	}
	if hasUnpricedLine {
		c = append(c, "some material lines have no price (neither a BOM snapshot nor a catalog price) — the estimate understates")
	}
	if hasUnconvertibleMat || hasUnconvertibleArt {
		c = append(c, "some amounts have no FX rate to the base currency and are excluded from the base total")
	}
	return c
}
