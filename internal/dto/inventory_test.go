package dto

import (
	"database/sql"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

// TestConvertPbReceiveMaterialStock_InputVatGuard pins the H-5 soft double-VAT guard: input VAT above
// 30% of the net line cost (unit_cost * qty) is rejected as a likely gross unit_cost.
func TestConvertPbReceiveMaterialStock_InputVatGuard(t *testing.T) {
	base := func() *pb_admin.ReceiveMaterialStockRequest {
		// net line cost = 100 * 10 = 1000 -> 30% cap = 300.
		return &pb_admin.ReceiveMaterialStockRequest{
			MaterialId: 1, Quantity: dec("10"), UnitCost: dec("100"), Currency: "EUR",
		}
	}

	// 23% input VAT (230 <= 300) is accepted.
	ok := base()
	ok.InputVatAmount = dec("230")
	ok.InputVatRegime = "domestic_pl"
	_, err := ConvertPbReceiveMaterialStock(ok)
	require.NoError(t, err)

	// 350 > 300 -> rejected with a NET hint (unit_cost was probably entered gross).
	bad := base()
	bad.InputVatAmount = dec("350")
	bad.InputVatRegime = "domestic_pl"
	_, err = ConvertPbReceiveMaterialStock(bad)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "NET")

	// input VAT without a unit_cost has no net base to bound, so the guard does not fire.
	noCost := &pb_admin.ReceiveMaterialStockRequest{
		MaterialId: 1, Quantity: dec("10"), Currency: "EUR",
		InputVatAmount: dec("350"), InputVatRegime: "domestic_pl",
	}
	_, err = ConvertPbReceiveMaterialStock(noCost)
	require.NoError(t, err)
}

// TestConvertPbReceiveMaterialStock_RollFactsNeedALot pins Ф5а.1's acceptance rule. Both roll facts
// are recorded on the LOT the receipt opens or tops up, and upsertLotOnReceipt returns early when the
// receipt names no lot code — so without this guard a client that measured a roll and forgot the lot
// got a 200 and no data, which is the one outcome an operator can never detect.
func TestConvertPbReceiveMaterialStock_RollFactsNeedALot(t *testing.T) {
	base := func() *pb_admin.ReceiveMaterialStockRequest {
		return &pb_admin.ReceiveMaterialStockRequest{MaterialId: 1, Quantity: dec("10")}
	}

	// A width with no lot is refused, and the message names the field to fill in.
	noLot := base()
	noLot.MeasuredWidthCm = dec("148")
	_, err := ConvertPbReceiveMaterialStock(noLot)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "lot")

	// So is a shade with no lot — and whitespace is not a lot code, because the store trims it away.
	blankLot := base()
	blankLot.Lot = "   "
	blankLot.ShadeCode = "SH-7"
	_, err = ConvertPbReceiveMaterialStock(blankLot)
	require.Error(t, err)

	// A plain receipt that carries neither fact is untouched by the rule.
	plain := base()
	_, err = ConvertPbReceiveMaterialStock(plain)
	require.NoError(t, err)

	// With a lot, both are accepted and the shade is trimmed on the way in.
	full := base()
	full.Lot = "ROLL-1"
	full.MeasuredWidthCm = dec("148")
	full.ShadeCode = "  SH-7  "
	ins, err := ConvertPbReceiveMaterialStock(full)
	require.NoError(t, err)
	assert.Equal(t, "SH-7", ins.ShadeCode.String)
	assert.True(t, ins.MeasuredWidthCm.Valid)

	// DECIMAL(6,2) would silently round a third decimal place and hand a different width back on the
	// next read; the marker is then made for a width nobody typed.
	overPrecise := base()
	overPrecise.Lot = "ROLL-1"
	overPrecise.MeasuredWidthCm = dec("148.456")
	_, err = ConvertPbReceiveMaterialStock(overPrecise)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "decimal places")
}

func TestConvertPbReceiveMaterialStock_InvoiceFileID(t *testing.T) {
	withInvoice, err := ConvertPbReceiveMaterialStock(&pb_admin.ReceiveMaterialStockRequest{
		MaterialId: 1, Quantity: dec("10"), InvoiceFileId: 42,
	})
	require.NoError(t, err)
	require.True(t, withInvoice.InvoiceFileId.Valid)
	assert.Equal(t, int32(42), withInvoice.InvoiceFileId.Int32)

	withoutInvoice, err := ConvertPbReceiveMaterialStock(&pb_admin.ReceiveMaterialStockRequest{
		MaterialId: 1, Quantity: dec("10"), InvoiceFileId: 0,
	})
	require.NoError(t, err)
	assert.False(t, withoutInvoice.InvoiceFileId.Valid)
}

func TestAllocateMaterialReceiptBatchCosts_ExactTotalsAndLargestRemainder(t *testing.T) {
	lines := []materialReceiptCostLine{
		{Quantity: decimal.RequireFromString("1"), UnitCost: decimal.RequireFromString("1")},
		{Quantity: decimal.RequireFromString("1"), UnitCost: decimal.RequireFromString("1")},
		{Quantity: decimal.RequireFromString("4"), UnitCost: decimal.RequireFromString("1")},
	}
	alloc, err := allocateMaterialReceiptBatchCosts(
		lines, decimal.RequireFromString("0.0002"), decimal.RequireFromString("0.02"))
	require.NoError(t, err)
	require.Len(t, alloc, 3)

	deliverySum := decimal.Zero
	vatSum := decimal.Zero
	for _, line := range alloc {
		deliverySum = deliverySum.Add(line.DeliveryShare)
		vatSum = vatSum.Add(line.InputVatShare)
	}
	assert.True(t, deliverySum.Equal(decimal.RequireFromString("0.0002")))
	assert.True(t, vatSum.Equal(decimal.RequireFromString("0.02")))
	// Ordinary rounding allocates only half of each tiny total. The full residual belongs to line 3,
	// whose net value (4) is larger than either sibling's (1).
	assert.True(t, alloc[0].DeliveryShare.IsZero())
	assert.True(t, alloc[1].DeliveryShare.IsZero())
	assert.True(t, alloc[2].DeliveryShare.Equal(decimal.RequireFromString("0.0002")))
	assert.True(t, alloc[0].InputVatShare.IsZero())
	assert.True(t, alloc[1].InputVatShare.IsZero())
	assert.True(t, alloc[2].InputVatShare.Equal(decimal.RequireFromString("0.02")))
	assert.True(t, alloc[2].LandedUnitCost.Equal(decimal.RequireFromString("1.0001")))
}

func TestConvertPbReceiveMaterialStockBatch_AllocationAndGuards(t *testing.T) {
	req := &pb_admin.ReceiveMaterialStockBatchRequest{
		Lines: []*pb_admin.ReceiveMaterialStockLine{
			{MaterialId: 1, Quantity: dec("2"), UnitCost: dec("10"), Lot: "A"},
			{MaterialId: 2, Quantity: dec("4"), UnitCost: dec("5"), Lot: "B"},
		},
		Currency: "eur", SupplierId: 3, SupplierDoc: "INV-7", InvoiceFileId: 9,
		DeliveryCost: dec("3"), InputVatAmount: dec("5"), InputVatRegime: "domestic_pl",
	}
	got, err := ConvertPbReceiveMaterialStockBatch(req)
	require.NoError(t, err)
	require.Len(t, got.Lines, 2)
	assert.True(t, got.Lines[0].UnitCost.Decimal.Equal(decimal.RequireFromString("10.75")))
	assert.True(t, got.Lines[1].UnitCost.Decimal.Equal(decimal.RequireFromString("5.375")))
	assert.True(t, got.Lines[0].InputVatAmount.Decimal.Equal(decimal.RequireFromString("2.5")))
	assert.True(t, got.Lines[1].InputVatAmount.Decimal.Equal(decimal.RequireFromString("2.5")))
	assert.Equal(t, "EUR", got.Lines[0].Currency)
	assert.Equal(t, int32(9), got.Lines[1].InvoiceFileId.Int32)

	duplicate := proto.Clone(req).(*pb_admin.ReceiveMaterialStockBatchRequest)
	duplicate.Lines[1] = &pb_admin.ReceiveMaterialStockLine{MaterialId: 1, Quantity: dec("1"), UnitCost: dec("1")}
	_, err = ConvertPbReceiveMaterialStockBatch(duplicate)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "more than once")

	uncosted := proto.Clone(req).(*pb_admin.ReceiveMaterialStockBatchRequest)
	uncosted.Lines[1] = &pb_admin.ReceiveMaterialStockLine{MaterialId: 2, Quantity: dec("1")}
	_, err = ConvertPbReceiveMaterialStockBatch(uncosted)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "every line")
}

// TestConvertEntityMaterialMovementToPb_OccurredAtFallback pins the defensive date: an undated
// movement (a row an older binary wrote during a deploy overlap) shows its created_at's UTC day, a
// dated one keeps its own date.
func TestConvertEntityMaterialMovementToPb_OccurredAtFallback(t *testing.T) {
	created := time.Date(2026, 10, 20, 23, 30, 0, 0, time.UTC)

	undated := ConvertEntityMaterialMovementToPb(entity.MaterialMovement{Id: 1, CreatedAt: created})
	require.NotNil(t, undated.OccurredAt)
	assert.Equal(t, time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC), undated.OccurredAt.AsTime())

	own := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	dated := ConvertEntityMaterialMovementToPb(entity.MaterialMovement{
		Id: 2, CreatedAt: created, OccurredAt: sql.NullTime{Time: own, Valid: true},
	})
	require.NotNil(t, dated.OccurredAt)
	assert.Equal(t, own, dated.OccurredAt.AsTime())
}
