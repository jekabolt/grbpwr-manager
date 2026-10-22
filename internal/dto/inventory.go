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
	if req.GetInvoiceFileId() < 0 {
		return entity.MaterialReceiptInsert{}, fmt.Errorf("invoice_file_id must be non-negative")
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
	inputVatAmount, inputVatRegime, err := convertInputVat(req.GetInputVatAmount(), req.GetInputVatRegime())
	if err != nil {
		return entity.MaterialReceiptInsert{}, err
	}
	// Roll facts recorded on the lot this receipt opens or tops up (Ф5а.1). A measured width of 0 is
	// not a measurement, it is an empty field that a decimal cannot express as absent — reject it
	// rather than storing a roll that is nought centimetres wide.
	measuredWidth, err := nullDecimalFromPb(req.GetMeasuredWidthCm())
	if err != nil {
		return entity.MaterialReceiptInsert{}, fmt.Errorf("measured_width_cm: %w", err)
	}
	if measuredWidth.Valid && !measuredWidth.Decimal.IsPositive() {
		return entity.MaterialReceiptInsert{}, fmt.Errorf("measured_width_cm must be positive when set")
	}
	// DECIMAL(6,2): MySQL silently rounds 148.456 to 148.46 and hands that back on the next read, so
	// the width the floor measured and the width the marker is made for would differ with nothing
	// saying so. Reject instead (limit 10000 = the four integer digits the column has).
	if err := validateDecimalScale(measuredWidth, "measured_width_cm", 2, 10000); err != nil {
		return entity.MaterialReceiptInsert{}, err
	}
	shadeCode := strings.TrimSpace(req.GetShadeCode())
	if len(shadeCode) > maxVarchar64 {
		return entity.MaterialReceiptInsert{}, fmt.Errorf("shade_code must be at most %d characters", maxVarchar64)
	}
	// Both facts live on the LOT, and there is no lot without a lot code: upsertLotOnReceipt returns
	// early on a blank one, so a measurement submitted without it would be accepted with a 200 and
	// then dropped on the floor — the worst possible answer, because the operator who measured the
	// roll has no way to find out it was not recorded. Refuse instead, and name the missing field.
	if strings.TrimSpace(req.GetLot()) == "" && (measuredWidth.Valid || shadeCode != "") {
		return entity.MaterialReceiptInsert{}, fmt.Errorf(
			"measured_width_cm/shade_code are recorded on the lot — set `lot` (the roll's lot code) to record them")
	}
	expectedAt, err := parseNullDate(req.GetExpectedAt())
	if err != nil {
		return entity.MaterialReceiptInsert{}, fmt.Errorf("expected_at: %w", err)
	}
	// Soft double-VAT guard (H-5): unit_cost is NET (VAT-exclusive), so the recoverable input VAT cannot
	// exceed the net line cost times the highest plausible rate. A larger value almost always means
	// unit_cost was entered GROSS — which double-counts the VAT (inflated inventory/COGS AND a 2080
	// recovery). Reject with a hint instead of silently booking it. (30% > the EU max standard rate.)
	if inputVatAmount.Valid && unitCost.Valid {
		net := unitCost.Decimal.Mul(qty)
		if inputVatAmount.Decimal.GreaterThan(net.Mul(decimal.RequireFromString("0.30"))) {
			return entity.MaterialReceiptInsert{}, fmt.Errorf(
				"input_vat_amount %s exceeds 30%% of the net line cost %s — unit_cost must be NET (VAT-exclusive)",
				inputVatAmount.Decimal.StringFixed(2), net.StringFixed(2))
		}
	}
	return entity.MaterialReceiptInsert{
		MaterialId:      int(req.MaterialId),
		Quantity:        qty,
		UnitCost:        unitCost,
		Currency:        currency,
		Lot:             nullStringFromPb(req.GetLot()),
		MeasuredWidthCm: measuredWidth,
		ShadeCode:       nullStringFromPb(shadeCode),
		SupplierDoc:     nullStringFromPb(req.GetSupplierDoc()),
		InvoiceFileId:   sql.NullInt32{Int32: req.GetInvoiceFileId(), Valid: req.GetInvoiceFileId() > 0},
		SupplierId:      sql.NullInt32{Int32: req.GetSupplierId(), Valid: req.GetSupplierId() > 0},
		ExpectedAt:      expectedAt,
		OccurredAt:      occurredAt,
		Comment:         nullStringFromPb(req.GetComment()),
		InputVatAmount:  inputVatAmount,
		InputVatRegime:  inputVatRegime,
	}, nil
}

const maxMaterialReceiptBatchLines = 50

type materialReceiptCostLine struct {
	Quantity decimal.Decimal
	UnitCost decimal.Decimal
}

type materialReceiptCostAllocation struct {
	LineNet        decimal.Decimal
	DeliveryShare  decimal.Decimal
	InputVatShare  decimal.Decimal
	LandedUnitCost decimal.Decimal
}

// allocateMaterialReceiptBatchCosts is the pure landed-cost allocator. Invoice delivery is kept at
// movement unit-cost precision (4 decimals), VAT at its ledger precision (2 decimals). Every share
// is rounded independently, then the complete residual is assigned to the largest net line so each
// set of shares sums exactly to the invoice total.
func allocateMaterialReceiptBatchCosts(lines []materialReceiptCostLine, deliveryCost, inputVatAmount decimal.Decimal) ([]materialReceiptCostAllocation, error) {
	if len(lines) == 0 {
		return nil, fmt.Errorf("at least one line is required")
	}
	weights := make([]decimal.Decimal, len(lines))
	totalWeight := decimal.Zero
	largest := 0
	for i, line := range lines {
		if line.Quantity.LessThanOrEqual(decimal.Zero) {
			return nil, fmt.Errorf("line %d quantity must be positive", i+1)
		}
		if line.UnitCost.IsNegative() {
			return nil, fmt.Errorf("line %d unit_cost must be non-negative", i+1)
		}
		weights[i] = line.Quantity.Mul(line.UnitCost)
		totalWeight = totalWeight.Add(weights[i])
		if weights[i].GreaterThan(weights[largest]) {
			largest = i
		}
	}
	if (deliveryCost.IsPositive() || inputVatAmount.IsPositive()) && !totalWeight.IsPositive() {
		return nil, fmt.Errorf("delivery_cost or input_vat_amount requires a positive line net value")
	}

	deliveryShares := allocateMaterialReceiptTotal(deliveryCost, weights, totalWeight, largest, 4)
	vatShares := allocateMaterialReceiptTotal(inputVatAmount, weights, totalWeight, largest, 2)
	out := make([]materialReceiptCostAllocation, len(lines))
	for i, line := range lines {
		out[i] = materialReceiptCostAllocation{
			LineNet:        weights[i],
			DeliveryShare:  deliveryShares[i],
			InputVatShare:  vatShares[i],
			LandedUnitCost: weights[i].Add(deliveryShares[i]).Div(line.Quantity).Round(4),
		}
	}
	return out, nil
}

func allocateMaterialReceiptTotal(total decimal.Decimal, weights []decimal.Decimal, totalWeight decimal.Decimal, largest, scale int) []decimal.Decimal {
	shares := make([]decimal.Decimal, len(weights))
	if total.IsZero() || totalWeight.IsZero() {
		return shares
	}
	allocated := decimal.Zero
	for i, weight := range weights {
		shares[i] = total.Mul(weight).Div(totalWeight).Round(int32(scale))
		allocated = allocated.Add(shares[i])
	}
	shares[largest] = shares[largest].Add(total.Sub(allocated))
	return shares
}

// ConvertPbReceiveMaterialStockBatch validates one invoice and allocates its shared delivery and VAT
// totals into receipt lines. AdminUsername is added by the handler after the costing/file guards.
func ConvertPbReceiveMaterialStockBatch(req *pb_admin.ReceiveMaterialStockBatchRequest) (entity.MaterialReceiptBatchInsert, error) {
	if req == nil || len(req.GetLines()) == 0 {
		return entity.MaterialReceiptBatchInsert{}, fmt.Errorf("at least one line is required")
	}
	if len(req.GetLines()) > maxMaterialReceiptBatchLines {
		return entity.MaterialReceiptBatchInsert{}, fmt.Errorf("at most %d lines are allowed", maxMaterialReceiptBatchLines)
	}
	if req.GetInvoiceFileId() < 0 {
		return entity.MaterialReceiptBatchInsert{}, fmt.Errorf("invoice_file_id must be non-negative")
	}
	if req.GetSupplierId() < 0 {
		return entity.MaterialReceiptBatchInsert{}, fmt.Errorf("supplier_id must be non-negative")
	}

	currency := strings.ToUpper(strings.TrimSpace(req.GetCurrency()))
	if currency != "" && !IsExpenseCurrency(currency) {
		return entity.MaterialReceiptBatchInsert{}, fmt.Errorf("currency must be a supported currency or USDT")
	}
	occurredAt, err := parseNullDate(req.GetOccurredAt())
	if err != nil {
		return entity.MaterialReceiptBatchInsert{}, fmt.Errorf("occurred_at: %w", err)
	}
	expectedAt, err := parseNullDate(req.GetExpectedAt())
	if err != nil {
		return entity.MaterialReceiptBatchInsert{}, fmt.Errorf("expected_at: %w", err)
	}
	inputVatAmount, inputVatRegime, err := convertInputVat(req.GetInputVatAmount(), req.GetInputVatRegime())
	if err != nil {
		return entity.MaterialReceiptBatchInsert{}, err
	}
	if err := validateDecimalScale(inputVatAmount, "input_vat_amount", 2, 10_000_000_000); err != nil {
		return entity.MaterialReceiptBatchInsert{}, err
	}
	deliveryCost, err := nullDecimalFromPb(req.GetDeliveryCost())
	if err != nil {
		return entity.MaterialReceiptBatchInsert{}, fmt.Errorf("delivery_cost: %w", err)
	}
	if deliveryCost.Valid {
		if err := validateDecimalScale(deliveryCost, "delivery_cost", 4, 100_000_000); err != nil {
			return entity.MaterialReceiptBatchInsert{}, err
		}
		if deliveryCost.Decimal.IsZero() {
			deliveryCost = decimal.NullDecimal{}
		}
	}

	costLines := make([]materialReceiptCostLine, len(req.GetLines()))
	unitCosts := make([]decimal.NullDecimal, len(req.GetLines()))
	seen := make(map[int32]struct{}, len(req.GetLines()))
	anyUncosted := false
	for i, line := range req.GetLines() {
		if line == nil || line.GetMaterialId() <= 0 {
			return entity.MaterialReceiptBatchInsert{}, fmt.Errorf("line %d material_id is required", i+1)
		}
		if _, duplicate := seen[line.GetMaterialId()]; duplicate {
			return entity.MaterialReceiptBatchInsert{}, fmt.Errorf("material_id %d appears more than once", line.GetMaterialId())
		}
		seen[line.GetMaterialId()] = struct{}{}
		qty, err := positiveDecimal(line.GetQuantity().GetValue(), fmt.Sprintf("line %d quantity", i+1))
		if err != nil {
			return entity.MaterialReceiptBatchInsert{}, err
		}
		if err := validateDecimalScale(decimal.NullDecimal{Decimal: qty, Valid: true}, fmt.Sprintf("line %d quantity", i+1), 3, 1_000_000_000); err != nil {
			return entity.MaterialReceiptBatchInsert{}, err
		}
		unitCost, err := nullDecimalFromPb(line.GetUnitCost())
		if err != nil {
			return entity.MaterialReceiptBatchInsert{}, fmt.Errorf("line %d unit_cost: %w", i+1, err)
		}
		if err := validateDecimalScale(unitCost, fmt.Sprintf("line %d unit_cost", i+1), 4, 100_000_000); err != nil {
			return entity.MaterialReceiptBatchInsert{}, err
		}
		if unitCost.Valid && !IsExpenseCurrency(currency) {
			return entity.MaterialReceiptBatchInsert{}, fmt.Errorf("currency must be a supported currency or USDT when unit_cost is set")
		}
		if !unitCost.Valid {
			anyUncosted = true
		}
		unitCosts[i] = unitCost
		costLines[i] = materialReceiptCostLine{Quantity: qty}
		if unitCost.Valid {
			costLines[i].UnitCost = unitCost.Decimal
		}
	}
	if anyUncosted && (deliveryCost.Valid || inputVatAmount.Valid) {
		return entity.MaterialReceiptBatchInsert{}, fmt.Errorf("delivery_cost or input_vat_amount requires every line to have unit_cost")
	}

	deliveryTotal := decimal.Zero
	if deliveryCost.Valid {
		deliveryTotal = deliveryCost.Decimal
	}
	vatTotal := decimal.Zero
	if inputVatAmount.Valid {
		vatTotal = inputVatAmount.Decimal
	}
	allocations, err := allocateMaterialReceiptBatchCosts(costLines, deliveryTotal, vatTotal)
	if err != nil {
		return entity.MaterialReceiptBatchInsert{}, err
	}

	shared := entity.MaterialReceiptInsert{
		Currency:       currency,
		SupplierDoc:    nullStringFromPb(req.GetSupplierDoc()),
		InvoiceFileId:  sql.NullInt32{Int32: req.GetInvoiceFileId(), Valid: req.GetInvoiceFileId() > 0},
		SupplierId:     sql.NullInt32{Int32: req.GetSupplierId(), Valid: req.GetSupplierId() > 0},
		ExpectedAt:     expectedAt,
		OccurredAt:     occurredAt,
		Comment:        nullStringFromPb(req.GetComment()),
		InputVatRegime: inputVatRegime,
	}
	out := entity.MaterialReceiptBatchInsert{Lines: make([]entity.MaterialReceiptInsert, len(req.GetLines()))}
	for i, line := range req.GetLines() {
		ins := shared
		ins.MaterialId = int(line.GetMaterialId())
		ins.Quantity = costLines[i].Quantity
		ins.Lot = nullStringFromPb(line.GetLot())
		if unitCosts[i].Valid {
			ins.UnitCost = decimal.NullDecimal{Decimal: allocations[i].LandedUnitCost, Valid: true}
		}
		if inputVatAmount.Valid {
			ins.InputVatAmount = decimal.NullDecimal{Decimal: allocations[i].InputVatShare, Valid: true}
			if allocations[i].InputVatShare.GreaterThan(allocations[i].LineNet.Mul(decimal.RequireFromString("0.30"))) {
				return entity.MaterialReceiptBatchInsert{}, fmt.Errorf(
					"line %d input VAT share %s exceeds 30%% of its net line cost %s — unit_cost must be NET (VAT-exclusive)",
					i+1, allocations[i].InputVatShare.StringFixed(2), allocations[i].LineNet.StringFixed(2))
			}
		}
		out.Lines[i] = ins
	}
	return out, nil
}

// convertInputVat validates and converts a receipt's recoverable input VAT (phase 2, wave 1,
// docs/plan-accounting-phase2/01-wave1-vat.md §1.4): amount must be non-negative, regime must be one
// of entity.ValidInputVatRegimes, and the two are all-or-nothing — a receipt either records both or
// neither (a VAT amount with no treatment, or a treatment with no amount, cannot be posted by the M1
// rule, so it is rejected here rather than silently dropped).
func convertInputVat(pbAmount *pb_decimal.Decimal, rawRegime string) (decimal.NullDecimal, sql.NullString, error) {
	amount, err := nullDecimalFromPb(pbAmount)
	if err != nil {
		return decimal.NullDecimal{}, sql.NullString{}, fmt.Errorf("input_vat_amount: %w", err)
	}
	if amount.Valid && amount.Decimal.IsNegative() {
		return decimal.NullDecimal{}, sql.NullString{}, fmt.Errorf("input_vat_amount must be non-negative")
	}
	regime := strings.ToLower(strings.TrimSpace(rawRegime))
	if regime != "" && !entity.ValidInputVatRegimes[entity.InputVatRegime(regime)] {
		return decimal.NullDecimal{}, sql.NullString{}, fmt.Errorf("invalid input_vat_regime %q", rawRegime)
	}
	if amount.Valid != (regime != "") {
		return decimal.NullDecimal{}, sql.NullString{}, fmt.Errorf("input_vat_amount and input_vat_regime must be set together")
	}
	if regime == "" {
		return decimal.NullDecimal{}, sql.NullString{}, nil
	}
	return amount, sql.NullString{String: regime, Valid: true}, nil
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

// ConvertPbBatchIssueMaterialStock validates and converts an atomic issue/return request.
func ConvertPbBatchIssueMaterialStock(req *pb_admin.BatchIssueMaterialStockRequest) (entity.MaterialBatchIssueInsert, error) {
	if req == nil || len(req.GetLines()) == 0 {
		return entity.MaterialBatchIssueInsert{}, fmt.Errorf("at least one line is required")
	}
	hasRun := req.GetProductionRunId() > 0
	hasSample := req.GetSampleId() > 0
	if hasRun == hasSample {
		return entity.MaterialBatchIssueInsert{}, fmt.Errorf("exactly one of production_run_id / sample_id must be set")
	}
	occurredAt, err := parseNullDate(req.GetOccurredAt())
	if err != nil {
		return entity.MaterialBatchIssueInsert{}, fmt.Errorf("occurred_at: %w", err)
	}

	ins := entity.MaterialBatchIssueInsert{
		IsReturn:   req.GetIsReturn(),
		OccurredAt: occurredAt,
		Lines:      make([]entity.MaterialBatchIssueLine, 0, len(req.GetLines())),
	}
	if hasRun {
		ins.ProductionRunId = nullInt32FromPb(req.GetProductionRunId())
		if req.GetProductId() > 0 {
			ins.ProductId = nullInt32FromPb(req.GetProductId())
		}
	} else {
		ins.SampleId = nullInt32FromPb(req.GetSampleId())
	}

	seen := make(map[int32]int, len(req.GetLines()))
	for i, line := range req.GetLines() {
		materialID := line.GetMaterialId()
		if materialID <= 0 {
			return entity.MaterialBatchIssueInsert{}, fmt.Errorf("line %d: material_id is required", i+1)
		}
		if first, ok := seen[materialID]; ok {
			return entity.MaterialBatchIssueInsert{}, fmt.Errorf("line %d (material %d): duplicate material_id (first used on line %d)", i+1, materialID, first)
		}
		seen[materialID] = i + 1

		qty, err := positiveDecimal(line.GetQuantity().GetValue(), "quantity")
		if err != nil {
			return entity.MaterialBatchIssueInsert{}, fmt.Errorf("line %d (material %d): %w", i+1, materialID, err)
		}
		comment := line.GetComment()
		if comment == "" {
			comment = req.GetComment()
		}
		entityLine := entity.MaterialBatchIssueLine{
			MaterialId: int(materialID),
			Quantity:   qty,
			Comment:    nullStringFromPb(comment),
		}
		if line.GetLotId() > 0 {
			entityLine.LotId = nullInt32FromPb(line.GetLotId())
		}
		ins.Lines = append(ins.Lines, entityLine)
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
		InvoiceFileId:   nullInt32Value(m.InvoiceFileId),
		InvoiceFileName: m.InvoiceFileName.String,
		Reason:          m.Reason.String,
		Comment:         m.Comment.String,
		AdminUsername:   m.AdminUsername,
		CreatedAt:       timestamppb.New(m.CreatedAt),
	}
	if m.OccurredAt.Valid {
		out.OccurredAt = timestamppb.New(m.OccurredAt.Time)
	} else if !m.CreatedAt.IsZero() {
		// Defensive: every movement is dated at insert and the occurred-at backfill migration filled the
		// legacy NULLs, but a row written by an older binary during a deploy overlap could still be
		// undated — show the UTC day it was recorded (as the backfill does) rather than a blank date.
		c := m.CreatedAt.UTC()
		out.OccurredAt = timestamppb.New(time.Date(c.Year(), c.Month(), c.Day(), 0, 0, 0, 0, time.UTC))
	}
	if m.ExpectedAt.Valid {
		out.ExpectedAt = timestamppb.New(m.ExpectedAt.Time)
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
		// Ф5а.1: the measured roll facts. Absent stays absent — an unmeasured roll must not read as
		// "0 cm wide" or as agreeing with the article's nominal width.
		MeasuredWidthCm: pbDecimalFromNull(l.MeasuredWidthCm),
		ShadeCode:       l.ShadeCode.String,
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
