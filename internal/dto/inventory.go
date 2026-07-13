package dto

import (
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/shopspring/decimal"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// materialMovementTypeToPb maps entity movement types to the proto enum.
var materialMovementTypeToPb = map[entity.MaterialMovementType]pb_common.MaterialMovementType{
	entity.MaterialMovementReceipt:           pb_common.MaterialMovementType_MATERIAL_MOVEMENT_TYPE_RECEIPT,
	entity.MaterialMovementReceiptProduction: pb_common.MaterialMovementType_MATERIAL_MOVEMENT_TYPE_RECEIPT_PRODUCTION,
	entity.MaterialMovementIssueProduction:   pb_common.MaterialMovementType_MATERIAL_MOVEMENT_TYPE_ISSUE_PRODUCTION,
	entity.MaterialMovementIssueSample:       pb_common.MaterialMovementType_MATERIAL_MOVEMENT_TYPE_ISSUE_SAMPLE,
	entity.MaterialMovementReturnProduction:  pb_common.MaterialMovementType_MATERIAL_MOVEMENT_TYPE_RETURN_PRODUCTION,
	entity.MaterialMovementReturnSample:      pb_common.MaterialMovementType_MATERIAL_MOVEMENT_TYPE_RETURN_SAMPLE,
	entity.MaterialMovementAdjustment:        pb_common.MaterialMovementType_MATERIAL_MOVEMENT_TYPE_ADJUSTMENT,
	entity.MaterialMovementWriteoff:          pb_common.MaterialMovementType_MATERIAL_MOVEMENT_TYPE_WRITEOFF,
}

// materialMovementTypeFromPb is the reverse map for list filtering.
var materialMovementTypeFromPb = func() map[pb_common.MaterialMovementType]entity.MaterialMovementType {
	m := make(map[pb_common.MaterialMovementType]entity.MaterialMovementType, len(materialMovementTypeToPb))
	for k, v := range materialMovementTypeToPb {
		m[v] = k
	}
	return m
}()

// validMaterialAdjustReasons is the closed set of adjustment/write-off reasons.
var validMaterialAdjustReasons = map[string]struct{}{
	entity.MaterialAdjustReasonStockCount: {}, entity.MaterialAdjustReasonDamage: {},
	entity.MaterialAdjustReasonLoss: {}, entity.MaterialAdjustReasonFound: {},
	entity.MaterialAdjustReasonCorrection: {}, entity.MaterialAdjustReasonPackaging: {},
	entity.MaterialAdjustReasonOther: {},
}

// ConvertPbReceiveMaterialStock validates and converts a receipt request. AdminUsername is set by
// the handler from the auth context, not here.
func ConvertPbReceiveMaterialStock(req *pb_admin.ReceiveMaterialStockRequest) (entity.MaterialReceiptInsert, error) {
	if req == nil || req.MaterialId <= 0 {
		return entity.MaterialReceiptInsert{}, fmt.Errorf("material_id is required")
	}
	qty, err := positiveDecimal(req.GetQuantity().GetValue(), "quantity")
	if err != nil {
		return entity.MaterialReceiptInsert{}, err
	}
	unitCost, err := nullDecimalFromPb(req.GetUnitCost())
	if err != nil {
		return entity.MaterialReceiptInsert{}, fmt.Errorf("unit_cost: %w", err)
	}
	currency := strings.ToUpper(strings.TrimSpace(req.GetCurrency()))
	if unitCost.Valid {
		if unitCost.Decimal.IsNegative() {
			return entity.MaterialReceiptInsert{}, fmt.Errorf("unit_cost must be non-negative")
		}
		if len(currency) != maxCurrency {
			return entity.MaterialReceiptInsert{}, fmt.Errorf("currency must be a 3-letter ISO 4217 code when unit_cost is set")
		}
	}
	occurredAt, err := parseNullDate(req.GetOccurredAt())
	if err != nil {
		return entity.MaterialReceiptInsert{}, fmt.Errorf("occurred_at: %w", err)
	}
	return entity.MaterialReceiptInsert{
		MaterialId:  int(req.MaterialId),
		Quantity:    qty,
		UnitCost:    unitCost,
		Currency:    currency,
		Lot:         nullStringFromPb(req.GetLot()),
		SupplierDoc: nullStringFromPb(req.GetSupplierDoc()),
		OccurredAt:  occurredAt,
		Comment:     nullStringFromPb(req.GetComment()),
	}, nil
}

// nullInt32Value yields the int32 value of a NullInt32, or 0 when invalid.
func nullInt32Value(v sql.NullInt32) int32 {
	if !v.Valid {
		return 0
	}
	return v.Int32
}

// ConvertPbIssueMaterialStock validates and converts an issue/return request.
func ConvertPbIssueMaterialStock(req *pb_admin.IssueMaterialStockRequest) (entity.MaterialIssueInsert, error) {
	if req == nil || req.MaterialId <= 0 {
		return entity.MaterialIssueInsert{}, fmt.Errorf("material_id is required")
	}
	qty, err := positiveDecimal(req.GetQuantity().GetValue(), "quantity")
	if err != nil {
		return entity.MaterialIssueInsert{}, err
	}
	hasRun := req.ProductionRunId > 0
	hasSample := req.SampleId > 0
	if hasRun == hasSample {
		return entity.MaterialIssueInsert{}, fmt.Errorf("exactly one of production_run_id / sample_id must be set")
	}
	occurredAt, err := parseNullDate(req.GetOccurredAt())
	if err != nil {
		return entity.MaterialIssueInsert{}, fmt.Errorf("occurred_at: %w", err)
	}
	ins := entity.MaterialIssueInsert{
		MaterialId: int(req.MaterialId),
		Quantity:   qty,
		IsReturn:   req.IsReturn,
		OccurredAt: occurredAt,
		Comment:    nullStringFromPb(req.GetComment()),
	}
	if hasRun {
		ins.ProductionRunId = nullInt32FromPb(req.ProductionRunId)
	} else {
		ins.SampleId = nullInt32FromPb(req.SampleId)
	}
	return ins, nil
}

// ConvertPbAdjustMaterialStock validates and converts a stock-count / write-off request.
func ConvertPbAdjustMaterialStock(req *pb_admin.AdjustMaterialStockRequest) (entity.MaterialAdjustInsert, error) {
	if req == nil || req.MaterialId <= 0 {
		return entity.MaterialAdjustInsert{}, fmt.Errorf("material_id is required")
	}
	mode := entity.MaterialAdjustMode(strings.ToLower(strings.TrimSpace(req.GetMode())))
	switch mode {
	case entity.MaterialAdjustModeSet, entity.MaterialAdjustModeAdjust, entity.MaterialAdjustModeWriteoff:
	default:
		return entity.MaterialAdjustInsert{}, fmt.Errorf("mode must be one of set|adjust|writeoff")
	}
	qty, err := decimalFromString(req.GetQuantity().GetValue(), "quantity")
	if err != nil {
		return entity.MaterialAdjustInsert{}, err
	}
	reason := strings.ToLower(strings.TrimSpace(req.GetReason()))
	if reason != "" {
		if _, ok := validMaterialAdjustReasons[reason]; !ok {
			return entity.MaterialAdjustInsert{}, fmt.Errorf("invalid reason %q", reason)
		}
	}
	return entity.MaterialAdjustInsert{
		MaterialId: int(req.MaterialId),
		Mode:       mode,
		Quantity:   qty,
		Reason:     reason,
		Comment:    nullStringFromPb(req.GetComment()),
	}, nil
}

// ConvertEntityMaterialMovementToPb converts a ledger row to pb.
func ConvertEntityMaterialMovementToPb(m entity.MaterialMovement) *pb_common.MaterialMovement {
	out := &pb_common.MaterialMovement{
		Id:              int32(m.Id),
		MaterialId:      int32(m.MaterialId),
		MovementType:    materialMovementTypeToPb[m.MovementType],
		Quantity:        pbDecimalFromDecimal(m.Quantity),
		OnHandBefore:    pbDecimalFromDecimal(m.OnHandBefore),
		OnHandAfter:     pbDecimalFromDecimal(m.OnHandAfter),
		UnitCost:        pbDecimalFromNull(m.UnitCost),
		Currency:        m.Currency.String,
		UnitCostBase:    pbDecimalFromNull(m.UnitCostBase),
		ProductionRunId: nullInt32Value(m.ProductionRunId),
		SampleId:        nullInt32Value(m.SampleId),
		TechCardId:      nullInt32Value(m.TechCardId),
		Lot:             m.Lot.String,
		SupplierDoc:     m.SupplierDoc.String,
		Reason:          m.Reason.String,
		Comment:         m.Comment.String,
		AdminUsername:   m.AdminUsername,
		CreatedAt:       timestamppb.New(m.CreatedAt),
	}
	if m.OccurredAt.Valid {
		out.OccurredAt = timestamppb.New(m.OccurredAt.Time)
	}
	return out
}

// ConvertEntityMaterialStockToPb converts a stock balance to pb.
func ConvertEntityMaterialStockToPb(st entity.MaterialStock, baseCurrency string) *pb_common.MaterialStock {
	return &pb_common.MaterialStock{
		MaterialId:      int32(st.MaterialId),
		OnHand:          pbDecimalFromDecimal(st.OnHand),
		AvgUnitCostBase: pbDecimalFromNull(st.AvgUnitCostBase),
		BaseCurrency:    baseCurrency,
		UpdatedAt:       timestamppb.New(st.UpdatedAt),
	}
}

// ConvertEntityMaterialStockRowToPb converts a warehouse list row to pb.
func ConvertEntityMaterialStockRowToPb(r entity.MaterialStockRow, baseCurrency string) *pb_common.MaterialStockRow {
	return &pb_common.MaterialStockRow{
		Material:        ConvertEntityMaterialToPb(entity.MaterialWithPrice{Material: r.Material}),
		OnHand:          pbDecimalFromDecimal(r.OnHand),
		AvgUnitCostBase: pbDecimalFromNull(r.AvgUnitCostBase),
		StockValueBase:  pbDecimalFromNull(r.StockValueBase),
		MinStock:        pbDecimalFromNull(r.MinStock),
		BelowMinStock:   r.BelowMinStock,
		BaseCurrency:    baseCurrency,
	}
}

// MaterialMovementTypeFromPb maps a proto movement-type filter to the entity value (empty for
// UNKNOWN).
func MaterialMovementTypeFromPb(t pb_common.MaterialMovementType) entity.MaterialMovementType {
	return materialMovementTypeFromPb[t]
}

// --- small local helpers ---

func positiveDecimal(s, field string) (decimal.Decimal, error) {
	d, err := decimalFromString(s, field)
	if err != nil {
		return decimal.Decimal{}, err
	}
	if d.LessThanOrEqual(decimal.Zero) {
		return decimal.Decimal{}, fmt.Errorf("%s must be positive", field)
	}
	return d, nil
}

func decimalFromString(s, field string) (decimal.Decimal, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return decimal.Decimal{}, fmt.Errorf("%s is required", field)
	}
	d, err := decimal.NewFromString(s)
	if err != nil {
		return decimal.Decimal{}, fmt.Errorf("%s must be a number", field)
	}
	return d, nil
}

func parseNullDate(s string) (sql.NullTime, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return sql.NullTime{}, nil
	}
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		return sql.NullTime{}, fmt.Errorf("must be YYYY-MM-DD")
	}
	return sql.NullTime{Time: t, Valid: true}, nil
}
