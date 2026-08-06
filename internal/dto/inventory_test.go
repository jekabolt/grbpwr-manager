package dto

import (
	"testing"

	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
