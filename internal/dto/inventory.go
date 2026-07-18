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
	pb_decimal "google.golang.org/genproto/googleapis/type/decimal"
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
	entity.MaterialAdjustReasonScrap: {}, entity.MaterialAdjustReasonOther: {},
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
	// A non-empty currency must be a supported (selling) currency or USDT — material lots are an
	// EXPENSE surface, so USDT is allowed here (the store persists it whenever set, so a junk value
	// would overflow the column); a unit cost additionally requires the currency.
	if currency != "" && !IsExpenseCurrency(currency) {
		return entity.MaterialReceiptInsert{}, fmt.Errorf("currency must be a supported currency or USDT")
	}
	if unitCost.Valid {
		if unitCost.Decimal.IsNegative() {
			return entity.MaterialReceiptInsert{}, fmt.Errorf("unit_cost must be non-negative")
		}
		if !IsExpenseCurrency(currency) {
			return entity.MaterialReceiptInsert{}, fmt.Errorf("currency must be a supported currency or USDT when unit_cost is set")
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
		// Optional per-colourway attribution (gap-07 v2 C) — only for a run issue.
		if req.ProductId > 0 {
			ins.ProductId = nullInt32FromPb(req.ProductId)
		}
	} else {
		ins.SampleId = nullInt32FromPb(req.SampleId)
	}
	// Optional structured lot draw (gap-07 v2 D) — valid for either target.
	if req.LotId > 0 {
		ins.LotId = nullInt32FromPb(req.LotId)
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
		ProductId:       nullInt32Value(m.ProductId),
		LotId:           nullInt32Value(m.LotId),
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

// parseNonNegDecimal reads an optional proto decimal into a non-negative rounded quantity (absent → 0).
func parseNonNegDecimal(d *pb_decimal.Decimal, field string) (decimal.Decimal, error) {
	if d == nil || strings.TrimSpace(d.Value) == "" {
		return decimal.Zero, nil
	}
	v, err := decimal.NewFromString(d.Value)
	if err != nil {
		return decimal.Zero, fmt.Errorf("%s must be a number", field)
	}
	if v.IsNegative() {
		return decimal.Zero, fmt.Errorf("%s must be >= 0", field)
	}
	return v.Round(3), nil
}

// ConvertPbPackagingBomToEntity validates the packaging recipe write (gap-07 v2 B): each line needs a
// material_id, non-negative quantities and a non-zero total; a material_id may appear at most once.
func ConvertPbPackagingBomToEntity(items []*pb_admin.PackagingBomItem) ([]entity.PackagingBomItem, error) {
	out := make([]entity.PackagingBomItem, 0, len(items))
	seen := map[int32]bool{}
	for _, it := range items {
		if it == nil {
			continue
		}
		if it.MaterialId <= 0 {
			return nil, fmt.Errorf("packaging bom line needs a material_id")
		}
		if seen[it.MaterialId] {
			return nil, fmt.Errorf("packaging bom has duplicate material_id %d", it.MaterialId)
		}
		seen[it.MaterialId] = true
		perOrder, err := parseNonNegDecimal(it.QtyPerOrder, "qty_per_order")
		if err != nil {
			return nil, err
		}
		perItem, err := parseNonNegDecimal(it.QtyPerItem, "qty_per_item")
		if err != nil {
			return nil, err
		}
		if perOrder.IsZero() && perItem.IsZero() {
			return nil, fmt.Errorf("packaging bom material %d has no quantity", it.MaterialId)
		}
		out = append(out, entity.PackagingBomItem{
			MaterialId:  int(it.MaterialId),
			QtyPerOrder: perOrder,
			QtyPerItem:  perItem,
			Active:      it.Active,
		})
	}
	return out, nil
}

// PackagingBomItemToPb converts a stored packaging line to protobuf.
func PackagingBomItemToPb(it entity.PackagingBomItem) *pb_admin.PackagingBomItem {
	pb := &pb_admin.PackagingBomItem{
		MaterialId:   int32(it.MaterialId),
		MaterialName: it.MaterialName,
		QtyPerOrder:  pbDecimalFromDecimal(it.QtyPerOrder),
		QtyPerItem:   pbDecimalFromDecimal(it.QtyPerItem),
		Active:       it.Active,
	}
	if it.MaterialUnit.Valid {
		pb.MaterialUnit = it.MaterialUnit.String
	}
	return pb
}

// PackagingBomListToPb converts a slice of packaging lines to protobuf.
func PackagingBomListToPb(items []entity.PackagingBomItem) []*pb_admin.PackagingBomItem {
	out := make([]*pb_admin.PackagingBomItem, 0, len(items))
	for _, it := range items {
		out = append(out, PackagingBomItemToPb(it))
	}
	return out
}

// ConvertPbPackagingRecipeToEntity validates a packaging recipe write (PLM rework §2.8, Q3): each
// line needs a material_id, non-negative quantities and a non-zero total; a material_id may appear at
// most once. The scope target is validated in the handler and carried on the store call.
func ConvertPbPackagingRecipeToEntity(items []*pb_admin.PackagingRecipeItem) ([]entity.PackagingRecipeInsert, error) {
	out := make([]entity.PackagingRecipeInsert, 0, len(items))
	seen := map[int32]bool{}
	for _, it := range items {
		if it == nil {
			continue
		}
		if it.MaterialId <= 0 {
			return nil, fmt.Errorf("packaging recipe line needs a material_id")
		}
		if seen[it.MaterialId] {
			return nil, fmt.Errorf("packaging recipe has duplicate material_id %d", it.MaterialId)
		}
		seen[it.MaterialId] = true
		perOrder, err := parseNonNegDecimal(it.QtyPerOrder, "qty_per_order")
		if err != nil {
			return nil, err
		}
		perItem, err := parseNonNegDecimal(it.QtyPerItem, "qty_per_item")
		if err != nil {
			return nil, err
		}
		if perOrder.IsZero() && perItem.IsZero() {
			return nil, fmt.Errorf("packaging recipe material %d has no quantity", it.MaterialId)
		}
		out = append(out, entity.PackagingRecipeInsert{
			MaterialId:  int(it.MaterialId),
			QtyPerOrder: perOrder,
			QtyPerItem:  perItem,
			Active:      it.Active,
		})
	}
	return out, nil
}

// PackagingRecipeLineToPb converts a stored packaging recipe line to protobuf.
func PackagingRecipeLineToPb(pr entity.PackagingRecipe) *pb_admin.PackagingRecipeLine {
	pb := &pb_admin.PackagingRecipeLine{
		Id:           int32(pr.Id),
		Scope:        string(pr.Scope),
		MaterialId:   int32(pr.MaterialId),
		MaterialName: pr.MaterialName,
		QtyPerOrder:  pbDecimalFromDecimal(pr.QtyPerOrder),
		QtyPerItem:   pbDecimalFromDecimal(pr.QtyPerItem),
		Active:       pr.Active,
	}
	if pr.TechCardId.Valid {
		pb.TechCardId = pr.TechCardId.Int32
	}
	if pr.ProductId.Valid {
		pb.ProductId = pr.ProductId.Int32
	}
	if pr.MaterialUnit.Valid {
		pb.MaterialUnit = pr.MaterialUnit.String
	}
	return pb
}

// PackagingRecipeListToPb converts stored packaging recipe lines to protobuf.
func PackagingRecipeListToPb(items []entity.PackagingRecipe) []*pb_admin.PackagingRecipeLine {
	out := make([]*pb_admin.PackagingRecipeLine, 0, len(items))
	for _, it := range items {
		out = append(out, PackagingRecipeLineToPb(it))
	}
	return out
}

// MaterialLotToPb converts a stored lot (roll / dye-lot) to protobuf (gap-07 v2 D).
func MaterialLotToPb(l entity.MaterialLot) *pb_common.MaterialLot {
	pb := &pb_common.MaterialLot{
		Id:           int32(l.Id),
		MaterialId:   int32(l.MaterialId),
		LotCode:      l.LotCode,
		SupplierDoc:  l.SupplierDoc.String,
		ReceivedQty:  pbDecimalFromDecimal(l.ReceivedQty),
		RemainingQty: pbDecimalFromDecimal(l.RemainingQty),
		UnitCost:     pbDecimalFromNull(l.UnitCost),
		Currency:     l.Currency.String,
		Note:         l.Note.String,
		Archived:     l.Archived,
	}
	if l.ReceivedAt.Valid {
		pb.ReceivedAt = timestamppb.New(l.ReceivedAt.Time)
	}
	return pb
}

// MaterialLotListToPb converts a slice of lots to protobuf.
func MaterialLotListToPb(lots []entity.MaterialLot) []*pb_common.MaterialLot {
	out := make([]*pb_common.MaterialLot, 0, len(lots))
	for _, l := range lots {
		out = append(out, MaterialLotToPb(l))
	}
	return out
}
