package dto

import (
	"database/sql"
	"fmt"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/cache"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/shopspring/decimal"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// productionRunStatusPbToEntity maps the proto status enum to the stored string.
var productionRunStatusPbToEntity = map[pb_common.ProductionRunStatus]entity.ProductionRunStatus{
	pb_common.ProductionRunStatus_PRODUCTION_RUN_STATUS_PLANNED:     entity.ProductionRunPlanned,
	pb_common.ProductionRunStatus_PRODUCTION_RUN_STATUS_IN_PROGRESS: entity.ProductionRunInProgress,
	pb_common.ProductionRunStatus_PRODUCTION_RUN_STATUS_RECEIVED:    entity.ProductionRunReceived,
	pb_common.ProductionRunStatus_PRODUCTION_RUN_STATUS_CLOSED:      entity.ProductionRunClosed,
	pb_common.ProductionRunStatus_PRODUCTION_RUN_STATUS_CANCELLED:   entity.ProductionRunCancelled,
}

// productionRunStatusEntityToPb is the reverse map.
var productionRunStatusEntityToPb = func() map[entity.ProductionRunStatus]pb_common.ProductionRunStatus {
	m := make(map[entity.ProductionRunStatus]pb_common.ProductionRunStatus, len(productionRunStatusPbToEntity))
	for k, v := range productionRunStatusPbToEntity {
		m[v] = k
	}
	return m
}()

// productionRunCostKindPbToEntity maps the proto cost-kind enum to the stored string.
var productionRunCostKindPbToEntity = map[pb_common.ProductionRunCostKind]entity.ProductionRunCostKind{
	pb_common.ProductionRunCostKind_PRODUCTION_RUN_COST_KIND_MATERIALS: entity.ProductionRunCostMaterials,
	pb_common.ProductionRunCostKind_PRODUCTION_RUN_COST_KIND_CMT:       entity.ProductionRunCostCMT,
	pb_common.ProductionRunCostKind_PRODUCTION_RUN_COST_KIND_HARDWARE:  entity.ProductionRunCostHardware,
	pb_common.ProductionRunCostKind_PRODUCTION_RUN_COST_KIND_PACKAGING: entity.ProductionRunCostPackaging,
	pb_common.ProductionRunCostKind_PRODUCTION_RUN_COST_KIND_LOGISTICS: entity.ProductionRunCostLogistics,
	pb_common.ProductionRunCostKind_PRODUCTION_RUN_COST_KIND_DUTY:      entity.ProductionRunCostDuty,
	pb_common.ProductionRunCostKind_PRODUCTION_RUN_COST_KIND_OTHER:     entity.ProductionRunCostOther,
}

// productionRunCostKindEntityToPb is the reverse map. Fixed enum order (materials..other) is used
// for the by-kind rollup below.
var productionRunCostKindEntityToPb = func() map[entity.ProductionRunCostKind]pb_common.ProductionRunCostKind {
	m := make(map[entity.ProductionRunCostKind]pb_common.ProductionRunCostKind, len(productionRunCostKindPbToEntity))
	for k, v := range productionRunCostKindPbToEntity {
		m[v] = k
	}
	return m
}()

// productionRunCostKindOrder is the stable display order of cost kinds for the by-kind rollup.
var productionRunCostKindOrder = []entity.ProductionRunCostKind{
	entity.ProductionRunCostMaterials, entity.ProductionRunCostCMT, entity.ProductionRunCostHardware,
	entity.ProductionRunCostPackaging, entity.ProductionRunCostLogistics, entity.ProductionRunCostDuty,
	entity.ProductionRunCostOther,
}

// ConvertPbProductionRunInsertToEntity validates and converts a writable production run. The
// planned-cost snapshot is NOT taken from the client — the service layer sets it separately.
func ConvertPbProductionRunInsertToEntity(pb *pb_common.ProductionRunInsert) (*entity.ProductionRunInsert, error) {
	if pb == nil {
		return nil, fmt.Errorf("production run is required")
	}
	if pb.TechCardId <= 0 {
		return nil, fmt.Errorf("tech_card_id is required")
	}
	status, ok := productionRunStatusPbToEntity[pb.Status]
	if !ok {
		return nil, fmt.Errorf("status is required and must be valid")
	}
	if len(pb.Notes) > maxVarchar1024 {
		return nil, fmt.Errorf("notes must be at most %d characters", maxVarchar1024)
	}
	sizes, err := convertPbProductionRunSizes(pb.Sizes)
	if err != nil {
		return nil, err
	}
	costs, err := convertPbProductionRunCosts(pb.Costs)
	if err != nil {
		return nil, err
	}
	return &entity.ProductionRunInsert{
		TechCardId: int(pb.TechCardId),
		ReleaseId:  nullInt64FromPb(int64(pb.ReleaseId)),
		Status:     status,
		StartedAt:  nullTimeFromPbTimestamp(pb.StartedAt),
		ReceivedAt: nullTimeFromPbTimestamp(pb.ReceivedAt),
		Notes:      nullStringFromPb(pb.Notes),
		Sizes:      sizes,
		Costs:      costs,
	}, nil
}

func convertPbProductionRunCosts(pbs []*pb_common.ProductionRunCost) ([]entity.ProductionRunCost, error) {
	if len(pbs) == 0 {
		return nil, nil
	}
	out := make([]entity.ProductionRunCost, 0, len(pbs))
	for _, c := range pbs {
		if c == nil {
			continue
		}
		kind, ok := productionRunCostKindPbToEntity[c.Kind]
		if !ok {
			return nil, fmt.Errorf("production run cost: kind is required and must be valid")
		}
		if len(c.Description) > maxVarchar255 {
			return nil, fmt.Errorf("production run cost: description must be at most %d characters", maxVarchar255)
		}
		amount, err := nullDecimalFromPb(c.Amount)
		if err != nil {
			return nil, fmt.Errorf("production run cost amount: %w", err)
		}
		if !amount.Valid || amount.Decimal.IsNegative() {
			return nil, fmt.Errorf("production run cost: amount must be a non-negative number")
		}
		currency := strings.ToUpper(strings.TrimSpace(c.Currency))
		if len(currency) != maxCurrency {
			return nil, fmt.Errorf("production run cost: currency must be a 3-letter ISO 4217 code")
		}
		amountBase, err := nullDecimalFromPb(c.AmountBase)
		if err != nil {
			return nil, fmt.Errorf("production run cost amount_base: %w", err)
		}
		if amountBase.Valid && amountBase.Decimal.IsNegative() {
			return nil, fmt.Errorf("production run cost: amount_base must be non-negative")
		}
		out = append(out, entity.ProductionRunCost{
			Kind:        kind,
			Description: nullStringFromPb(c.Description),
			Amount:      amount.Decimal,
			Currency:    currency,
			AmountBase:  amountBase,
			IncurredAt:  nullDateFromPbTimestamp(c.IncurredAt),
		})
	}
	return out, nil
}

// FoldProductionRunCostsToBase fills each cost's AmountBase (when unset) by folding Amount from
// its currency into the base currency via the costing FX rates. A cost whose currency has no rate
// is left with AmountBase unset — the read-side actuals then report has_base=false. A caller-
// supplied amount_base (manual override) is preserved.
func FoldProductionRunCostsToBase(costs []entity.ProductionRunCost, fx CostingFx) {
	for i := range costs {
		if costs[i].AmountBase.Valid {
			continue
		}
		if base, ok := fx.toBase(costs[i].Amount, costs[i].Currency); ok {
			costs[i].AmountBase = decimal.NullDecimal{Decimal: roundMoney(base), Valid: true}
		}
	}
}

func convertPbProductionRunSizes(pbs []*pb_common.ProductionRunSize) ([]entity.ProductionRunSize, error) {
	if len(pbs) == 0 {
		return nil, nil
	}
	seen := make(map[int]struct{}, len(pbs))
	out := make([]entity.ProductionRunSize, 0, len(pbs))
	for _, sz := range pbs {
		if sz == nil {
			continue
		}
		if sz.SizeId <= 0 {
			return nil, fmt.Errorf("production run size: size_id is required")
		}
		if _, dup := seen[int(sz.SizeId)]; dup {
			return nil, fmt.Errorf("production run size: duplicate size_id %d", sz.SizeId)
		}
		seen[int(sz.SizeId)] = struct{}{}
		if sz.PlannedQty < 0 {
			return nil, fmt.Errorf("production run size: planned_qty must be non-negative")
		}
		e := entity.ProductionRunSize{SizeId: int(sz.SizeId), PlannedQty: int(sz.PlannedQty)}
		if sz.ReceivedQty != nil {
			if *sz.ReceivedQty < 0 {
				return nil, fmt.Errorf("production run size: received_qty must be non-negative")
			}
			e.ReceivedQty = sql.NullInt64{Int64: int64(*sz.ReceivedQty), Valid: true}
		}
		if sz.DefectQty != nil {
			if *sz.DefectQty < 0 {
				return nil, fmt.Errorf("production run size: defect_qty must be non-negative")
			}
			e.DefectQty = sql.NullInt64{Int64: int64(*sz.DefectQty), Valid: true}
		}
		out = append(out, e)
	}
	return out, nil
}

// ConvertEntityProductionRunToPb converts a stored run (with its size grid) to pb.
func ConvertEntityProductionRunToPb(r *entity.ProductionRun) *pb_common.ProductionRun {
	if r == nil {
		return nil
	}
	return &pb_common.ProductionRun{
		Id: int32(r.Id),
		Run: &pb_common.ProductionRunInsert{
			TechCardId: int32(r.TechCardId),
			ReleaseId:  int32(r.ReleaseId.Int64),
			Status:     productionRunStatusEntityToPb[r.Status],
			StartedAt:  pbTimestampFromNullTime(r.StartedAt),
			ReceivedAt: pbTimestampFromNullTime(r.ReceivedAt),
			Notes:      pbStringFromNull(r.Notes),
			Sizes:      productionRunSizesToPb(r.Sizes),
			Costs:      productionRunCostsToPb(r.Costs),
		},
		PlannedUnitCost: pbDecimalFromNull(r.PlannedUnitCost),
		PlannedCurrency: pbStringFromNull(r.PlannedCurrency),
		CreatedAt:       timestamppb.New(r.CreatedAt),
		UpdatedAt:       timestamppb.New(r.UpdatedAt),
		Actuals:         computeProductionRunActuals(r),
	}
}

func productionRunCostsToPb(costs []entity.ProductionRunCost) []*pb_common.ProductionRunCost {
	out := make([]*pb_common.ProductionRunCost, 0, len(costs))
	for _, c := range costs {
		out = append(out, &pb_common.ProductionRunCost{
			Kind:        productionRunCostKindEntityToPb[c.Kind],
			Description: pbStringFromNull(c.Description),
			Amount:      pbDecimalFromDecimal(c.Amount),
			Currency:    c.Currency,
			AmountBase:  pbDecimalFromNull(c.AmountBase),
			IncurredAt:  pbTimestampFromNullTime(c.IncurredAt),
		})
	}
	return out
}

// computeProductionRunActuals derives the plan/fact summary from a run's cost articles and its
// size grid. Base amounts come from cost.AmountBase (already folded on write); a cost with no
// base leaves has_base=false so the caller knows the total is partial. Ratios are guarded against
// division by zero and only emitted when their inputs are present.
func computeProductionRunActuals(r *entity.ProductionRun) *pb_common.ProductionRunActuals {
	var plannedQty, receivedQty, defectQty int64
	for _, sz := range r.Sizes {
		plannedQty += int64(sz.PlannedQty)
		if sz.ReceivedQty.Valid {
			receivedQty += sz.ReceivedQty.Int64
		}
		if sz.DefectQty.Valid {
			defectQty += sz.DefectQty.Int64
		}
	}

	totalBase := decimal.Zero
	hasBase := true
	byKind := make(map[entity.ProductionRunCostKind]decimal.Decimal)
	for _, c := range r.Costs {
		if !c.AmountBase.Valid {
			hasBase = false
			continue
		}
		totalBase = totalBase.Add(c.AmountBase.Decimal)
		byKind[c.Kind] = byKind[c.Kind].Add(c.AmountBase.Decimal)
	}

	out := &pb_common.ProductionRunActuals{
		ActualTotalBase:  pbDecimalFromDecimal(roundMoney(totalBase)),
		BaseCurrency:     cache.GetBaseCurrency(),
		PlannedQtyTotal:  int32(plannedQty),
		ReceivedQtyTotal: int32(receivedQty),
		DefectQtyTotal:   int32(defectQty),
		HasBase:          hasBase,
	}
	for _, k := range productionRunCostKindOrder {
		if amt, ok := byKind[k]; ok {
			out.ByKind = append(out.ByKind, &pb_common.ProductionRunCostByKind{
				Kind:       productionRunCostKindEntityToPb[k],
				AmountBase: pbDecimalFromDecimal(roundMoney(amt)),
			})
		}
	}

	recv := decimal.NewFromInt(receivedQty)
	var actualUnit decimal.Decimal
	haveUnit := false
	if receivedQty > 0 {
		actualUnit = totalBase.Div(recv)
		haveUnit = true
		out.ActualUnitCost = pbDecimalFromDecimal(roundMoney(actualUnit))
	}
	if denom := receivedQty + defectQty; denom > 0 {
		pct := decimal.NewFromInt(defectQty).Mul(decimal.NewFromInt(100)).Div(decimal.NewFromInt(denom))
		out.DefectPctActual = pbDecimalFromDecimal(pct.Round(2))
	}

	// plan/fact against the run's frozen planned unit cost, scaled to the received quantity.
	if r.PlannedUnitCost.Valid && receivedQty > 0 {
		plannedTotal := r.PlannedUnitCost.Decimal.Mul(recv)
		out.PlannedTotalBase = pbDecimalFromDecimal(roundMoney(plannedTotal))
		out.TotalVariance = pbDecimalFromDecimal(roundMoney(totalBase.Sub(plannedTotal)))
		if haveUnit {
			out.UnitCostVariance = pbDecimalFromDecimal(roundMoney(actualUnit.Sub(r.PlannedUnitCost.Decimal)))
		}
	}
	return out
}

func productionRunSizesToPb(sizes []entity.ProductionRunSize) []*pb_common.ProductionRunSize {
	out := make([]*pb_common.ProductionRunSize, 0, len(sizes))
	for _, sz := range sizes {
		pb := &pb_common.ProductionRunSize{SizeId: int32(sz.SizeId), PlannedQty: int32(sz.PlannedQty)}
		if sz.ReceivedQty.Valid {
			v := int32(sz.ReceivedQty.Int64)
			pb.ReceivedQty = &v
		}
		if sz.DefectQty.Valid {
			v := int32(sz.DefectQty.Int64)
			pb.DefectQty = &v
		}
		out = append(out, pb)
	}
	return out
}

// ProductionRunActualUnitCostBase returns the run's actual unit cost in the base currency, valid
// only when it is trustworthy for setting cost_price: there is at least one cost article, EVERY
// article folded to base (a partial total would understate cost), and some quantity was received.
// It is the same figure as ProductionRunActuals.actual_unit_cost under those conditions.
func ProductionRunActualUnitCostBase(r *entity.ProductionRun) decimal.NullDecimal {
	if r == nil || len(r.Costs) == 0 {
		return decimal.NullDecimal{}
	}
	var received int64
	for _, sz := range r.Sizes {
		if sz.ReceivedQty.Valid {
			received += sz.ReceivedQty.Int64
		}
	}
	if received == 0 {
		return decimal.NullDecimal{}
	}
	total := decimal.Zero
	for _, c := range r.Costs {
		if !c.AmountBase.Valid {
			return decimal.NullDecimal{} // partial fold → not trustworthy for cost_price
		}
		total = total.Add(c.AmountBase.Decimal)
	}
	return decimal.NullDecimal{Decimal: roundMoney(total.Div(decimal.NewFromInt(received))), Valid: true}
}

// NormalizeProductionRunStatusFilter validates an optional status filter string, returning the
// entity status ("" for no filter). It rejects an unknown non-empty value.
func NormalizeProductionRunStatusFilter(s string) (entity.ProductionRunStatus, error) {
	s = strings.TrimSpace(strings.ToLower(s))
	if s == "" {
		return "", nil
	}
	st := entity.ProductionRunStatus(s)
	if !entity.IsValidProductionRunStatus(st) {
		return "", fmt.Errorf("unknown production run status %q", s)
	}
	return st, nil
}
