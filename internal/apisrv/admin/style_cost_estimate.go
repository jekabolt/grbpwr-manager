package admin

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"

	"github.com/jekabolt/grbpwr-manager/internal/dto"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	"github.com/shopspring/decimal"
	pb_decimal "google.golang.org/genproto/googleapis/type/decimal"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// styleCostRunScan caps runs aggregated for the estimate's actual comparison (a style has a handful).
const styleCostRunScan = 100

// GetStyleCostEstimate returns the transparent estimated (plan) cost of one style colourway (Q4):
// a per-line material breakdown resolved via the price ladder (bom_item.unit_price → latest
// material_price → base via costing FX) with each line's source/date/currency, the typed cost
// articles, and a plan-vs-fact comparison (estimate vs production-run actual vs the order-time cost
// snapshot). It is a READ PROJECTION — it never writes product.cost_price and never touches the
// actual (production_run→receive→snapshot) channel. All money is stripped without costing:read.
func (s *Server) GetStyleCostEstimate(ctx context.Context, req *pb_admin.GetStyleCostEstimateRequest) (*pb_admin.GetStyleCostEstimateResponse, error) {
	tcID := int(req.GetTechCardId())
	if tcID <= 0 {
		return nil, status.Error(codes.InvalidArgument, "tech_card_id is required")
	}
	card, err := s.repo.TechCards().GetTechCardById(ctx, tcID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, status.Error(codes.NotFound, "tech card not found")
		}
		slog.Default().ErrorContext(ctx, "style cost estimate: can't load tech card", slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't load tech card")
	}

	fx := s.costingFx(ctx)
	catalog := s.buildCatalogFallback(ctx, card)
	est := dto.ComputeStyleCostEstimate(card, int(req.GetColorwayId()), catalog, fx)
	if est == nil {
		return nil, status.Error(codes.Internal, "can't compute estimate")
	}
	est.Comparison = s.computeStyleCostComparison(ctx, tcID, est)

	resp := &pb_admin.GetStyleCostEstimateResponse{Estimate: est}
	// The estimate is entirely confidential cost data — strip it for accounts without costing:read,
	// leaving the material/article STRUCTURE (identity, section, consumption, provenance labels) but
	// no money, exactly as GetTechCard shows a cost-blind constructor the BOM without prices.
	if read, _ := s.costingAccess(ctx); !read {
		stripStyleCostEstimate(resp)
	}
	return resp, nil
}

// buildCatalogFallback fetches the latest catalog price for every BOM material whose line carries no
// plan snapshot (plan-1 empty → plan-2 fallback). Best-effort: a material that can't be loaded simply
// leaves its line unpriced (flagged in the estimate), never fails the request. Deduped by material id.
func (s *Server) buildCatalogFallback(ctx context.Context, card *entity.TechCard) map[int64]*entity.MaterialPrice {
	need := map[int64]bool{}
	for i := range card.BomItems {
		b := &card.BomItems[i]
		if !b.UnitPrice.Valid && b.MaterialId.Valid && b.MaterialId.Int64 > 0 {
			need[b.MaterialId.Int64] = true
		}
	}
	if len(need) == 0 {
		return nil
	}
	out := make(map[int64]*entity.MaterialPrice, len(need))
	for id := range need {
		m, err := s.repo.TechCards().GetMaterial(ctx, int(id))
		if err != nil {
			slog.Default().WarnContext(ctx, "style cost estimate: can't load catalog material for fallback price",
				slog.Int64("material_id", id), slog.String("err", err.Error()))
			continue
		}
		if m != nil && m.LatestPrice != nil {
			out[id] = m.LatestPrice
		}
	}
	return out
}

// computeStyleCostComparison ties the plan estimate to the two downstream facts: the production-run
// actual (reusing the SAME aggregation as GetStyleEconomics — not recomputed) and the order-time cost
// snapshot (current product.cost_price). Never reads the live warehouse for a margin figure; the
// actual is the legitimate production-cost channel and the snapshot is product.cost_price.
func (s *Server) computeStyleCostComparison(ctx context.Context, tcID int, est *pb_admin.StyleCostEstimate) *pb_admin.StyleCostComparison {
	cmp := &pb_admin.StyleCostComparison{
		EstimateUnitCostBase: est.GetUnitCostBase(),
	}
	estUnit := pbToDecimal(est.GetUnitCostBase())

	// Actual: Σ production actuals ÷ received, exactly as the style economics card computes it.
	var actualUnit decimal.NullDecimal
	runs, _, err := s.repo.ProductionRuns().ListProductionRuns(ctx, styleCostRunScan, 0, entity.ProductionRunListFilter{TechCardId: tcID})
	if err != nil {
		slog.Default().ErrorContext(ctx, "style cost estimate: can't list production runs", slog.String("err", err.Error()))
	} else {
		matFromStock, mErr := s.repo.Metrics().GetStyleMaterialsFromStock(ctx, tcID)
		if mErr != nil {
			slog.Default().ErrorContext(ctx, "style cost estimate: can't get materials from stock", slog.String("err", mErr.Error()))
		} else {
			hasStock := !matFromStock.Base.IsZero() || matFromStock.HasUncosted
			prod := dto.ComputeStyleProductionSummary(runs, matFromStock.Base, hasStock)
			if prod.GetReceivedQtyTotal() > 0 && prod.GetHasActuals() {
				actual := pbToDecimal(prod.GetActualCostBase())
				u := actual.Div(decimal.NewFromInt(int64(prod.GetReceivedQtyTotal())))
				actualUnit = decimal.NullDecimal{Decimal: u, Valid: true}
				cmp.ActualUnitCostBase = estMoney(u)
				cmp.HasActual = true
			}
			cmp.ByKind = s.styleCostByKindVariance(runs, matFromStock.Base, prod.GetReceivedQtyTotal(), est)
		}
	}

	// Snapshot: the colourway product's current cost_price — the order-time COGS basis.
	var snapshotUnit decimal.NullDecimal
	if pid := int(est.GetColorwayId()); pid > 0 {
		if ci, cErr := s.repo.Products().GetProductCostInfo(ctx, pid); cErr != nil {
			slog.Default().ErrorContext(ctx, "style cost estimate: can't load product cost info", slog.String("err", cErr.Error()))
		} else if ci != nil && ci.CostPrice.Valid {
			snapshotUnit = ci.CostPrice
			cmp.SnapshotCostBase = estMoney(ci.CostPrice.Decimal)
			cmp.SnapshotSource = ci.CostPriceSource.String
			cmp.HasSnapshot = true
		}
	}

	// Pairwise deltas, only where both operands exist.
	if cmp.HasActual {
		cmp.EstimateVsActual = estMoney(actualUnit.Decimal.Sub(estUnit))
	}
	if cmp.HasSnapshot {
		cmp.EstimateVsSnapshot = estMoney(snapshotUnit.Decimal.Sub(estUnit))
	}
	if cmp.HasActual && cmp.HasSnapshot {
		cmp.ActualVsSnapshot = estMoney(snapshotUnit.Decimal.Sub(actualUnit.Decimal))
	}

	var caveats []string
	if !cmp.HasActual {
		caveats = append(caveats, "no production actuals yet — estimate cannot be checked against a real run")
	}
	if !cmp.HasSnapshot {
		caveats = append(caveats, "no cost_price snapshot on the colourway — nothing booked into COGS yet")
	}
	cmp.Caveat = joinCaveats(caveats)
	return cmp
}

// styleCostByKindVariance builds the per-cost-kind plan/fact rows: the estimate's per-unit figure for
// each kind against the production actual for the same kind (Σ over runs ÷ received). materials on the
// actual side is the manual `materials` articles PLUS the warehouse materials-from-stock, matching how
// the run/style actuals fold them together.
func (s *Server) styleCostByKindVariance(runs []entity.ProductionRun, materialsFromStockBase decimal.Decimal, received int32, est *pb_admin.StyleCostEstimate) []*pb_admin.StyleCostKindVariance {
	// Estimate per-unit by kind.
	estByKind := map[string]decimal.Decimal{}
	matBase := decimal.Zero
	for _, ml := range est.GetMaterials() {
		if ml.GetHasBase() {
			matBase = matBase.Add(pbToDecimal(ml.GetLineTotalBase()))
		}
	}
	estByKind["materials"] = matBase
	for _, al := range est.GetArticles() {
		if al.GetHasBase() {
			estByKind[al.GetKind()] = pbToDecimal(al.GetAmountBase())
		}
	}

	// Actual per-unit by kind (only when we can divide by received units).
	actByKind := map[string]decimal.Decimal{}
	haveActual := received > 0
	if haveActual {
		totalByKind := map[string]decimal.Decimal{}
		for i := range runs {
			if runs[i].Status == entity.ProductionRunCancelled {
				continue
			}
			for _, c := range runs[i].Costs {
				if c.AmountBase.Valid {
					totalByKind[string(c.Kind)] = totalByKind[string(c.Kind)].Add(c.AmountBase.Decimal)
				}
			}
		}
		totalByKind["materials"] = totalByKind["materials"].Add(materialsFromStockBase)
		recv := decimal.NewFromInt(int64(received))
		for k, v := range totalByKind {
			actByKind[k] = v.Div(recv)
		}
	}

	// Emit a row for every kind present on either side, in a stable order.
	order := []string{"materials", "cmt", "hardware", "packaging", "logistics", "overhead", "duty", "other"}
	var out []*pb_admin.StyleCostKindVariance
	for _, k := range order {
		e, hasE := estByKind[k]
		a, hasA := actByKind[k]
		if !hasE && !hasA {
			continue
		}
		row := &pb_admin.StyleCostKindVariance{Kind: k}
		if hasE {
			row.EstimateBase = estMoney(e)
		}
		if haveActual && hasA {
			row.ActualBase = estMoney(a)
			row.HasActual = true
			row.Variance = estMoney(a.Sub(e))
		}
		out = append(out, row)
	}
	return out
}

// estMoney formats a decimal as a base-currency proto amount at money scale (2), matching the
// admin package's StringFixed(2) convention.
func estMoney(d decimal.Decimal) *pb_decimal.Decimal {
	return &pb_decimal.Decimal{Value: d.StringFixed(2)}
}

// pbToDecimal parses a proto Decimal to a decimal, treating nil/blank/garbage as zero (these are
// output figures the server itself just produced, so a parse error is not an operator-facing case).
func pbToDecimal(d *pb_decimal.Decimal) decimal.Decimal {
	if d == nil {
		return decimal.Zero
	}
	v, err := decimal.NewFromString(d.GetValue())
	if err != nil {
		return decimal.Zero
	}
	return v
}

func joinCaveats(c []string) string {
	out := ""
	for i, s := range c {
		if i > 0 {
			out += "; "
		}
		out += s
	}
	return out
}
