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
