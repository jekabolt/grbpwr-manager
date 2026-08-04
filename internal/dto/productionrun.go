package dto

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/cache"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/shopspring/decimal"
	pb_decimal "google.golang.org/genproto/googleapis/type/decimal"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// productionRunStatusPbToEntity maps the proto status enum to the stored string.
var productionRunStatusPbToEntity = map[pb_common.ProductionRunStatus]entity.ProductionRunStatus{
	pb_common.ProductionRunStatus_PRODUCTION_RUN_STATUS_PLANNED:            entity.ProductionRunPlanned,
	pb_common.ProductionRunStatus_PRODUCTION_RUN_STATUS_IN_PROGRESS:        entity.ProductionRunInProgress,
	pb_common.ProductionRunStatus_PRODUCTION_RUN_STATUS_RECEIVED:           entity.ProductionRunReceived,
	pb_common.ProductionRunStatus_PRODUCTION_RUN_STATUS_CLOSED:             entity.ProductionRunClosed,
	pb_common.ProductionRunStatus_PRODUCTION_RUN_STATUS_CANCELLED:          entity.ProductionRunCancelled,
	pb_common.ProductionRunStatus_PRODUCTION_RUN_STATUS_PARTIALLY_RECEIVED: entity.ProductionRunPartiallyReceived,
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
// planned-cost snapshot is NOT taken from the client — the service layer sets it separately. Neither
// is received_at: it is the timestamp of a physical receipt, stamped only by the receive flow beside
// the stock it books. A client-writable received_at let an open run be back-dated into the
// accounting scan (which reads received_at, not status) with no stock movement behind it.
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
	if len(pb.MarkerNotes) > maxVarchar1024 {
		return nil, fmt.Errorf("marker_notes must be at most %d characters", maxVarchar1024)
	}
	markerEff, err := nullDecimalFromPb(pb.MarkerEfficiencyPct)
	if err != nil {
		return nil, fmt.Errorf("marker_efficiency_pct: %w", err)
	}
	if markerEff.Valid && (markerEff.Decimal.IsNegative() || markerEff.Decimal.GreaterThan(decimal.NewFromInt(100))) {
		return nil, fmt.Errorf("marker_efficiency_pct must be between 0 and 100")
	}
	actualWastage, err := nullDecimalFromPb(pb.ActualWastagePercent)
	if err != nil {
		return nil, fmt.Errorf("actual_wastage_percent: %w", err)
	}
	if actualWastage.Valid && (actualWastage.Decimal.IsNegative() || actualWastage.Decimal.GreaterThan(decimal.NewFromInt(100))) {
		return nil, fmt.Errorf("actual_wastage_percent must be between 0 and 100")
	}
	lines, err := convertPbProductionRunLines(pb.Lines)
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
		// Planning dates ARE taken from the client, unlike received_at above: they express what the
		// operator intends, and nothing books stock or accrues cost from them.
		PlannedStartAt:       nullTimeFromPbTimestamp(pb.PlannedStartAt),
		PromisedAt:           nullTimeFromPbTimestamp(pb.PromisedAt),
		MarkerEfficiencyPct:  markerEff,
		MarkerNotes:          nullStringFromPb(pb.MarkerNotes),
		ActualWastagePercent: actualWastage,
		Notes:                nullStringFromPb(pb.Notes),
		Lines:                lines,
		Costs:                costs,
	}, nil
}

// nonNegNullDecimal converts an optional pb decimal, rejecting a negative value.
func nonNegNullDecimal(d *pb_decimal.Decimal, field string) (decimal.NullDecimal, error) {
	v, err := nullDecimalFromPb(d)
	if err != nil {
		return decimal.NullDecimal{}, fmt.Errorf("%s: %w", field, err)
	}
	if v.Valid && v.Decimal.IsNegative() {
		return decimal.NullDecimal{}, fmt.Errorf("%s must be non-negative", field)
	}
	return v, nil
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
		if !IsExpenseCurrency(currency) {
			return nil, fmt.Errorf("production run cost: currency must be a supported currency or USDT")
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

// PreserveProductionRunCostBases carries each STORED article's amount_base onto the incoming article
// that replaces it, whenever kind, amount and currency are all unchanged. An update full-replaces a
// run's cost rows, so an article the client re-sent without its amount_base was re-folded at TODAY's
// rate: an expense of USD 1000 booked in March at 0.92 quietly became a different euro number in
// June, moving the run's actual cost and the variance against it long after the money was spent.
// amount_base is a FACT about a payment, not a live conversion.
//
// Only an unset incoming base is filled — a caller-supplied one is a deliberate manual override and
// FoldProductionRunCostsToBase already respects it. A changed amount or currency is a different
// payment and is re-folded as before. Rows are matched as a multiset (each stored base is handed out
// once), because production_run_cost has no natural key to join on.
func PreserveProductionRunCostBases(incoming, stored []entity.ProductionRunCost) {
	if len(incoming) == 0 || len(stored) == 0 {
		return
	}
	type key struct {
		kind     entity.ProductionRunCostKind
		currency string
		amount   string
	}
	k := func(c entity.ProductionRunCost) key {
		return key{kind: c.Kind, currency: strings.ToUpper(strings.TrimSpace(c.Currency)), amount: c.Amount.String()}
	}
	bases := make(map[key][]decimal.Decimal, len(stored))
	for _, c := range stored {
		if c.AmountBase.Valid {
			bases[k(c)] = append(bases[k(c)], c.AmountBase.Decimal)
		}
	}
	for i := range incoming {
		if incoming[i].AmountBase.Valid {
			continue
		}
		kk := k(incoming[i])
		if avail := bases[kk]; len(avail) > 0 {
			incoming[i].AmountBase = decimal.NullDecimal{Decimal: avail[0], Valid: true}
			bases[kk] = avail[1:]
		}
	}
}

func convertPbProductionRunLines(pbs []*pb_common.ProductionRunLine) ([]entity.ProductionRunLine, error) {
	if len(pbs) == 0 {
		return nil, nil
	}
	// A (product_id, size_id) pair must be unique — product_id 0 (unset) collapses to one planning
	// bucket per size, matching the DB uniq_prl (NULLs there are distinct, but a duplicate NULL+size
	// on the API is a client mistake worth rejecting early).
	type key struct{ product, size int }
	seen := make(map[key]struct{}, len(pbs))
	// line_key is the row's stable identity (migration 0230): it must be unique within the payload,
	// or the store's keyed diff would silently collapse two submitted lines onto one row.
	seenKeys := make(map[string]struct{}, len(pbs))
	out := make([]entity.ProductionRunLine, 0, len(pbs))
	for _, ln := range pbs {
		if ln == nil {
			continue
		}
		if ln.SizeId < 0 {
			return nil, fmt.Errorf("production run line: size_id must not be negative")
		}
		// A size is required exactly when the line names a product: a garment line books into
		// product_size stock, so it must say which size. A product-less line may omit the size —
		// that is the auxiliary run's single output line (a dust bag has no size grade), whose save
		// this check used to 400 unconditionally, making aux planning unusable since it shipped.
		if ln.SizeId == 0 && ln.ProductId > 0 {
			return nil, fmt.Errorf("production run line: size_id is required on a line with a product")
		}
		if ln.ProductId < 0 {
			return nil, fmt.Errorf("production run line: product_id must not be negative")
		}
		k := key{product: int(ln.ProductId), size: int(ln.SizeId)}
		if _, dup := seen[k]; dup {
			return nil, fmt.Errorf("production run line: duplicate product_id %d / size_id %d", ln.ProductId, ln.SizeId)
		}
		seen[k] = struct{}{}
		if ln.PlannedQty < 0 {
			return nil, fmt.Errorf("production run line: planned_qty must be non-negative")
		}
		// A keyless line is a new line (or a client that predates 0230): mint its identity here, so
		// the run reads back with a key on every line and the NEXT save can be diffed by it.
		lineKey := strings.TrimSpace(ln.LineKey)
		if lineKey == "" {
			minted, err := entity.MintProductionRunLineKey()
			if err != nil {
				return nil, fmt.Errorf("production run line: %w", err)
			}
			lineKey = minted
		} else if !entity.IsValidProductionRunLineKey(lineKey) {
			return nil, fmt.Errorf("production run line: line_key must be %d uppercase alphanumeric characters, got %q",
				entity.ProductionRunLineKeyLen, ln.LineKey)
		}
		if _, dup := seenKeys[lineKey]; dup {
			return nil, fmt.Errorf("production run line: duplicate line_key %q", lineKey)
		}
		seenKeys[lineKey] = struct{}{}
		e := entity.ProductionRunLine{LineKey: lineKey, SizeId: int(ln.SizeId), PlannedQty: int(ln.PlannedQty)}
		if ln.ProductId > 0 {
			e.ProductId = sql.NullInt32{Int32: ln.ProductId, Valid: true}
		}
		if ln.ReceivedQty != nil {
			if *ln.ReceivedQty < 0 {
				return nil, fmt.Errorf("production run line: received_qty must be non-negative")
			}
			e.ReceivedQty = sql.NullInt64{Int64: int64(*ln.ReceivedQty), Valid: true}
		}
		if ln.DefectQty != nil {
			if *ln.DefectQty < 0 {
				return nil, fmt.Errorf("production run line: defect_qty must be non-negative")
			}
			e.DefectQty = sql.NullInt64{Int64: int64(*ln.DefectQty), Valid: true}
		}
		// NOTE: whether a received line needs a product depends on the card's purpose (a sellable run
		// books into a product; an AUXILIARY run's output goes to the material warehouse with no
		// product). The dto cannot see the purpose, so this is enforced in the receive handlers:
		// ReceiveProductionRun requires a product per received line, receiveAuxiliaryRun forbids one.
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
			TechCardId:           int32(r.TechCardId),
			ReleaseId:            int32(r.ReleaseId.Int64),
			Status:               productionRunStatusEntityToPb[r.Status],
			StartedAt:            pbTimestampFromNullTime(r.StartedAt),
			ReceivedAt:           pbTimestampFromNullTime(r.ReceivedAt),
			PlannedStartAt:       pbTimestampFromNullTime(r.PlannedStartAt),
			PromisedAt:           pbTimestampFromNullTime(r.PromisedAt),
			MarkerEfficiencyPct:  pbDecimalFromNull(r.MarkerEfficiencyPct),
			MarkerNotes:          pbStringFromNull(r.MarkerNotes),
			ActualWastagePercent: pbDecimalFromNull(r.ActualWastagePercent),
			Notes:                pbStringFromNull(r.Notes),
			Lines:                productionRunLinesToPb(r.Lines),
			Costs:                productionRunCostsToPb(r.Costs),
		},
		PlannedUnitCost: pbDecimalFromNull(r.PlannedUnitCost),
		PlannedCurrency: pbStringFromNull(r.PlannedCurrency),
		CreatedAt:       timestamppb.New(r.CreatedAt),
		UpdatedAt:       timestamppb.New(r.UpdatedAt),
		Actuals:         computeProductionRunActuals(r),
		LockVersion:     int32(r.LockVersion),
		Receipts:        productionRunReceiptsToPb(r.Receipts),
	}
}

// productionRunReceiptsToPb maps a run's receiving events onto the wire. Money (the frozen
// valuation) rides here and is stripped by stripProductionRunCosting without costing:read.
func productionRunReceiptsToPb(receipts []entity.ProductionRunReceipt) []*pb_common.ProductionRunReceipt {
	out := make([]*pb_common.ProductionRunReceipt, 0, len(receipts))
	for i := range receipts {
		r := &receipts[i]
		lines := make([]*pb_common.ProductionRunReceiptLine, 0, len(r.Lines))
		for _, l := range r.Lines {
			lines = append(lines, &pb_common.ProductionRunReceiptLine{
				LineKey:   l.LineKey,
				ProductId: l.ProductId.Int32,
				SizeId:    l.SizeId.Int32,
				GoodQty:   int32(l.GoodQty),
				DefectQty: int32(l.DefectQty),
			})
		}
		out = append(out, &pb_common.ProductionRunReceipt{
			Id:            int32(r.Id),
			RunId:         int32(r.RunId),
			ReceivedAt:    timestamppb.New(r.ReceivedAt),
			AdminUsername: pbStringFromNull(r.AdminUsername),
			Note:          pbStringFromNull(r.Note),
			UnitCostBase:  pbDecimalFromNull(r.UnitCostBase),
			BaseCurrency:  pbStringFromNull(r.BaseCurrency),
			HasBase:       r.HasBase,
			Lines:         lines,
			CreatedAt:     timestamppb.New(r.CreatedAt),
			Final:         r.Final,
			PostingStatus: r.PostingStatus,
			ReversalOf:    r.ReversalOf.Int32,
			ReversedBy:    r.ReversedBy.Int32,
		})
	}
	return out
}

// ConvertPbReceiptLinesToEntity validates and converts the receipt command's line inputs: every
// line_key must be shaped like a real line key, unique within the payload, and the counts
// non-negative. (Whether the key names a real plan line is the store's decision, under the lock.)
func ConvertPbReceiptLinesToEntity(pbs []*pb_admin.PostProductionRunReceiptLineInput) ([]entity.ProductionRunReceiptLineInput, error) {
	out := make([]entity.ProductionRunReceiptLineInput, 0, len(pbs))
	seen := make(map[string]struct{}, len(pbs))
	for _, ln := range pbs {
		if ln == nil {
			continue
		}
		key := strings.TrimSpace(ln.LineKey)
		if !entity.IsValidProductionRunLineKey(key) {
			return nil, fmt.Errorf("receipt line: line_key must be %d uppercase alphanumeric characters, got %q",
				entity.ProductionRunLineKeyLen, ln.LineKey)
		}
		if _, dup := seen[key]; dup {
			return nil, fmt.Errorf("receipt line: line_key %q submitted twice", key)
		}
		seen[key] = struct{}{}
		if ln.GoodQty < 0 || ln.DefectQty < 0 {
			return nil, fmt.Errorf("receipt line %q: quantities must be non-negative", key)
		}
		out = append(out, entity.ProductionRunReceiptLineInput{
			LineKey:   key,
			GoodQty:   int(ln.GoodQty),
			DefectQty: int(ln.DefectQty),
		})
	}
	return out, nil
}

// HashProductionRunReceiptPayload is the canonical request hash of the receipt command: SHA-256
// over the run, the SORTED counted lines, the note, the cost_price flag and the FINAL flag (a
// partial and a final with the same counts are different intents — replaying one as the other
// would silently close or reopen the receipt series). expected_lock_version is deliberately
// EXCLUDED — the idempotency key identifies the operator's intent ("receive these counts"), and
// the lock version is a concurrency token, not intent: a retry of the same count after a refetch
// must replay, not die on AlreadyExists.
func HashProductionRunReceiptPayload(runID int, lines []entity.ProductionRunReceiptLineInput, note string, updateCostPrice, final bool) string {
	sorted := make([]entity.ProductionRunReceiptLineInput, len(lines))
	copy(sorted, lines)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].LineKey < sorted[j].LineKey })
	h := sha256.New()
	fmt.Fprintf(h, "run=%d;note=%q;cost=%t;final=%t", runID, note, updateCostPrice, final)
	for _, l := range sorted {
		fmt.Fprintf(h, ";%s=%d/%d", l.LineKey, l.GoodQty, l.DefectQty)
	}
	return hex.EncodeToString(h.Sum(nil))
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
// colour-model × size lines. Base amounts come from cost.AmountBase (already folded on write); a
// cost with no base leaves has_base=false so the caller knows the total is partial. Quantities are
// summed across ALL lines (every colourway of the batch). Ratios are guarded against division by
// zero and only emitted when their inputs are present.
func computeProductionRunActuals(r *entity.ProductionRun) *pb_common.ProductionRunActuals {
	var plannedQty, receivedQty, defectQty int64
	for _, ln := range r.Lines {
		plannedQty += int64(ln.PlannedQty)
		if ln.ReceivedQty.Valid {
			receivedQty += ln.ReceivedQty.Int64
		}
		if ln.DefectQty.Valid {
			defectQty += ln.DefectQty.Int64
		}
	}

	manualBase := decimal.Zero
	hasBase := true
	hasManualMaterials := false
	byKind := make(map[entity.ProductionRunCostKind]decimal.Decimal)
	for _, c := range r.Costs {
		if c.Kind == entity.ProductionRunCostMaterials {
			hasManualMaterials = true
		}
		if !c.AmountBase.Valid {
			hasBase = false
			continue
		}
		manualBase = manualBase.Add(c.AmountBase.Decimal)
		byKind[c.Kind] = byKind[c.Kind].Add(c.AmountBase.Decimal)
	}

	// Materials issued from the warehouse (NF-06): issues add cost, returns give it back. An issue
	// with no frozen average is skipped and flagged (the figure then understates). Per-colourway
	// (gap-07 v2 C): the same issues are also bucketed by the product_id they were cut for; issues
	// with no product_id fall into the unattributed bucket, never a colourway.
	materialsFromStock := decimal.Zero
	hasStockIssues, hasUncostedIssues := false, false
	perColorway := map[int32]decimal.Decimal{}
	perColorwayUncosted := map[int32]bool{}
	unattributed := decimal.Zero
	addColorway := func(pid sql.NullInt32, v decimal.Decimal, costed bool) {
		if !pid.Valid || pid.Int32 <= 0 {
			if costed {
				unattributed = unattributed.Add(v)
			}
			return
		}
		if costed {
			perColorway[pid.Int32] = perColorway[pid.Int32].Add(v)
		} else {
			perColorwayUncosted[pid.Int32] = true
		}
	}
	for _, m := range r.MaterialMovements {
		switch m.MovementType {
		case entity.MaterialMovementIssueProduction:
			hasStockIssues = true
			if m.UnitCostBase.Valid {
				v := m.Quantity.Mul(m.UnitCostBase.Decimal)
				materialsFromStock = materialsFromStock.Add(v)
				addColorway(m.ProductId, v, true)
			} else {
				hasUncostedIssues = true
				addColorway(m.ProductId, decimal.Zero, false)
			}
		case entity.MaterialMovementReturnProduction:
			if m.UnitCostBase.Valid {
				v := m.Quantity.Mul(m.UnitCostBase.Decimal)
				materialsFromStock = materialsFromStock.Sub(v)
				addColorway(m.ProductId, v.Neg(), true)
			}
		}
	}
	totalBase := manualBase.Add(materialsFromStock)

	out := &pb_common.ProductionRunActuals{
		ActualTotalBase:        pbDecimalFromDecimal(roundMoney(totalBase)),
		BaseCurrency:           cache.GetBaseCurrency(),
		PlannedQtyTotal:        int32(plannedQty),
		ReceivedQtyTotal:       int32(receivedQty),
		DefectQtyTotal:         int32(defectQty),
		HasBase:                hasBase,
		MaterialsFromStockBase: pbDecimalFromDecimal(roundMoney(materialsFromStock)),
		MixedMaterialsSources:  hasStockIssues && hasManualMaterials,
		HasUncostedIssues:      hasUncostedIssues,
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

	// plan/fact against the run's frozen planned unit cost, scaled to the received quantity — only
	// when that snapshot is in the base currency the actuals are measured in (plannedCostInBase).
	if plannedCostInBase(r) && receivedQty > 0 {
		plannedTotal := r.PlannedUnitCost.Decimal.Mul(recv)
		out.PlannedTotalBase = pbDecimalFromDecimal(roundMoney(plannedTotal))
		out.TotalVariance = pbDecimalFromDecimal(roundMoney(totalBase.Sub(plannedTotal)))
		if haveUnit {
			out.UnitCostVariance = pbDecimalFromDecimal(roundMoney(actualUnit.Sub(r.PlannedUnitCost.Decimal)))
		}
	}

	// Per-colourway material breakdown (gap-07 v2 C): emit a row for every product that has attributed
	// materials, an uncosted issue, or received units. Rows appear only once issues/lines carry a
	// product_id, so a legacy single-colour run stays empty here.
	out.UnattributedMaterialsBase = pbDecimalFromDecimal(roundMoney(unattributed))
	receivedByProduct := map[int32]int64{}
	for _, ln := range r.Lines {
		if ln.ProductId.Valid && ln.ReceivedQty.Valid {
			receivedByProduct[ln.ProductId.Int32] += ln.ReceivedQty.Int64
		}
	}
	pidSet := map[int32]bool{}
	for pid := range perColorway {
		pidSet[pid] = true
	}
	for pid := range perColorwayUncosted {
		pidSet[pid] = true
	}
	for pid := range receivedByProduct {
		pidSet[pid] = true
	}
	pids := make([]int32, 0, len(pidSet))
	for pid := range pidSet {
		pids = append(pids, pid)
	}
	sort.Slice(pids, func(i, j int) bool { return pids[i] < pids[j] })
	for _, pid := range pids {
		mat := perColorway[pid] // zero when only uncosted/received
		cw := &pb_common.ProductionRunColorwayCost{
			ProductId:              pid,
			ReceivedQty:            int32(receivedByProduct[pid]),
			MaterialsFromStockBase: pbDecimalFromDecimal(roundMoney(mat)),
			HasUncosted:            perColorwayUncosted[pid],
		}
		if rq := receivedByProduct[pid]; rq > 0 {
			cw.MaterialsUnitCost = pbDecimalFromDecimal(roundMoney(mat.Div(decimal.NewFromInt(rq))))
		}
		out.ByColorway = append(out.ByColorway, cw)
	}
	return out
}

func productionRunLinesToPb(lines []entity.ProductionRunLine) []*pb_common.ProductionRunLine {
	out := make([]*pb_common.ProductionRunLine, 0, len(lines))
	for _, ln := range lines {
		pb := &pb_common.ProductionRunLine{
			LineKey: ln.LineKey, SizeId: int32(ln.SizeId), PlannedQty: int32(ln.PlannedQty)}
		if ln.ProductId.Valid {
			pb.ProductId = ln.ProductId.Int32
		}
		if ln.ReceivedQty.Valid {
			v := int32(ln.ReceivedQty.Int64)
			pb.ReceivedQty = &v
		}
		if ln.DefectQty.Valid {
			v := int32(ln.DefectQty.Int64)
			pb.DefectQty = &v
		}
		out = append(out, pb)
	}
	return out
}

// plannedCostInBase reports whether a run's frozen planned unit cost may be subtracted from its
// actuals. Actual cost is ALWAYS in the base currency (every article is folded on write), while
// planned_unit_cost is a snapshot of the tech-card costing and can be in the costing currency —
// planned_currency records which. Nothing read it, so a PLN 142.50 plan against a EUR 30 actual
// reported a −112.50 "saving" that was pure FX. A snapshot in any other currency (or with no
// currency recorded at all, which is unverifiable) yields no variance rather than a fictional one;
// planned_unit_cost / planned_currency still travel on the wire, so the client can say why.
func plannedCostInBase(r *entity.ProductionRun) bool {
	return r.PlannedUnitCost.Valid && r.PlannedCurrency.Valid &&
		strings.EqualFold(strings.TrimSpace(r.PlannedCurrency.String), cache.GetBaseCurrency())
}

// ProductionRunActualUnitCostBase returns the run's actual unit cost in the base currency, valid
// only when it is trustworthy for setting cost_price. The math lives on the entity so the store can
// compute it identically inside the receive transaction; this is a thin delegate for dto callers.
func ProductionRunActualUnitCostBase(r *entity.ProductionRun) decimal.NullDecimal {
	return r.ActualUnitCostBase()
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
