package order

import (
	"context"
	"fmt"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"golang.org/x/sync/errgroup"
)

func getOrderIds(orders []entity.Order) []int {
	orderIds := make([]int, len(orders))
	for i, order := range orders {
		orderIds[i] = order.Id
	}
	return orderIds
}

func fetchOrderInfo(ctx context.Context, rep dependency.Repository, orders []entity.Order) ([]entity.OrderFull, error) {
	ids := getOrderIds(orders)

	var (
		orderItems     map[int][]entity.OrderItem
		refundedByItem map[int]map[int]int64
		payments       map[string]entity.Payment
		shipments      map[int]entity.Shipment
		promos         map[int]entity.PromoCode
		buyers         map[int]entity.Buyer
		addresses      map[int]addressFull
	)

	db := rep.DB()

	if rep.InTx() {
		var err error
		orderItems, err = getOrdersItems(ctx, db, ids...)
		if err != nil {
			return nil, fmt.Errorf("can't get order items: %w", err)
		}
		payments, err = paymentsByOrderIds(ctx, db, ids)
		if err != nil {
			return nil, fmt.Errorf("can't get payment by id: %w", err)
		}
		shipments, err = shipmentsByOrderIds(ctx, db, ids)
		if err != nil {
			return nil, fmt.Errorf("can't get order shipment: %w", err)
		}
		promos, err = promosByOrderIds(ctx, db, ids)
		if err != nil {
			return nil, fmt.Errorf("can't get order promos: %w", err)
		}
		buyers, err = buyersByOrderIds(ctx, db, ids)
		if err != nil {
			return nil, fmt.Errorf("can't get buyers order by ids %w", err)
		}
		addresses, err = addressesByOrderIds(ctx, db, ids)
		if err != nil {
			return nil, fmt.Errorf("can't get addresses by id: %w", err)
		}
		refundedByItem, err = getRefundedQuantitiesByOrderIds(ctx, db, ids)
		if err != nil {
			return nil, fmt.Errorf("get refunded quantities: %w", err)
		}
	} else {
		g, ctx := errgroup.WithContext(ctx)

		g.Go(func() error {
			var err error
			orderItems, err = getOrdersItems(ctx, db, ids...)
			if err != nil {
				return fmt.Errorf("can't get order items: %w", err)
			}
			return nil
		})

		g.Go(func() error {
			var err error
			payments, err = paymentsByOrderIds(ctx, db, ids)
			if err != nil {
				return fmt.Errorf("can't get payment by id: %w", err)
			}
			return nil
		})

		g.Go(func() error {
			var err error
			shipments, err = shipmentsByOrderIds(ctx, db, ids)
			if err != nil {
				return fmt.Errorf("can't get order shipment: %w", err)
			}
			return nil
		})

		g.Go(func() error {
			var err error
			promos, err = promosByOrderIds(ctx, db, ids)
			if err != nil {
				return fmt.Errorf("can't get order promos: %w", err)
			}
			return nil
		})

		g.Go(func() error {
			var err error
			buyers, err = buyersByOrderIds(ctx, db, ids)
			if err != nil {
				return fmt.Errorf("can't get buyers order by ids %w", err)
			}
			return nil
		})

		g.Go(func() error {
			var err error
			addresses, err = addressesByOrderIds(ctx, db, ids)
			if err != nil {
				return fmt.Errorf("can't get addresses by id: %w", err)
			}
			return nil
		})

		g.Go(func() error {
			var err error
			refundedByItem, err = getRefundedQuantitiesByOrderIds(ctx, db, ids)
			if err != nil {
				return fmt.Errorf("get refunded quantities: %w", err)
			}
			return nil
		})

		if err := g.Wait(); err != nil {
			return nil, err
		}
	}

	refundedOrderItems := mergeRefundedOrderItems(orderItems, refundedByItem)

	ofs := make([]entity.OrderFull, 0, len(orders))

	for _, order := range orders {
		if _, ok := promos[order.Id]; !ok {
			promos[order.Id] = entity.PromoCode{}
		}
		orderItemsList := orderItems[order.Id]
		refundedItems := refundedOrderItems[order.Id]
		if refundedItems == nil {
			refundedItems = []entity.OrderItem{}
		}
		payment := payments[order.UUID]
		shipment := shipments[order.Id]
		buyer := buyers[order.Id]
		addrs := addresses[order.Id]

		ofs = append(ofs, entity.OrderFull{
			Order:              order,
			OrderItems:         orderItemsList,
			RefundedOrderItems: refundedItems,
			Payment:            payment,
			Shipment:           shipment,
			Buyer:              buyer,
			PromoCode:          promos[order.Id],
			Billing:            addrs.billing,
			Shipping:           addrs.shipping,
		})
	}

	return ofs, nil
}
