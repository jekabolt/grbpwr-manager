package store

import (
	"context"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/dto"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

// TestOrderShipmentInternalCostsFrontendVisibility is the acceptance test for FIX 2 (P1 VISIBILITY):
// a shipment's actual_cost (real carrier invoice) and return_shipping_cost (reverse-logistics cost)
// are INTERNAL margin data (#62) that must NEVER reach storefront customers. The shared/admin order
// projection (ConvertEntityOrderFullToPbOrderFull) keeps them so the admin order/fulfillment detail
// still shows them; the storefront projection (ConvertEntityOrderFullToPbOrderFullStorefront — the
// converter every frontend order read now uses: ListMyOrders, GetOrderByUUIDAndEmail, CancelOrderByUser,
// ValidateOrderByUUID) strips both to nil while preserving the customer-facing shipment Cost.
func TestOrderShipmentInternalCostsFrontendVisibility(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	cfg := *testCfg
	cfg.Automigrate = true
	s, err := NewForTest(ctx, cfg)
	require.NoError(t, err)
	defer s.Close()

	// Initialise the const/dictionary cache (payment methods) so the shared order->pb conversion, which
	// resolves the payment method by id, succeeds.
	commonWriteTestFixtures(ctx, t, s)
	var pmID int
	require.NoError(t, testDB.QueryRowContext(ctx, "SELECT MIN(id) FROM payment_method").Scan(&pmID))

	of := &entity.OrderFull{
		Order: entity.Order{Id: 1, UUID: "vis-test-uuid", Currency: "EUR", OrderStatusId: 1},
		Payment: entity.Payment{PaymentInsert: entity.PaymentInsert{
			PaymentMethodID:                  pmID,
			TransactionAmount:                decimal.NewFromInt(100),
			TransactionAmountPaymentCurrency: decimal.NewFromInt(100),
		}},
		Shipment: entity.Shipment{
			Cost:               decimal.NewFromInt(15),                                    // customer-facing: must survive
			ActualCost:         decimal.NewNullDecimal(decimal.RequireFromString("9.42")), // internal: strip on storefront
			ReturnShippingCost: decimal.NewNullDecimal(decimal.RequireFromString("4.11")), // internal: strip on storefront
		},
	}

	// ADMIN projection retains the internal costs.
	admin, err := dto.ConvertEntityOrderFullToPbOrderFull(of)
	require.NoError(t, err)
	require.NotNil(t, admin.Shipment)
	require.NotNil(t, admin.Shipment.ActualCost, "admin projection must retain actual_cost")
	require.Equal(t, "9.42", admin.Shipment.ActualCost.Value)
	require.NotNil(t, admin.Shipment.ReturnShippingCost, "admin projection must retain return_shipping_cost")
	require.Equal(t, "4.11", admin.Shipment.ReturnShippingCost.Value)

	// STOREFRONT projection strips both internal costs, keeps the customer-facing cost.
	front, err := dto.ConvertEntityOrderFullToPbOrderFullStorefront(of)
	require.NoError(t, err)
	require.NotNil(t, front.Shipment)
	require.Nil(t, front.Shipment.ActualCost, "storefront projection must NOT expose actual_cost")
	require.Nil(t, front.Shipment.ReturnShippingCost, "storefront projection must NOT expose return_shipping_cost")
	require.NotNil(t, front.Shipment.Cost, "customer-facing shipment cost must survive the strip")
	require.Equal(t, "15", front.Shipment.Cost.Value)

	// The strip must not mutate the source entity: a subsequent admin projection still carries the costs.
	admin2, err := dto.ConvertEntityOrderFullToPbOrderFull(of)
	require.NoError(t, err)
	require.NotNil(t, admin2.Shipment.ActualCost, "storefront strip must not leak into a later admin projection")
	require.Equal(t, "9.42", admin2.Shipment.ActualCost.Value)
}
