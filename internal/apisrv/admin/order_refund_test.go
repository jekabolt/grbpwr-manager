package admin

import (
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/shopspring/decimal"
)

func TestCalculateFullRefundAmountSubtractsAlreadyRefunded(t *testing.T) {
	oi := func(id int, price, qty int64) entity.OrderItem {
		return entity.OrderItem{
			Id: id,
			OrderItemInsert: entity.OrderItemInsert{
				ProductPriceWithSale: decimal.NewFromInt(price),
				Quantity:             decimal.NewFromInt(qty),
			},
		}
	}

	base := &entity.OrderFull{
		Order:      entity.Order{Currency: "EUR"},
		OrderItems: []entity.OrderItem{oi(1, 10, 2)},
	}

	// Nothing refunded yet: full 2 units => 20.
	if got := calculateFullRefundAmount(base, false); !got.Equal(decimal.NewFromInt(20)) {
		t.Errorf("no prior refund: got %s, want 20", got)
	}

	// 1 unit already refunded: only 1 remaining unit => 10 (was 20 before the fix).
	withRefund := *base
	withRefund.RefundedOrderItems = []entity.OrderItem{oi(1, 10, 1)}
	if got := calculateFullRefundAmount(&withRefund, false); !got.Equal(decimal.NewFromInt(10)) {
		t.Errorf("1 unit refunded: got %s, want 10", got)
	}

	// All units already refunded: 0, not a negative or full re-charge.
	allRefunded := *base
	allRefunded.RefundedOrderItems = []entity.OrderItem{oi(1, 10, 2)}
	if got := calculateFullRefundAmount(&allRefunded, false); !got.Equal(decimal.Zero) {
		t.Errorf("all units refunded: got %s, want 0", got)
	}
}
