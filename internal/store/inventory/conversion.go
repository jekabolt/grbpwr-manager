package inventory

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/cache"
	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
	"github.com/shopspring/decimal"
)

type conversionCostInput struct {
	Quantity decimal.Decimal
	Avg      decimal.Decimal
}

type conversionCost struct {
	InputsCostBase     decimal.Decimal
	InputsValueBase    decimal.Decimal
	TotalCostBase      decimal.Decimal
	OutputUnitCostBase decimal.Decimal
}

// computeConversionCost is the exact, side-effect-free money path. Input cost stays unrounded for
// total/unit-cost arithmetic; InputsValueBase independently sums each movement builder's round-2
// value so the conversion's 1120 postings net exactly. Only the final output unit cost is round-4.
func computeConversionCost(inputs []conversionCostInput, workBase, outputQty decimal.Decimal) (conversionCost, error) {
	if len(inputs) == 0 || outputQty.LessThanOrEqual(decimal.Zero) || workBase.IsNegative() {
		return conversionCost{}, entity.ErrMaterialConversionInvalid
	}
	var out conversionCost
	for _, input := range inputs {
		if input.Quantity.LessThanOrEqual(decimal.Zero) || input.Avg.IsNegative() {
			return conversionCost{}, entity.ErrMaterialConversionInvalid
		}
		value := input.Quantity.Mul(input.Avg)
		out.InputsCostBase = out.InputsCostBase.Add(value)
		out.InputsValueBase = out.InputsValueBase.Add(value.Round(2))
	}
	out.TotalCostBase = out.InputsCostBase.Add(workBase)
	if out.TotalCostBase.IsZero() {
		out.OutputUnitCostBase = decimal.Zero
	} else {
		out.OutputUnitCostBase = out.TotalCostBase.Div(outputQty).Round(avgScale)
	}

	spread := outputQty.Mul(out.OutputUnitCostBase).Sub(out.TotalCostBase).Abs()
	tolerance := decimal.RequireFromString("0.01")
	relative := out.TotalCostBase.Abs().Mul(decimal.RequireFromString("0.001"))
	if relative.GreaterThan(tolerance) {
		tolerance = relative
	}
	if spread.GreaterThan(tolerance) {
		return conversionCost{}, fmt.Errorf("%w: rounded output differs by %s (limit %s)",
			entity.ErrMaterialConversionPrecision, spread.String(), tolerance.String())
	}
	return out, nil
}

// ConvertMaterialStock atomically consumes one to five materials and produces one existing
// material. Every stock/lot/header/movement/catalog write uses the same repository transaction.
func (s *Store) ConvertMaterialStock(ctx context.Context, ins entity.MaterialConversionInsert) (*entity.MaterialConversion, error) {
	if err := validateConversionShape(ins); err != nil {
		return nil, err
	}
	var out *entity.MaterialConversion
	err := s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		var err error
		out, err = convertMaterialStockInTx(ctx, rep, ins, s.Now())
		return err
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func validateConversionShape(ins entity.MaterialConversionInsert) error {
	if len(ins.Inputs) < 1 || len(ins.Inputs) > 5 || ins.Output.MaterialId <= 0 ||
		ins.Output.Quantity.LessThanOrEqual(decimal.Zero) || ins.WorkCost.IsNegative() {
		return entity.ErrMaterialConversionInvalid
	}
	seen := make(map[int]bool, len(ins.Inputs))
	for _, input := range ins.Inputs {
		if input.MaterialId <= 0 || input.Quantity.LessThanOrEqual(decimal.Zero) || seen[input.MaterialId] ||
			input.MaterialId == ins.Output.MaterialId || (input.LossQty.Valid &&
			(input.LossQty.Decimal.IsNegative() || input.LossQty.Decimal.GreaterThan(input.Quantity))) {
			return entity.ErrMaterialConversionInvalid
		}
		seen[input.MaterialId] = true
	}
	if ins.WorkCost.IsPositive() && strings.TrimSpace(ins.WorkCurrency) == "" {
		return entity.ErrMaterialConversionInvalid
	}
	return nil
}

func convertMaterialStockInTx(ctx context.Context, rep dependency.Repository, ins entity.MaterialConversionInsert, now time.Time) (*entity.MaterialConversion, error) {
	db := rep.DB()

	// The replay check precedes every state guard: a completed request must remain replayable after
	// its first execution has reduced input stock. SERIALIZABLE turns a missing-key read into the
	// concurrency fence; a deadlock is retried by the outer transaction helper.
	if ins.ClientRequestId.Valid {
		id, err := conversionIDByRequest(ctx, db, ins.ClientRequestId.String)
		if err == nil {
			return getMaterialConversion(ctx, db, id)
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
	}

	ids := make([]int, 0, len(ins.Inputs)+1)
	ids = append(ids, ins.Output.MaterialId)
	for _, input := range ins.Inputs {
		ids = append(ids, input.MaterialId)
	}
	sort.Ints(ids)

	metas := make(map[int]materialMeta, len(ids))
	for _, id := range ids {
		meta, err := readMaterialMeta(ctx, db, id)
		if err != nil {
			return nil, err
		}
		if meta.Archived {
			return nil, fmt.Errorf("%w: material %d", entity.ErrMaterialArchived, id)
		}
		metas[id] = meta
		if err := ensureStockRow(ctx, db, id); err != nil {
			return nil, err
		}
	}

	// Exclusive stock locks are taken in stable ascending material order, independent of request
	// line order. The original line order is retained for the resulting convert_out rows.
	states := make(map[int]stockState, len(ids))
	for _, id := range ids {
		state, err := readStockForUpdate(ctx, db, id)
		if err != nil {
			return nil, err
		}
		states[id] = state
	}
	// A concurrent first execution may have committed while this request waited for the same stock
	// locks. A locking read is a current read in InnoDB, so it sees that header even when the early
	// SERIALIZABLE snapshot saw the idempotency key as absent. This check must precede every stock-
	// dependent guard or a replay could fail after the first call consumed the available input.
	if ins.ClientRequestId.Valid {
		id, err := conversionIDByRequestForUpdate(ctx, db, ins.ClientRequestId.String)
		if err == nil {
			return getMaterialConversion(ctx, db, id)
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
	}

	if n, err := storeutil.QueryCountNamed(ctx, db, `
		SELECT COUNT(*) FROM material_stock_movement
		WHERE material_id = :id AND movement_type = 'receipt'`,
		map[string]any{"id": ins.Output.MaterialId}); err != nil {
		return nil, fmt.Errorf("check conversion output purchase history: %w", err)
	} else if n > 0 {
		return nil, fmt.Errorf("%w: material %d", entity.ErrConversionOutputPurchased, ins.Output.MaterialId)
	}
	if ins.SupplierId.Valid {
		n, err := storeutil.QueryCountNamed(ctx, db, `SELECT COUNT(*) FROM supplier WHERE id = :id`,
			map[string]any{"id": ins.SupplierId.Int32})
		if err != nil {
			return nil, fmt.Errorf("check conversion supplier: %w", err)
		}
		if n == 0 {
			return nil, fmt.Errorf("%w: supplier %d", entity.ErrMaterialConversionInvalid, ins.SupplierId.Int32)
		}
	}

	if err := lockConversionLots(ctx, db, ins.Inputs); err != nil {
		return nil, err
	}

	costInputs := make([]conversionCostInput, 0, len(ins.Inputs))
	for _, input := range ins.Inputs {
		meta := metas[input.MaterialId]
		if entity.IsCountableMaterialUnit(meta.Unit.String) &&
			(!input.Quantity.Equal(input.Quantity.Truncate(0)) ||
				(input.LossQty.Valid && !input.LossQty.Decimal.Equal(input.LossQty.Decimal.Truncate(0)))) {
			return nil, fmt.Errorf("%w: countable material %d requires integer quantity", entity.ErrMaterialConversionInvalid, input.MaterialId)
		}
		state := states[input.MaterialId]
		if !state.Avg.Valid {
			return nil, fmt.Errorf("%w: material %d", entity.ErrMaterialConversionUncosted, input.MaterialId)
		}
		if state.OnHand.LessThan(input.Quantity) {
			return nil, fmt.Errorf("%w: material %d has %s available", entity.ErrInsufficientMaterialStock, input.MaterialId, state.OnHand.String())
		}
		costInputs = append(costInputs, conversionCostInput{Quantity: input.Quantity, Avg: state.Avg.Decimal})
	}
	if meta := metas[ins.Output.MaterialId]; entity.IsCountableMaterialUnit(meta.Unit.String) &&
		!ins.Output.Quantity.Equal(ins.Output.Quantity.Truncate(0)) {
		return nil, fmt.Errorf("%w: countable output material %d requires integer quantity", entity.ErrMaterialConversionInvalid, ins.Output.MaterialId)
	}

	workBase := decimal.Zero
	if ins.WorkCost.IsPositive() {
		base, ok, err := toBase(ctx, rep, ins.WorkCost, ins.WorkCurrency)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, fmt.Errorf("%w: %s", entity.ErrMaterialConversionNoFxRate, ins.WorkCurrency)
		}
		workBase = base.Round(2)
	}
	cost, err := computeConversionCost(costInputs, workBase, ins.Output.Quantity)
	if err != nil {
		return nil, err
	}

	occurredAt := ins.OccurredAt
	if !occurredAt.Valid {
		occurredAt = sql.NullTime{Time: dateOnlyUTC(now), Valid: true}
	}
	outputBefore := states[ins.Output.MaterialId]
	// An uncosted output with stock would take the conversion's unit cost for every unit already on
	// hand (blendIntoAvg adopts the new cost when the old average is NULL), creating value no journal
	// entry carries. The existing stock must be costed first.
	if outputBefore.OnHand.IsPositive() && !outputBefore.Avg.Valid {
		return nil, fmt.Errorf("%w: output material %d has uncosted stock", entity.ErrMaterialConversionUncosted, ins.Output.MaterialId)
	}
	outputUnitCost := decimal.NullDecimal{Decimal: cost.OutputUnitCostBase, Valid: true}
	outputAvg := blendIntoAvg(outputBefore, ins.Output.Quantity, outputUnitCost)
	baseCurrency := strings.ToUpper(cache.GetBaseCurrency())

	conversionID, err := storeutil.ExecNamedLastId(ctx, db, `
		INSERT INTO material_conversion
			(output_material_id, supplier_id, work_cost, work_currency, work_cost_base,
			 inputs_cost_base, inputs_value_base, total_cost_base, output_unit_cost_base,
			 output_avg_before, supplier_doc, comment, client_request_id, admin_username, occurred_at)
		VALUES
			(:output_material_id, :supplier_id, :work_cost, :work_currency, :work_cost_base,
			 :inputs_cost_base, :inputs_value_base, :total_cost_base, :output_unit_cost_base,
			 :output_avg_before, :supplier_doc, :comment, :client_request_id, :admin_username, :occurred_at)
		ON DUPLICATE KEY UPDATE id = LAST_INSERT_ID(id)`,
		map[string]any{
			"output_material_id":    ins.Output.MaterialId,
			"supplier_id":           ins.SupplierId,
			"work_cost":             ins.WorkCost,
			"work_currency":         currencyNull(ins.WorkCurrency),
			"work_cost_base":        workBase,
			"inputs_cost_base":      cost.InputsCostBase,
			"inputs_value_base":     cost.InputsValueBase,
			"total_cost_base":       cost.TotalCostBase,
			"output_unit_cost_base": cost.OutputUnitCostBase,
			"output_avg_before":     nullDecimal(outputBefore.Avg),
			"supplier_doc":          ins.SupplierDoc,
			"comment":               ins.Comment,
			"client_request_id":     ins.ClientRequestId,
			"admin_username":        ins.AdminUsername,
			"occurred_at":           occurredAt,
		})
	if err != nil {
		return nil, fmt.Errorf("insert material conversion: %w", err)
	}
	// A concurrent replay can win the UNIQUE key while this transaction was waiting. Its movements
	// already exist; return them without applying stock a second time.
	if n, err := storeutil.QueryCountNamed(ctx, db,
		`SELECT COUNT(*) FROM material_stock_movement WHERE conversion_id = :id`,
		map[string]any{"id": conversionID}); err != nil {
		return nil, fmt.Errorf("check material conversion replay: %w", err)
	} else if n > 0 {
		return getMaterialConversion(ctx, db, conversionID)
	}

	conversionRef := sql.NullInt32{Int32: int32(conversionID), Valid: true}
	for _, input := range ins.Inputs {
		before := states[input.MaterialId]
		after := before.OnHand.Sub(input.Quantity)
		if err := upsertStock(ctx, db, input.MaterialId, after, before.Avg); err != nil {
			return nil, err
		}
		if input.LotId.Valid {
			if err := drawFromLot(ctx, db, int(input.LotId.Int32), input.MaterialId, input.Quantity, false); err != nil {
				return nil, err
			}
		}
		if _, err := insertMovement(ctx, db, entity.MaterialMovement{
			MaterialId: input.MaterialId, MovementType: entity.MaterialMovementConvertOut,
			Quantity: input.Quantity, OnHandBefore: before.OnHand, OnHandAfter: after,
			UnitCostBase: before.Avg, LotId: input.LotId, LossQty: input.LossQty,
			ConversionId: conversionRef, Comment: ins.Comment, AdminUsername: ins.AdminUsername,
			OccurredAt: occurredAt,
		}, now); err != nil {
			return nil, err
		}
	}

	if err := upsertStock(ctx, db, ins.Output.MaterialId,
		outputBefore.OnHand.Add(ins.Output.Quantity), outputAvg); err != nil {
		return nil, err
	}
	lotID, err := upsertLotOnReceipt(ctx, db, entity.MaterialReceiptInsert{
		MaterialId: ins.Output.MaterialId, Quantity: ins.Output.Quantity,
		UnitCost: outputUnitCost, Currency: baseCurrency, Lot: ins.Output.LotCode,
		MeasuredWidthCm: ins.Output.MeasuredWidthCm, ShadeCode: ins.Output.ShadeCode,
		SupplierDoc: ins.SupplierDoc, OccurredAt: occurredAt,
	})
	if err != nil {
		return nil, err
	}
	outMovement := entity.MaterialMovement{
		MaterialId: ins.Output.MaterialId, MovementType: entity.MaterialMovementConvertIn,
		Quantity: ins.Output.Quantity, OnHandBefore: outputBefore.OnHand,
		OnHandAfter: outputBefore.OnHand.Add(ins.Output.Quantity), UnitCost: outputUnitCost,
		Currency: currencyNull(baseCurrency), UnitCostBase: outputUnitCost, Lot: ins.Output.LotCode,
		LotId: lotID, SupplierDoc: ins.SupplierDoc, SupplierId: ins.SupplierId,
		ConversionId: conversionRef, Comment: ins.Comment, AdminUsername: ins.AdminUsername,
		OccurredAt: occurredAt,
	}
	if _, err := insertMovement(ctx, db, outMovement, now); err != nil {
		return nil, err
	}

	if len(ins.Inputs) == 1 {
		if err := rep.TechCards().SetMaterialSourceIfEmpty(ctx, ins.Output.MaterialId, ins.Inputs[0].MaterialId); err != nil {
			return nil, err
		}
	}
	if err := rep.TechCards().AddMaterialPrice(ctx, entity.MaterialPrice{
		MaterialId: ins.Output.MaterialId, Price: cost.OutputUnitCostBase, Currency: baseCurrency,
		ValidFrom: occurredAt.Time, Source: entity.MaterialPriceSourceConversion, Note: ins.Comment,
	}); err != nil {
		return nil, err
	}
	return getMaterialConversion(ctx, db, conversionID)
}

func ensureStockRow(ctx context.Context, db dependency.DB, materialID int) error {
	if err := storeutil.ExecNamed(ctx, db, `
		INSERT INTO material_stock (material_id, on_hand) VALUES (:id, 0)
		ON DUPLICATE KEY UPDATE material_id = VALUES(material_id)`, map[string]any{"id": materialID}); err != nil {
		return fmt.Errorf("ensure material stock %d: %w", materialID, err)
	}
	return nil
}

type conversionLotLock struct {
	Id         int             `db:"id"`
	MaterialId int             `db:"material_id"`
	Remaining  decimal.Decimal `db:"remaining_qty"`
}

func lockConversionLots(ctx context.Context, db dependency.DB, inputs []entity.MaterialConversionInput) error {
	ids := make([]int, 0, len(inputs))
	want := make(map[int]entity.MaterialConversionInput, len(inputs))
	for _, input := range inputs {
		if input.LotId.Valid {
			id := int(input.LotId.Int32)
			ids = append(ids, id)
			want[id] = input
		}
	}
	if len(ids) == 0 {
		return nil
	}
	sort.Ints(ids)
	rows, err := storeutil.QueryListNamed[conversionLotLock](ctx, db, `
		SELECT id, material_id, remaining_qty FROM material_lot
		WHERE id IN (:ids) ORDER BY id FOR UPDATE`, map[string]any{"ids": ids})
	if err != nil {
		return fmt.Errorf("lock conversion lots: %w", err)
	}
	if len(rows) != len(ids) {
		return entity.ErrMaterialLotMismatch
	}
	for _, lot := range rows {
		input := want[lot.Id]
		if lot.MaterialId != input.MaterialId {
			return fmt.Errorf("%w: lot %d is material %d, not %d", entity.ErrMaterialLotMismatch, lot.Id, lot.MaterialId, input.MaterialId)
		}
		if lot.Remaining.LessThan(input.Quantity) {
			return fmt.Errorf("%w: lot %d has %s remaining", entity.ErrInsufficientMaterialLot, lot.Id, lot.Remaining.String())
		}
	}
	return nil
}

func conversionIDByRequest(ctx context.Context, db dependency.DB, requestID string) (int, error) {
	row, err := storeutil.QueryNamedOne[struct {
		Id int `db:"id"`
	}](ctx, db, `SELECT id FROM material_conversion WHERE client_request_id = :request_id`,
		map[string]any{"request_id": requestID})
	if err != nil {
		return 0, err
	}
	return row.Id, nil
}

func conversionIDByRequestForUpdate(ctx context.Context, db dependency.DB, requestID string) (int, error) {
	row, err := storeutil.QueryNamedOne[struct {
		Id int `db:"id"`
	}](ctx, db, `SELECT id FROM material_conversion WHERE client_request_id = :request_id FOR UPDATE`,
		map[string]any{"request_id": requestID})
	if err != nil {
		return 0, err
	}
	return row.Id, nil
}

// GetMaterialConversion returns one conversion with all sides, or sql.ErrNoRows when absent.
func (s *Store) GetMaterialConversion(ctx context.Context, id int) (*entity.MaterialConversion, error) {
	return getMaterialConversion(ctx, s.DB, id)
}

func getMaterialConversion(ctx context.Context, db dependency.DB, id int) (*entity.MaterialConversion, error) {
	rows, err := getMaterialConversionsByIds(ctx, db, []int{id})
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, sql.ErrNoRows
	}
	return &rows[0], nil
}

// GetMaterialConversionsByIds batch-loads history headers and lines without an N+1 query loop.
func (s *Store) GetMaterialConversionsByIds(ctx context.Context, ids []int) ([]entity.MaterialConversion, error) {
	return getMaterialConversionsByIds(ctx, s.DB, ids)
}

type conversionHistoryRow struct {
	HeaderId                 int                 `db:"header_id"`
	HeaderOutputMaterialId   int                 `db:"header_output_material_id"`
	HeaderSupplierId         sql.NullInt32       `db:"header_supplier_id"`
	HeaderWorkCost           decimal.Decimal     `db:"header_work_cost"`
	HeaderWorkCurrency       sql.NullString      `db:"header_work_currency"`
	HeaderWorkCostBase       decimal.Decimal     `db:"header_work_cost_base"`
	HeaderInputsCostBase     decimal.Decimal     `db:"header_inputs_cost_base"`
	HeaderInputsValueBase    decimal.Decimal     `db:"header_inputs_value_base"`
	HeaderTotalCostBase      decimal.Decimal     `db:"header_total_cost_base"`
	HeaderOutputUnitCostBase decimal.Decimal     `db:"header_output_unit_cost_base"`
	HeaderOutputAvgBefore    decimal.NullDecimal `db:"header_output_avg_before"`
	HeaderSupplierDoc        sql.NullString      `db:"header_supplier_doc"`
	HeaderComment            sql.NullString      `db:"header_comment"`
	HeaderClientRequestId    sql.NullString      `db:"header_client_request_id"`
	HeaderAdminUsername      string              `db:"header_admin_username"`
	HeaderOccurredAt         sql.NullTime        `db:"header_occurred_at"`
	HeaderCreatedAt          time.Time           `db:"header_created_at"`
	entity.MaterialMovement
	MaterialName string         `db:"material_name"`
	MaterialUnit sql.NullString `db:"material_unit"`
}

func (r conversionHistoryRow) header() entity.MaterialConversion {
	return entity.MaterialConversion{
		Id: r.HeaderId, OutputMaterialId: r.HeaderOutputMaterialId, SupplierId: r.HeaderSupplierId,
		WorkCost: r.HeaderWorkCost, WorkCurrency: r.HeaderWorkCurrency, WorkCostBase: r.HeaderWorkCostBase,
		InputsCostBase: r.HeaderInputsCostBase, InputsValueBase: r.HeaderInputsValueBase,
		TotalCostBase: r.HeaderTotalCostBase, OutputUnitCostBase: r.HeaderOutputUnitCostBase,
		OutputAvgBefore: r.HeaderOutputAvgBefore, SupplierDoc: r.HeaderSupplierDoc,
		Comment: r.HeaderComment, ClientRequestId: r.HeaderClientRequestId,
		AdminUsername: r.HeaderAdminUsername, OccurredAt: r.HeaderOccurredAt, CreatedAt: r.HeaderCreatedAt,
	}
}

func getMaterialConversionsByIds(ctx context.Context, db dependency.DB, ids []int) ([]entity.MaterialConversion, error) {
	if len(ids) == 0 {
		return []entity.MaterialConversion{}, nil
	}
	rows, err := storeutil.QueryListNamed[conversionHistoryRow](ctx, db, `
		SELECT mc.id AS header_id, mc.output_material_id AS header_output_material_id,
		       mc.supplier_id AS header_supplier_id, mc.work_cost AS header_work_cost,
		       mc.work_currency AS header_work_currency, mc.work_cost_base AS header_work_cost_base,
		       mc.inputs_cost_base AS header_inputs_cost_base,
		       mc.inputs_value_base AS header_inputs_value_base,
		       mc.total_cost_base AS header_total_cost_base,
		       mc.output_unit_cost_base AS header_output_unit_cost_base,
		       mc.output_avg_before AS header_output_avg_before,
		       mc.supplier_doc AS header_supplier_doc, mc.comment AS header_comment,
		       mc.client_request_id AS header_client_request_id,
		       mc.admin_username AS header_admin_username,
		       mc.occurred_at AS header_occurred_at, mc.created_at AS header_created_at,
		       `+materialMovementSelect+`, mat.name AS material_name, mat.unit AS material_unit
		FROM material_conversion mc
		JOIN material_stock_movement msm ON msm.conversion_id = mc.id
		JOIN material mat ON mat.id = msm.material_id
		LEFT JOIN library_file lf ON lf.id = msm.invoice_file_id
		WHERE mc.id IN (:ids)
		ORDER BY mc.id,
		         CASE msm.movement_type WHEN 'convert_out' THEN 0 ELSE 1 END, msm.id`,
		map[string]any{"ids": ids})
	if err != nil {
		return nil, fmt.Errorf("get material conversions: %w", err)
	}
	byID := make(map[int]*entity.MaterialConversion, len(ids))
	for _, row := range rows {
		header := byID[row.HeaderId]
		if header == nil {
			value := row.header()
			header = &value
			byID[row.HeaderId] = header
		}
		header.Lines = append(header.Lines, entity.MaterialConversionLine{
			Movement: row.MaterialMovement, MaterialName: row.MaterialName, Unit: row.MaterialUnit.String,
		})
	}

	out := make([]entity.MaterialConversion, 0, len(byID))
	seen := make(map[int]bool, len(ids))
	for _, id := range ids {
		if seen[id] {
			continue
		}
		seen[id] = true
		if header := byID[id]; header != nil {
			out = append(out, *header)
		}
	}
	return out, nil
}
