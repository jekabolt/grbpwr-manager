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
var estimateArticleKinds = []string{"cmt", "hardware", "packaging", "logistics", "overhead"}

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

	orderQtyBySize := make(map[int]int, len(tc.SizeQuantities))
	totalOrderQty := 0
	for _, q := range tc.SizeQuantities {
		orderQtyBySize[q.SizeId] = q.OrderQty
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
	)
	materialsBase := decimal.Zero
	if cw != nil {
		for i := range cw.Usages {
			u := &cw.Usages[i]
			bom := resolveUsageBom(tc.BomItems, u)
			line := &pb_admin.StyleCostMaterialLine{}
			if bom != nil {
				line.BomItemId = int64(bom.Id)
				line.MaterialName = bom.Name
				line.Section = string(bom.Section)
				line.Unit = bom.Unit.String
				line.WastagePct = pbDecimalFromNull(bom.WastagePercent)
			}

			qty, applyWaste, ok := usagePerGarmentQty(u, orderQtyBySize, totalOrderQty)
			if ok {
				line.Consumption = pbDecimalFromDecimal(qty)
			}

			price, ccy, source, priceDate := resolvePlanUnitPrice(bom, catalog)
			line.PriceSource = source
			line.Currency = ccy
			if source == pb_admin.StyleCostPriceSource_STYLE_COST_PRICE_SOURCE_CATALOG_LATEST {
				usedCatalogFallback = true
				if priceDate.Valid {
					line.PriceDate = timestamppb.New(priceDate.Time)
				}
			}
			if price.Valid {
				line.UnitPrice = pbDecimalFromDecimal(price.Decimal)
			}

			// Per-garment line total in the line's own currency: qty × unit_price, grossed by
			// wastage for measured/per-size usage (countable trims take no wastage) — identical to
			// entity.UnitTotal with the resolved price substituted.
			if ok && price.Valid {
				lineTotal := qty.Mul(price.Decimal)
				if applyWaste && bom != nil {
					lineTotal = grossByWastage(lineTotal, bom.WastagePercent)
				}
				if base, conv := fx.toBase(lineTotal, ccy); conv {
					line.HasBase = true
					line.LineTotalBase = money2(base)
					materialsBase = materialsBase.Add(base)
				} else {
					hasUnconvertibleMat = true
				}
			} else {
				hasUnpricedLine = true
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

	out.Caveat = strings.Join(estimateCaveats(usedCatalogFallback, hasUnpricedLine, hasUnconvertibleMat, hasUnconvertibleArt), "; ")
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
// factored out: countable Quantity (no wastage); measured Consumption (wastage); per-size graded
// consumption normalised to per-garment by dividing the run quantity by totalOrderQty (wastage).
func usagePerGarmentQty(u *entity.TechCardColorwayUsage, orderQtyBySize map[int]int, totalOrderQty int) (decimal.Decimal, bool, bool) {
	if len(u.SizeConsumptions) == 0 {
		if u.Quantity.Valid {
			return u.Quantity.Decimal, false, true
		}
		if u.Consumption.Valid {
			return u.Consumption.Decimal, true, true
		}
		return decimal.Zero, false, false
	}
	if totalOrderQty <= 0 {
		return decimal.Zero, false, false
	}
	runQty := decimal.Zero
	for _, sc := range u.SizeConsumptions {
		q, ok := orderQtyBySize[sc.SizeId]
		if !ok || q <= 0 {
			continue
		}
		runQty = runQty.Add(sc.Consumption.Mul(decimal.NewFromInt(int64(q))))
	}
	if runQty.IsZero() {
		return decimal.Zero, false, false
	}
	return runQty.Div(decimal.NewFromInt(int64(totalOrderQty))), true, true
}

// resolvePlanUnitPrice applies the Q4 price ladder for one BOM line: the line's own snapshot price
// wins; else the latest catalog price for the linked material; else none. Returns the price, its
// currency, the provenance, and (for a catalog price) its effective date.
func resolvePlanUnitPrice(bom *entity.TechCardBomItem, catalog map[int64]*entity.MaterialPrice) (decimal.NullDecimal, string, pb_admin.StyleCostPriceSource, sql.NullTime) {
	if bom == nil {
		return decimal.NullDecimal{}, "", pb_admin.StyleCostPriceSource_STYLE_COST_PRICE_SOURCE_NONE, sql.NullTime{}
	}
	if bom.UnitPrice.Valid {
		return bom.UnitPrice, bom.Currency.String, pb_admin.StyleCostPriceSource_STYLE_COST_PRICE_SOURCE_BOM_SNAPSHOT, sql.NullTime{}
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
	case "hardware":
		return c.HardwareCost
	case "packaging":
		return c.PackagingCost
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

func estimateCaveats(usedCatalogFallback, hasUnpricedLine, hasUnconvertibleMat, hasUnconvertibleArt bool) []string {
	var c []string
	if usedCatalogFallback {
		c = append(c, "some material lines use the latest catalog price (no BOM snapshot); the estimate may drift from the saved plan document")
	}
	if hasUnpricedLine {
		c = append(c, "some material lines have no price (neither a BOM snapshot nor a catalog price) — the estimate understates")
	}
	if hasUnconvertibleMat || hasUnconvertibleArt {
		c = append(c, "some amounts have no FX rate to the base currency and are excluded from the base total")
	}
	return c
}
