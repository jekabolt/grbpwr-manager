package inventory_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	_ "github.com/go-sql-driver/mysql"
	"github.com/google/uuid"
	"github.com/jekabolt/grbpwr-manager/internal/cache"
	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store"
	"github.com/jekabolt/grbpwr-manager/internal/testdbguard"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// These tests intentionally use a separate external package: the production store composes the
// inventory store, so importing internal/store from package inventory would create an import cycle.
// They are destructive and can only open a local/container database whose name ends in _test.
var (
	conversionDBOnce sync.Once
	conversionRep    dependency.Repository
	conversionRaw    *sql.DB
	conversionDBErr  error
)

func conversionRepository(t *testing.T) (dependency.Repository, *sql.DB) {
	t.Helper()
	if os.Getenv("CI") != "1" {
		t.Skip("material conversion DB tests require CI=1 and a disposable local MySQL database")
	}
	host, port := os.Getenv("MYSQL_HOST"), os.Getenv("MYSQL_PORT")
	user, pass, name := os.Getenv("MYSQL_USER"), os.Getenv("MYSQL_PASSWORD"), os.Getenv("MYSQL_DATABASE")
	if host == "" || port == "" || user == "" || name == "" {
		t.Skip("MYSQL_* for the disposable database are not set")
	}
	dsn := fmt.Sprintf("%s:%s@tcp(%s:%s)/%s?parseTime=true&multiStatements=true", user, pass, host, port, name)
	if err := testdbguard.CheckMySQLDSN(dsn); err != nil {
		t.Fatalf("material conversion DB tests refuse to open MySQL: %v", err)
	}
	conversionDBOnce.Do(func() {
		ctx := context.Background()
		conversionRaw, conversionDBErr = sql.Open("mysql", dsn)
		if conversionDBErr != nil {
			return
		}
		if conversionDBErr = conversionRaw.Ping(); conversionDBErr != nil {
			return
		}
		conversionRep, conversionDBErr = store.NewForTest(ctx, store.Config{
			DSN: dsn, Automigrate: true, MaxOpenConnections: 10, MaxIdleConnections: 5,
		})
		cache.SetDefaultCurrency("EUR")
	})
	require.NoError(t, conversionDBErr)
	require.NotNil(t, conversionRep)
	return conversionRep, conversionRaw
}

func d(v string) decimal.Decimal { return decimal.RequireFromString(v) }

func nd(v string) decimal.NullDecimal {
	return decimal.NullDecimal{Decimal: d(v), Valid: true}
}

func ns(v string) sql.NullString { return sql.NullString{String: v, Valid: true} }

func ni(v int) sql.NullInt32 { return sql.NullInt32{Int32: int32(v), Valid: true} }

type conversionFixture struct {
	t           *testing.T
	rep         dependency.Repository
	raw         *sql.DB
	materialIDs []int
	sampleIDs   []int
	cardIDs     []int
}

func newConversionFixture(t *testing.T) *conversionFixture {
	rep, raw := conversionRepository(t)
	f := &conversionFixture{t: t, rep: rep, raw: raw}
	t.Cleanup(f.cleanup)
	return f
}

func (f *conversionFixture) cleanup() {
	ctx := context.Background()
	for _, id := range f.materialIDs {
		_, _ = f.raw.ExecContext(ctx, `DELETE FROM material_lot_shrinkage WHERE material_id = ?`, id)
	}
	for _, id := range f.materialIDs {
		_, _ = f.raw.ExecContext(ctx, `DELETE FROM material_stock_movement WHERE material_id = ?`, id)
	}
	for _, id := range f.materialIDs {
		_, _ = f.raw.ExecContext(ctx, `DELETE FROM material_conversion WHERE output_material_id = ?`, id)
	}
	for _, id := range f.sampleIDs {
		_, _ = f.raw.ExecContext(ctx, `DELETE FROM sample WHERE id = ?`, id)
	}
	for _, id := range f.cardIDs {
		_, _ = f.raw.ExecContext(ctx, `DELETE FROM tech_card WHERE id = ?`, id)
	}
	for _, id := range f.materialIDs {
		_, _ = f.raw.ExecContext(ctx, `UPDATE material SET source_material_id = NULL WHERE id = ?`, id)
	}
	for i := len(f.materialIDs) - 1; i >= 0; i-- {
		id := f.materialIDs[i]
		_, _ = f.raw.ExecContext(ctx, `DELETE FROM material_price WHERE material_id = ?`, id)
		_, _ = f.raw.ExecContext(ctx, `DELETE FROM material_lot WHERE material_id = ?`, id)
		_, _ = f.raw.ExecContext(ctx, `DELETE FROM material_stock WHERE material_id = ?`, id)
		_, _ = f.raw.ExecContext(ctx, `DELETE FROM material WHERE id = ?`, id)
	}
}

func (f *conversionFixture) material(unit string, class entity.MaterialClass) int {
	f.t.Helper()
	insert := &entity.MaterialInsert{
		Name:          "conversion probe " + uuid.NewString(),
		Section:       "trim",
		Code:          ns("CV-" + uuid.NewString()),
		Unit:          ns(unit),
		MaterialClass: string(class),
	}
	if class == entity.MaterialClassFabric {
		insert.Section = "fabric"
		insert.FabricAttr = &entity.MaterialFabricAttr{}
	}
	id, err := f.rep.TechCards().CreateMaterial(context.Background(), insert)
	require.NoError(f.t, err)
	f.materialIDs = append(f.materialIDs, id)
	return id
}

func (f *conversionFixture) receipt(materialID int, qty, unitCost, lot string, width decimal.NullDecimal) entity.MaterialMovement {
	f.t.Helper()
	m, err := f.rep.MaterialStock().ReceiveMaterialStock(context.Background(), entity.MaterialReceiptInsert{
		MaterialId: materialID, Quantity: d(qty), UnitCost: nd(unitCost), Currency: "EUR",
		Lot: sql.NullString{String: lot, Valid: lot != ""}, MeasuredWidthCm: width,
		AdminUsername: "conversion-test",
	})
	require.NoError(f.t, err)
	return m
}

func (f *conversionFixture) convert(inputID, outputID int, qty, outputQty string) entity.MaterialConversionInsert {
	return entity.MaterialConversionInsert{
		Inputs:          []entity.MaterialConversionInput{{MaterialId: inputID, Quantity: d(qty)}},
		Output:          entity.MaterialConversionOutput{MaterialId: outputID, Quantity: d(outputQty)},
		ClientRequestId: ns(uuid.NewString()), AdminUsername: "conversion-test",
	}
}

func assertStock(t *testing.T, rep dependency.Repository, materialID int, qty, avg string) {
	t.Helper()
	stock, err := rep.MaterialStock().GetMaterialStock(context.Background(), materialID)
	require.NoError(t, err)
	assert.True(t, stock.OnHand.Equal(d(qty)), "on_hand %s, want %s", stock.OnHand, qty)
	require.True(t, stock.AvgUnitCostBase.Valid)
	assert.True(t, stock.AvgUnitCostBase.Decimal.Equal(d(avg)), "avg %s, want %s", stock.AvgUnitCostBase.Decimal, avg)
}

func TestMaterialConversionDBMoneyBlendIdempotencyAndHistory(t *testing.T) {
	f := newConversionFixture(t)
	ctx := context.Background()
	input := f.material("m", entity.MaterialClassFabric)
	output := f.material("pcs", entity.MaterialClassOther)
	f.receipt(input, "30", "4.20", "", decimal.NullDecimal{})

	ins := f.convert(input, output, "10", "200")
	ins.Inputs[0].LossQty = nd("0.5")
	ins.WorkCost, ins.WorkCurrency = d("50"), "EUR"
	first, err := f.rep.MaterialStock().ConvertMaterialStock(ctx, ins)
	require.NoError(t, err)
	require.Len(t, first.Lines, 2)
	assert.True(t, first.InputsValueBase.Equal(d("42")))
	assert.True(t, first.TotalCostBase.Equal(d("92")))
	assert.True(t, first.OutputUnitCostBase.Equal(d("0.46")))
	assertStock(t, f.rep, input, "20", "4.2")
	assertStock(t, f.rep, output, "200", "0.46")

	out, err := f.rep.TechCards().GetMaterial(ctx, output)
	require.NoError(t, err)
	require.True(t, out.SourceMaterialId.Valid)
	assert.Equal(t, int32(input), out.SourceMaterialId.Int32)
	assert.NotEmpty(t, out.SourceMaterialName.String)
	prices, err := f.rep.TechCards().ListMaterialPrices(ctx, output)
	require.NoError(t, err)
	require.NotEmpty(t, prices)
	assert.Equal(t, entity.MaterialPriceSourceConversion, prices[0].Source)
	assert.True(t, prices[0].Price.Equal(d("0.46")))

	moves, total, err := f.rep.MaterialStock().ListMaterialMovements(ctx, 10, 0, entity.MaterialMovementFilter{ConversionId: first.Id})
	require.NoError(t, err)
	assert.Equal(t, 2, total)
	require.Len(t, moves, 2)
	byType := make(map[entity.MaterialMovementType]entity.MaterialMovement, len(moves))
	for _, movement := range moves {
		byType[movement.MovementType] = movement
	}
	require.Contains(t, byType, entity.MaterialMovementConvertOut)
	require.Contains(t, byType, entity.MaterialMovementConvertIn)
	assert.True(t, byType[entity.MaterialMovementConvertOut].LossQty.Decimal.Equal(d("0.5")))

	batch, err := f.rep.MaterialStock().GetMaterialConversionsByIds(ctx, []int{first.Id})
	require.NoError(t, err)
	require.Len(t, batch, 1)
	require.Len(t, batch[0].Lines, 2)

	replay, err := f.rep.MaterialStock().ConvertMaterialStock(ctx, ins)
	require.NoError(t, err)
	assert.Equal(t, first.Id, replay.Id)
	assertStock(t, f.rep, input, "20", "4.2")
	assertStock(t, f.rep, output, "200", "0.46")
	var headers int
	require.NoError(t, f.raw.QueryRow(`SELECT COUNT(*) FROM material_conversion WHERE client_request_id = ?`, ins.ClientRequestId.String).Scan(&headers))
	assert.Equal(t, 1, headers)

	blendOutput := f.material("pcs", entity.MaterialClassOther)
	_, err = f.raw.Exec(`INSERT INTO material_stock (material_id, on_hand, avg_unit_cost_base)
		VALUES (?, 100, 0.5000) ON DUPLICATE KEY UPDATE on_hand = 100, avg_unit_cost_base = 0.5000`, blendOutput)
	require.NoError(t, err)
	blendInput := f.material("m", entity.MaterialClassFabric)
	f.receipt(blendInput, "30", "4.20", "", decimal.NullDecimal{})
	blend := f.convert(blendInput, blendOutput, "10", "200")
	blend.WorkCost, blend.WorkCurrency = d("50"), "EUR"
	_, err = f.rep.MaterialStock().ConvertMaterialStock(ctx, blend)
	require.NoError(t, err)
	assertStock(t, f.rep, blendOutput, "300", "0.4733")
}

func TestMaterialConversionDBGuardsRollback(t *testing.T) {
	tests := []struct {
		name string
		want error
		edit func(*conversionFixture, *entity.MaterialConversionInsert, int, int)
	}{
		{"uncosted input", entity.ErrMaterialConversionUncosted, func(f *conversionFixture, _ *entity.MaterialConversionInsert, input, _ int) {
			_, err := f.raw.Exec(`UPDATE material_stock SET avg_unit_cost_base = NULL WHERE material_id = ?`, input)
			require.NoError(f.t, err)
		}},
		{"same output", entity.ErrMaterialConversionInvalid, func(_ *conversionFixture, in *entity.MaterialConversionInsert, input, _ int) {
			in.Output.MaterialId = input
		}},
		{"zero output", entity.ErrMaterialConversionInvalid, func(_ *conversionFixture, in *entity.MaterialConversionInsert, _, _ int) {
			in.Output.Quantity = decimal.Zero
		}},
		{"fractional countable output", entity.ErrMaterialConversionInvalid, func(_ *conversionFixture, in *entity.MaterialConversionInsert, _, _ int) {
			in.Output.Quantity = d("2.5")
		}},
		{"loss over input", entity.ErrMaterialConversionInvalid, func(_ *conversionFixture, in *entity.MaterialConversionInsert, _, _ int) {
			in.Inputs[0].LossQty = nd("10.5")
		}},
		{"insufficient input", entity.ErrInsufficientMaterialStock, func(_ *conversionFixture, in *entity.MaterialConversionInsert, _, _ int) {
			in.Inputs[0].Quantity = d("11")
		}},
		{"archived input", entity.ErrMaterialArchived, func(f *conversionFixture, _ *entity.MaterialConversionInsert, input, _ int) {
			_, err := f.raw.Exec(`UPDATE material SET archived = TRUE WHERE id = ?`, input)
			require.NoError(f.t, err)
		}},
		{"archived output", entity.ErrMaterialArchived, func(f *conversionFixture, _ *entity.MaterialConversionInsert, _, output int) {
			_, err := f.raw.Exec(`UPDATE material SET archived = TRUE WHERE id = ?`, output)
			require.NoError(f.t, err)
		}},
		{"purchased output", entity.ErrConversionOutputPurchased, func(f *conversionFixture, _ *entity.MaterialConversionInsert, _, output int) {
			f.receipt(output, "1", "1", "", decimal.NullDecimal{})
		}},
		{"uncosted output stock", entity.ErrMaterialConversionUncosted, func(f *conversionFixture, _ *entity.MaterialConversionInsert, _, output int) {
			_, err := f.raw.Exec(`INSERT INTO material_stock (material_id, on_hand, avg_unit_cost_base) VALUES (?, 100, NULL)
				ON DUPLICATE KEY UPDATE on_hand = 100, avg_unit_cost_base = NULL`, output)
			require.NoError(f.t, err)
		}},
		{"work currency absent", entity.ErrMaterialConversionInvalid, func(_ *conversionFixture, in *entity.MaterialConversionInsert, _, _ int) { in.WorkCost = d("1") }},
		{"foreign lot", entity.ErrMaterialLotMismatch, func(f *conversionFixture, in *entity.MaterialConversionInsert, _, _ int) {
			other := f.material("m", entity.MaterialClassFabric)
			mv := f.receipt(other, "10", "1", "FOREIGN", decimal.NullDecimal{})
			in.Inputs[0].LotId = mv.LotId
		}},
		{"missing FX", entity.ErrMaterialConversionNoFxRate, func(_ *conversionFixture, in *entity.MaterialConversionInsert, _, _ int) {
			in.WorkCost, in.WorkCurrency = d("1"), "ZZZ"
		}},
		{"precision", entity.ErrMaterialConversionPrecision, func(_ *conversionFixture, in *entity.MaterialConversionInsert, _, _ int) {
			in.Output.Quantity = d("30000")
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newConversionFixture(t)
			input := f.material("m", entity.MaterialClassFabric)
			output := f.material("pcs", entity.MaterialClassOther)
			f.receipt(input, "10", "1", "INPUT", decimal.NullDecimal{})
			ins := f.convert(input, output, "10", "2")
			tc.edit(f, &ins, input, output)

			var beforeInput, beforeOutput decimal.Decimal
			require.NoError(t, f.raw.QueryRow(`SELECT on_hand FROM material_stock WHERE material_id = ?`, input).Scan(&beforeInput))
			_ = f.raw.QueryRow(`SELECT on_hand FROM material_stock WHERE material_id = ?`, output).Scan(&beforeOutput)
			_, err := f.rep.MaterialStock().ConvertMaterialStock(context.Background(), ins)
			assert.ErrorIs(t, err, tc.want)
			var afterInput, afterOutput decimal.Decimal
			require.NoError(t, f.raw.QueryRow(`SELECT on_hand FROM material_stock WHERE material_id = ?`, input).Scan(&afterInput))
			_ = f.raw.QueryRow(`SELECT on_hand FROM material_stock WHERE material_id = ?`, output).Scan(&afterOutput)
			assert.True(t, afterInput.Equal(beforeInput), "input stock changed: %s -> %s", beforeInput, afterInput)
			assert.True(t, afterOutput.Equal(beforeOutput), "output stock changed: %s -> %s", beforeOutput, afterOutput)
		})
	}
}

func TestMaterialConversionDBLineageAndPurchaseBlock(t *testing.T) {
	f := newConversionFixture(t)
	ctx := context.Background()
	firstInput := f.material("m", entity.MaterialClassFabric)
	secondInput := f.material("m", entity.MaterialClassFabric)
	output := f.material("pcs", entity.MaterialClassOther)
	f.receipt(firstInput, "10", "1", "", decimal.NullDecimal{})
	f.receipt(secondInput, "10", "2", "", decimal.NullDecimal{})

	first := f.convert(firstInput, output, "2", "10")
	_, err := f.rep.MaterialStock().ConvertMaterialStock(ctx, first)
	require.NoError(t, err)
	before, err := f.rep.MaterialStock().GetMaterialStock(ctx, output)
	require.NoError(t, err)
	_, err = f.rep.MaterialStock().ReceiveMaterialStock(ctx, entity.MaterialReceiptInsert{
		MaterialId: output, Quantity: d("1"), UnitCost: nd("1"), Currency: "EUR",
	})
	assert.ErrorIs(t, err, entity.ErrMaterialMadeNotPurchasable)
	after, err := f.rep.MaterialStock().GetMaterialStock(ctx, output)
	require.NoError(t, err)
	assert.True(t, after.OnHand.Equal(before.OnHand))

	second := f.convert(secondInput, output, "2", "10")
	_, err = f.rep.MaterialStock().ConvertMaterialStock(ctx, second)
	require.NoError(t, err)
	material, err := f.rep.TechCards().GetMaterial(ctx, output)
	require.NoError(t, err)
	assert.Equal(t, int32(firstInput), material.SourceMaterialId.Int32, "later conversions must not rewrite lineage")

	multiOutput := f.material("pcs", entity.MaterialClassOther)
	multi := entity.MaterialConversionInsert{
		Inputs: []entity.MaterialConversionInput{
			{MaterialId: firstInput, Quantity: d("1")},
			{MaterialId: secondInput, Quantity: d("1")},
		},
		Output:          entity.MaterialConversionOutput{MaterialId: multiOutput, Quantity: d("10")},
		ClientRequestId: ns(uuid.NewString()), AdminUsername: "conversion-test",
	}
	_, err = f.rep.MaterialStock().ConvertMaterialStock(ctx, multi)
	require.NoError(t, err)
	blended, err := f.rep.TechCards().GetMaterial(ctx, multiOutput)
	require.NoError(t, err)
	assert.False(t, blended.SourceMaterialId.Valid)
	_, err = f.rep.MaterialStock().ReceiveMaterialStock(ctx, entity.MaterialReceiptInsert{
		MaterialId: multiOutput, Quantity: d("1"), UnitCost: nd("1"), Currency: "EUR",
	})
	assert.ErrorIs(t, err, entity.ErrMaterialMadeNotPurchasable, "multi-input output has no lineage but is still made")
}

func TestMaterialShrinkageDBValuePercentWidthAndConflict(t *testing.T) {
	f := newConversionFixture(t)
	ctx := context.Background()
	materialID := f.material("m", entity.MaterialClassFabric)
	receipt := f.receipt(materialID, "10", "4.20", "A", nd("150"))
	beforeMaterial, err := f.rep.TechCards().GetMaterial(ctx, materialID)
	require.NoError(t, err)

	result, err := f.rep.MaterialStock().ShrinkMaterialLot(ctx, entity.MaterialLotShrinkInsert{
		LotId: int(receipt.LotId.Int32), LengthBefore: nd("10"), LengthAfter: d("9.5"),
		WidthAfter: nd("146"), AdminUsername: "shrink-test",
	})
	require.NoError(t, err)
	assertStock(t, f.rep, materialID, "9.5", "4.4211")
	assert.True(t, result.Lot.RemainingQty.Equal(d("9.5")))
	assert.True(t, result.Lot.ShrunkQty.Equal(d("0.5")))
	assert.True(t, result.Lot.UnitCost.Decimal.Equal(d("4.4211")))
	assert.True(t, result.Lot.ShrinkagePct.Decimal.Equal(d("5")))
	assert.True(t, result.Lot.MeasuredWidthCm.Decimal.Equal(d("150")))
	assert.True(t, result.Lot.WashedWidthCm.Decimal.Equal(d("146")))
	assert.Equal(t, entity.MaterialMovementShrinkage, result.Movement.MovementType)
	assert.True(t, result.Movement.Quantity.Equal(d("0.5")))
	assert.True(t, result.Movement.UnitCostBase.Valid)
	assert.True(t, result.Movement.UnitCostBase.Decimal.IsZero())

	widths, err := f.rep.MaterialStock().NarrowestMeasuredLotWidths(ctx, []int{materialID})
	require.NoError(t, err)
	assert.True(t, widths[materialID].Decimal.Equal(d("146")))
	afterMaterial, err := f.rep.TechCards().GetMaterial(ctx, materialID)
	require.NoError(t, err)
	require.NotNil(t, afterMaterial.FabricAttr)
	assert.True(t, afterMaterial.FabricAttr.ShrinkagePct.Decimal.Equal(d("5")))
	assert.Equal(t, beforeMaterial.LockVersion+1, afterMaterial.LockVersion)
	stale := beforeMaterial.MaterialInsert
	err = f.rep.TechCards().UpdateMaterial(ctx, materialID, &stale, beforeMaterial.LockVersion)
	assert.ErrorIs(t, err, entity.ErrMaterialConflict)
}

func TestMaterialShrinkageDBTwoLotsWeightingAndCompounding(t *testing.T) {
	t.Run("two lot value", func(t *testing.T) {
		f := newConversionFixture(t)
		materialID := f.material("m", entity.MaterialClassFabric)
		a := f.receipt(materialID, "10", "4.20", "A", decimal.NullDecimal{})
		f.receipt(materialID, "20", "4.20", "B", decimal.NullDecimal{})
		_, err := f.rep.MaterialStock().ShrinkMaterialLot(context.Background(), entity.MaterialLotShrinkInsert{
			LotId: int(a.LotId.Int32), LengthAfter: d("9.5"),
		})
		require.NoError(t, err)
		assertStock(t, f.rep, materialID, "29.5", "4.2712")
	})

	t.Run("weighted and compounded percent", func(t *testing.T) {
		f := newConversionFixture(t)
		ctx := context.Background()
		materialID := f.material("m", entity.MaterialClassFabric)
		a := f.receipt(materialID, "10", "4.20", "A", decimal.NullDecimal{})
		b := f.receipt(materialID, "20", "4.20", "B", decimal.NullDecimal{})
		c := f.receipt(materialID, "30", "4.20", "C", decimal.NullDecimal{})
		for _, step := range []struct {
			lot   int
			after string
		}{{int(a.LotId.Int32), "9.5"}, {int(b.LotId.Int32), "19"}, {int(c.LotId.Int32), "29.1"}} {
			_, err := f.rep.MaterialStock().ShrinkMaterialLot(ctx, entity.MaterialLotShrinkInsert{LotId: step.lot, LengthAfter: d(step.after)})
			require.NoError(t, err)
		}
		material, err := f.rep.TechCards().GetMaterial(ctx, materialID)
		require.NoError(t, err)
		assert.True(t, material.FabricAttr.ShrinkagePct.Decimal.Equal(d("4")))

		result, err := f.rep.MaterialStock().ShrinkMaterialLot(ctx, entity.MaterialLotShrinkInsert{
			LotId: int(a.LotId.Int32), LengthBefore: nd("9.5"), LengthAfter: d("9.31"),
		})
		require.NoError(t, err)
		assert.True(t, result.Lot.ShrinkagePct.Decimal.Equal(d("6.9")))
		assert.True(t, result.MaterialShrinkagePct.Decimal.Equal(d("4.32")))
	})
}

func (f *conversionFixture) sample() int {
	f.t.Helper()
	res, err := f.raw.Exec(`INSERT INTO tech_card (style_number, name, brand, target_gender, top_category_id)
		VALUES (?, 'conversion return probe', 'b', 'unisex', 1)`, "CV-"+uuid.NewString())
	require.NoError(f.t, err)
	cardID, err := res.LastInsertId()
	require.NoError(f.t, err)
	f.cardIDs = append(f.cardIDs, int(cardID))
	sampleID, err := f.rep.Samples().AddSample(context.Background(), &entity.SampleInsert{
		TechCardId: int(cardID), Purpose: "proto", Status: "planned", FabricSource: "sample",
	})
	require.NoError(f.t, err)
	f.sampleIDs = append(f.sampleIDs, sampleID)
	return sampleID
}

func TestMaterialShrinkageDBReturnCapAndGuards(t *testing.T) {
	f := newConversionFixture(t)
	ctx := context.Background()
	materialID := f.material("m", entity.MaterialClassFabric)
	receipt := f.receipt(materialID, "10", "4.20", "A", decimal.NullDecimal{})
	_, err := f.rep.MaterialStock().ShrinkMaterialLot(ctx, entity.MaterialLotShrinkInsert{
		LotId: int(receipt.LotId.Int32), LengthAfter: d("9.5"),
	})
	require.NoError(t, err)

	_, err = f.rep.MaterialStock().ShrinkMaterialLot(ctx, entity.MaterialLotShrinkInsert{
		LotId: int(receipt.LotId.Int32), LengthBefore: nd("10"), LengthAfter: d("9"),
	})
	assert.ErrorIs(t, err, entity.ErrMaterialLotStale)
	_, err = f.rep.MaterialStock().ShrinkMaterialLot(ctx, entity.MaterialLotShrinkInsert{
		LotId: int(receipt.LotId.Int32), LengthAfter: d("9.5"),
	})
	assert.ErrorIs(t, err, entity.ErrMaterialConversionInvalid)
	_, err = f.rep.MaterialStock().ShrinkMaterialLot(ctx, entity.MaterialLotShrinkInsert{
		LotId: int(receipt.LotId.Int32), LengthAfter: decimal.Zero,
	})
	assert.ErrorIs(t, err, entity.ErrMaterialConversionInvalid)

	pcs := f.material("pcs", entity.MaterialClassOther)
	pcsReceipt := f.receipt(pcs, "10", "1", "PCS", decimal.NullDecimal{})
	_, err = f.rep.MaterialStock().ShrinkMaterialLot(ctx, entity.MaterialLotShrinkInsert{
		LotId: int(pcsReceipt.LotId.Int32), LengthAfter: d("9"),
	})
	assert.ErrorIs(t, err, entity.ErrMaterialConversionInvalid)

	sampleID := f.sample()
	_, err = f.rep.MaterialStock().IssueMaterialStock(ctx, entity.MaterialIssueInsert{
		MaterialId: materialID, Quantity: d("4"), SampleId: ni(sampleID), LotId: receipt.LotId,
	})
	require.NoError(t, err)
	_, err = f.rep.MaterialStock().IssueMaterialStock(ctx, entity.MaterialIssueInsert{
		MaterialId: materialID, Quantity: d("4.5"), SampleId: ni(sampleID), LotId: receipt.LotId, IsReturn: true,
	})
	assert.True(t, errors.Is(err, entity.ErrExcessiveMaterialReturn))
	assertStock(t, f.rep, materialID, "5.5", "4.4211")
	_, err = f.rep.MaterialStock().IssueMaterialStock(ctx, entity.MaterialIssueInsert{
		MaterialId: materialID, Quantity: d("4"), SampleId: ni(sampleID), LotId: receipt.LotId, IsReturn: true,
	})
	require.NoError(t, err)
	assertStock(t, f.rep, materialID, "9.5", "4.4211")
}

func TestMaterialShrinkageDBUncosted(t *testing.T) {
	f := newConversionFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	materialID := f.material("m", entity.MaterialClassFabric)
	_, err := f.rep.MaterialStock().ReceiveMaterialStock(ctx, entity.MaterialReceiptInsert{
		MaterialId: materialID, Quantity: d("10"), Lot: ns("A"),
	})
	require.NoError(t, err)
	lots, err := f.rep.MaterialStock().ListMaterialLots(ctx, materialID, false)
	require.NoError(t, err)
	require.Len(t, lots, 1)
	result, err := f.rep.MaterialStock().ShrinkMaterialLot(ctx, entity.MaterialLotShrinkInsert{
		LotId: lots[0].Id, LengthAfter: d("9.5"),
	})
	require.NoError(t, err)
	stock, err := f.rep.MaterialStock().GetMaterialStock(ctx, materialID)
	require.NoError(t, err)
	assert.True(t, stock.OnHand.Equal(d("9.5")))
	assert.False(t, stock.AvgUnitCostBase.Valid)
	assert.False(t, result.Lot.UnitCost.Valid)
	assert.True(t, result.MaterialShrinkagePct.Decimal.Equal(d("5")))
}

func TestMaterialConversionDBLotRequiredWhenALotCovers(t *testing.T) {
	f := newConversionFixture(t)
	ctx := context.Background()
	input := f.material("m", entity.MaterialClassFabric)
	output := f.material("pcs", entity.MaterialClassOther)
	received := f.receipt(input, "20", "4.20", "E-LOT-"+uuid.NewString()[:8], decimal.NullDecimal{})
	require.True(t, received.LotId.Valid)

	_, err := f.rep.MaterialStock().ConvertMaterialStock(ctx, f.convert(input, output, "10", "200"))
	assert.ErrorIs(t, err, entity.ErrMaterialConversionLotRequired)
	assertStock(t, f.rep, input, "20", "4.2")
	var outRows int
	require.NoError(t, f.raw.QueryRow(`SELECT COUNT(*) FROM material_stock WHERE material_id = ? AND on_hand > 0`, output).Scan(&outRows))
	assert.Zero(t, outRows, "a refused conversion must not create output stock")

	withLot := f.convert(input, output, "10", "200")
	withLot.Inputs[0].LotId = received.LotId
	withLot.WorkCost, withLot.WorkCurrency = d("50"), "EUR"
	conv, err := f.rep.MaterialStock().ConvertMaterialStock(ctx, withLot)
	require.NoError(t, err)
	assert.True(t, conv.TotalCostBase.Equal(d("92")))
	assert.True(t, conv.OutputUnitCostBase.Equal(d("0.46")))
	assertStock(t, f.rep, input, "10", "4.2")
	var remaining decimal.Decimal
	require.NoError(t, f.raw.QueryRow(`SELECT remaining_qty FROM material_lot WHERE id = ?`, received.LotId.Int32).Scan(&remaining))
	assert.True(t, remaining.Equal(d("10")), "lot remaining %s, want 10", remaining)

	// No lot covers 10: a short roll plus untracked stock still converts without a lot.
	mixed := f.material("m", entity.MaterialClassFabric)
	f.receipt(mixed, "5", "2", "E-SHORT-"+uuid.NewString()[:8], decimal.NullDecimal{})
	f.receipt(mixed, "25", "2", "", decimal.NullDecimal{})
	mixedOut := f.material("pcs", entity.MaterialClassOther)
	_, err = f.rep.MaterialStock().ConvertMaterialStock(ctx, f.convert(mixed, mixedOut, "10", "20"))
	require.NoError(t, err)
	assertStock(t, f.rep, mixed, "20", "2")
}
