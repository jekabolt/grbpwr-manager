package order

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"log/slog"

	"github.com/jekabolt/grbpwr-manager/internal/cache"
	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/dto"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
	"github.com/shopspring/decimal"
)

func insertAddresses(ctx context.Context, db dependency.DB, shippingAddress, billingAddress *entity.AddressInsert) (int, int, error) {
	query := `
		INSERT INTO address (country, state, city, address_line_one, address_line_two, company, postal_code)
		VALUES (:country, :state, :city, :address_line_one, :address_line_two, :company, :postal_code);
	`

	insertAddress := func(address *entity.AddressInsert) (int64, error) {
		result, err := db.NamedExecContext(ctx, query, address)
		if err != nil {
			return 0, err
		}
		return result.LastInsertId()
	}

	if *shippingAddress == *billingAddress {
		id, err := insertAddress(shippingAddress)
		if err != nil {
			return 0, 0, err
		}
		return int(id), int(id), nil
	}

	shippingID, err := insertAddress(shippingAddress)
	if err != nil {
		return 0, 0, err
	}

	billingID, err := insertAddress(billingAddress)
	if err != nil {
		return 0, 0, err
	}

	return int(shippingID), int(billingID), nil
}

func insertBuyer(ctx context.Context, db dependency.DB, b *entity.BuyerInsert, sAdr, bAdr int) error {
	phone, err := sanitizePhone(b.Phone)
	if err != nil {
		return fmt.Errorf("invalid phone number: %w", err)
	}

	query := `
	INSERT INTO buyer 
		(order_id, first_name, last_name, email, phone, billing_address_id, shipping_address_id)
	VALUES 
		(:orderId, :firstName, :lastName, :email, :phone, :billingAddressId, :shippingAddressId)
	`

	email := strings.ToLower(strings.TrimSpace(b.Email))
	err = storeutil.ExecNamed(ctx, db, query, map[string]interface{}{
		"orderId":           b.OrderId,
		"firstName":         b.FirstName,
		"lastName":          b.LastName,
		"email":             email,
		"phone":             phone,
		"billingAddressId":  bAdr,
		"shippingAddressId": sAdr,
	})
	if err != nil {
		return fmt.Errorf("can't insert buyer: %w", err)
	}

	return nil
}

func insertPaymentRecord(ctx context.Context, db dependency.DB, paymentMethodId, orderId int, expiredAt time.Time) error {
	insertQuery := `
		INSERT INTO payment (order_id, payment_method_id, transaction_amount, transaction_amount_payment_currency, is_transaction_done, expired_at)
		VALUES (:orderId, :paymentMethodId, 0, 0, false, :expiredAt);
	`

	err := storeutil.ExecNamed(ctx, db, insertQuery, map[string]interface{}{
		"orderId":         orderId,
		"paymentMethodId": paymentMethodId,
		"expiredAt":       sql.NullTime{Time: expiredAt, Valid: true},
	})
	if err != nil {
		return fmt.Errorf("can't insert payment record: %w", err)
	}

	return nil
}

func insertPaymentRecordForCustomOrder(ctx context.Context, db dependency.DB, paymentMethodId, orderId int, totalAmount decimal.Decimal) error {
	insertQuery := `
		INSERT INTO payment (order_id, payment_method_id, transaction_amount, transaction_amount_payment_currency, is_transaction_done, expired_at)
		VALUES (:orderId, :paymentMethodId, :transactionAmount, :transactionAmountPaymentCurrency, true, NULL);
	`
	err := storeutil.ExecNamed(ctx, db, insertQuery, map[string]interface{}{
		"orderId":                          orderId,
		"paymentMethodId":                  paymentMethodId,
		"transactionAmount":                totalAmount,
		"transactionAmountPaymentCurrency": totalAmount,
	})
	if err != nil {
		return fmt.Errorf("can't insert payment record for custom order: %w", err)
	}
	return nil
}

func insertOrderItems(ctx context.Context, db dependency.DB, items []entity.OrderItemInsert, orderID int) error {
	if len(items) == 0 {
		return fmt.Errorf("no order items to insert")
	}
	// Snapshot each line's COGS (base currency) from the product's current cost_price so
	// historical margins stay reproducible when a product is later re-costed. cost_price is
	// confidential and deliberately not loaded on the order-validation product read, so fetch
	// it here directly. A product with no cost stays NULL; metrics fall back to the live
	// product cost for such legacy/uncosted lines.
	costByProduct, err := fetchProductCostPrices(ctx, db, items)
	if err != nil {
		return fmt.Errorf("can't fetch product costs for order-item snapshot: %w", err)
	}
	// Snapshot each line's base-currency (EUR) list price too, so fallback revenue
	// reconstruction (orders without total_settled_base) stays reproducible when a product is
	// later repriced. Products with no base-currency price row stay NULL; metrics fall back to
	// the live base price for such lines.
	basePriceByProduct, err := fetchProductBasePrices(ctx, db, items)
	if err != nil {
		return fmt.Errorf("can't fetch product base prices for order-item snapshot: %w", err)
	}
	// Snapshot each line's variant SKU (product_size.sku) so order history keeps the SKU sold even
	// if the product is later re-minted (task 15). Re-snapshotted at OrderPaymentDone (the freeze
	// point) in case it changed between checkout and payment.
	snapByProductSize, err := fetchVariantSnapshots(ctx, db, items)
	if err != nil {
		return fmt.Errorf("can't fetch variant snapshots for order line: %w", err)
	}
	rows := make([]map[string]any, 0, len(items))
	for _, item := range items {
		// The variant identity snapshot is mandatory and immutable (problems 019/023): a line must
		// resolve to a live variant (product_size) that has a non-empty SKU, or the order is rejected here
		// — before any commit — so order history can never be born without a stable identity.
		// fetchVariantSnapshots omits NULL/empty SKUs, so a missing row (mismatched pair) or a blank SKU
		// both fail this check. variant_id (product_size.id) is the FK RESTRICT anchor of the line.
		snap, ok := snapByProductSize[[2]int{item.ProductId, item.SizeId}]
		if !ok || snap.SKU == "" {
			return fmt.Errorf("cannot create order line: product %d size %d has no live variant SKU", item.ProductId, item.SizeId)
		}
		row := map[string]any{
			"order_id":                orderID,
			"product_id":              item.ProductId,
			"product_price":           item.ProductPriceDecimal(),
			"product_sale_percentage": item.ProductSalePercentageDecimal(),
			"quantity":                item.QuantityDecimal(),
			"size_id":                 item.SizeId,
			"variant_id":              snap.VariantID,
			"cost_price_at_sale":      nil,
			"product_price_base":      nil,
			"variant_sku_snapshot":    snap.SKU,
			"base_sku_snapshot":       snap.BaseSKU,
		}
		if cost, ok := costByProduct[item.ProductId]; ok {
			row["cost_price_at_sale"] = cost
		}
		if base, ok := basePriceByProduct[item.ProductId]; ok {
			row["product_price_base"] = base
		}
		rows = append(rows, row)
	}

	return storeutil.BulkInsert(ctx, db, "order_item", rows)
}

// variantSnapshot is the immutable identity a sold line freezes: the stable variant id (product_size.id,
// the FK RESTRICT anchor), the variant SKU, and the base SKU (product.sku).
type variantSnapshot struct {
	VariantID int    `db:"id"`
	ProductID int    `db:"product_id"`
	SizeID    int    `db:"size_id"`
	SKU       string `db:"variant_sku"`
	BaseSKU   string `db:"base_sku"`
}

// fetchVariantSnapshots returns the live variant id + variant SKU (product_size) and base SKU
// (product.sku) for each (product_id, size_id) referenced by items, keyed by [2]int{productID, sizeID}.
// Rows with a NULL/empty variant sku are omitted (an order line must pin a minted variant).
func fetchVariantSnapshots(ctx context.Context, db dependency.DB, items []entity.OrderItemInsert) (map[[2]int]variantSnapshot, error) {
	ids := distinctProductIDs(items)
	if len(ids) == 0 {
		return map[[2]int]variantSnapshot{}, nil
	}
	rows, err := storeutil.QueryListNamed[variantSnapshot](ctx, db,
		`SELECT ps.id, ps.product_id, ps.size_id, ps.sku AS variant_sku, COALESCE(p.sku, '') AS base_sku
		 FROM product_size ps JOIN product p ON p.id = ps.product_id
		 WHERE ps.product_id IN (:ids) AND ps.sku IS NOT NULL AND ps.sku != ''`,
		map[string]any{"ids": ids})
	if err != nil {
		return nil, err
	}
	out := make(map[[2]int]variantSnapshot, len(rows))
	for _, r := range rows {
		out[[2]int{r.ProductID, r.SizeID}] = r
	}
	return out, nil
}

// distinctProductIDs returns the unique product ids referenced by items, order-preserving.
func distinctProductIDs(items []entity.OrderItemInsert) []int {
	ids := make([]int, 0, len(items))
	seen := make(map[int]struct{}, len(items))
	for _, item := range items {
		if _, ok := seen[item.ProductId]; ok {
			continue
		}
		seen[item.ProductId] = struct{}{}
		ids = append(ids, item.ProductId)
	}
	return ids
}

// fetchProductCostPrices returns the current per-unit cost_price (base currency) for the
// distinct products in items, omitting products whose cost_price is NULL. Used to snapshot
// COGS onto order lines at sale time.
func fetchProductCostPrices(ctx context.Context, db dependency.DB, items []entity.OrderItemInsert) (map[int]decimal.Decimal, error) {
	ids := distinctProductIDs(items)
	if len(ids) == 0 {
		return map[int]decimal.Decimal{}, nil
	}
	rows, err := storeutil.QueryListNamed[struct {
		ID        int             `db:"id"`
		CostPrice decimal.Decimal `db:"cost_price"`
	}](ctx, db, `SELECT id, cost_price FROM product WHERE id IN (:ids) AND cost_price IS NOT NULL`,
		map[string]any{"ids": ids})
	if err != nil {
		return nil, err
	}
	out := make(map[int]decimal.Decimal, len(rows))
	for _, r := range rows {
		out[r.ID] = r.CostPrice
	}
	return out, nil
}

// fetchProductBasePrices returns the current base-currency (EUR) list price for the distinct
// products in items, read from product_price. Products with no base-currency price row are
// omitted. Used to snapshot the base list price onto order lines at sale time.
func fetchProductBasePrices(ctx context.Context, db dependency.DB, items []entity.OrderItemInsert) (map[int]decimal.Decimal, error) {
	ids := distinctProductIDs(items)
	if len(ids) == 0 {
		return map[int]decimal.Decimal{}, nil
	}
	rows, err := storeutil.QueryListNamed[struct {
		ProductID int             `db:"product_id"`
		Price     decimal.Decimal `db:"price"`
	}](ctx, db, `SELECT product_id, price FROM product_price WHERE product_id IN (:ids) AND UPPER(currency) = :base`,
		map[string]any{"ids": ids, "base": strings.ToUpper(cache.GetBaseCurrency())})
	if err != nil {
		return nil, err
	}
	out := make(map[int]decimal.Decimal, len(rows))
	for _, r := range rows {
		out[r.ProductID] = r.Price
	}
	return out, nil
}

func deleteOrderItems(ctx context.Context, db dependency.DB, orderId int) error {
	query := `DELETE FROM order_item WHERE order_id = :orderId`
	err := storeutil.ExecNamed(ctx, db, query, map[string]any{
		"orderId": orderId,
	})
	if err != nil {
		return fmt.Errorf("can't delete order items: %w", err)
	}
	return nil
}

func insertShipment(ctx context.Context, db dependency.DB, sc *entity.ShipmentCarrier, orderId int, cost decimal.Decimal, freeShipping bool) error {
	query := `
	INSERT INTO shipment (carrier_id, order_id, cost, free_shipping)
	VALUES (:carrierId, :orderId, :cost, :freeShipping)
	`
	err := storeutil.ExecNamed(ctx, db, query, map[string]interface{}{
		"carrierId":    sc.Id,
		"orderId":      orderId,
		"cost":         cost,
		"freeShipping": freeShipping,
	})
	if err != nil {
		return fmt.Errorf("can't insert shipment: %w", err)
	}
	return nil
}

func insertOrder(ctx context.Context, db dependency.DB, order *entity.Order) (int, string, error) {
	var err error
	query := `
	INSERT INTO customer_order
	 (uuid, total_price, total_price_eur, currency, order_status_id, promo_id, ga_client_id, vat_rate_pct, vat_amount)
	 VALUES (:uuid, :totalPrice, :totalPriceEur, :currency, :orderStatusId, :promoId, :gaClientId, :vatRatePct, :vatAmount)
	`

	orderRef, err := generateOrderReference()
	if err != nil {
		return 0, "", err
	}
	var totalPriceEur any
	if order.TotalPriceEUR.Valid {
		totalPriceEur = order.TotalPriceEUR.Decimal
	}
	var vatRatePct, vatAmount any
	if order.VatRatePct.Valid {
		vatRatePct = order.VatRatePct.Decimal
	}
	if order.VatAmount.Valid {
		vatAmount = order.VatAmount.Decimal
	}
	order.Id, err = storeutil.ExecNamedLastId(ctx, db, query, map[string]interface{}{
		"uuid":          orderRef,
		"totalPrice":    order.TotalPriceDecimal(),
		"totalPriceEur": totalPriceEur,
		"currency":      order.Currency,
		"orderStatusId": order.OrderStatusId,
		"promoId":       order.PromoId,
		"gaClientId":    order.GAClientID,
		"vatRatePct":    vatRatePct,
		"vatAmount":     vatAmount,
	})
	if err != nil {
		return 0, "", fmt.Errorf("can't insert order: %w", err)
	}
	return order.Id, orderRef, nil
}

func (s *Store) insertOrderDetails(ctx context.Context, db dependency.DB, order *entity.Order, validItemsInsert []entity.OrderItemInsert, carrier *entity.ShipmentCarrier, shipmentCost decimal.Decimal, freeShipping bool, orderNew *entity.OrderNew) error {
	var err error
	// Snapshot the EUR-equivalent total for loyalty qualifying-spend (best effort).
	if eur, eerr := computeTotalPriceEUR(ctx, db, order, validItemsInsert, carrier, shipmentCost, freeShipping); eerr != nil {
		slog.Default().WarnContext(ctx, "can't compute EUR snapshot for order", slog.String("err", eerr.Error()))
	} else {
		order.TotalPriceEUR = eur
	}
	// Resolve + snapshot destination VAT so net-of-VAT revenue is reproducible if a rate later
	// changes. Rate comes from the shipping country (absent/non-EU → 0); prices are
	// VAT-inclusive, so vat_amount = total × rate/(100+rate) in the order currency.
	shippingCountry := ""
	if orderNew.ShippingAddress != nil {
		shippingCountry = orderNew.ShippingAddress.Country
	}
	vatRate, verr := getVatRatePct(ctx, db, shippingCountry)
	if verr != nil {
		return fmt.Errorf("error while resolving vat rate: %w", verr)
	}
	order.VatRatePct = decimal.NullDecimal{Decimal: vatRate, Valid: true}
	order.VatAmount = decimal.NullDecimal{
		Decimal: dto.RoundForCurrency(entity.VatFromInclusive(order.TotalPriceDecimal(), vatRate), order.Currency),
		Valid:   true,
	}
	order.Id, order.UUID, err = insertOrder(ctx, db, order)
	if err != nil {
		return fmt.Errorf("error while inserting final order: %w", err)
	}

	slog.Info("inserting order items", "order_id", order.Id, "items", validItemsInsert)
	if err = insertOrderItems(ctx, db, validItemsInsert, order.Id); err != nil {
		return fmt.Errorf("error while inserting order items: %w", err)
	}
	if err = insertShipment(ctx, db, carrier, order.Id, shipmentCost, freeShipping); err != nil {
		return fmt.Errorf("error while inserting shipment: %w", err)
	}
	shippingAddressId, billingAddressId, err := insertAddresses(ctx, db, orderNew.ShippingAddress, orderNew.BillingAddress)
	if err != nil {
		return fmt.Errorf("error while inserting addresses: %w", err)
	}
	orderNew.Buyer.OrderId = order.Id
	if err = insertBuyer(ctx, db, orderNew.Buyer, shippingAddressId, billingAddressId); err != nil {
		return fmt.Errorf("error while inserting buyer: %w", err)
	}
	return nil
}

// insertRefundedOrderItems records newly refunded units in the refunded_order_item ledger.
// The ledger has a UNIQUE (order_id, order_item_id); ON DUPLICATE KEY UPDATE accumulates
// the quantity so repeated partial refunds of distinct units sum into one row, and a
// retried full refund (where qty is 0 because no new units remain) is a no-op. This keeps
// the whole RefundOrder transaction idempotent against a re-run.
func insertRefundedOrderItems(ctx context.Context, db dependency.DB, orderId int, refundedByItem map[int]int64) error {
	for orderItemId, qty := range refundedByItem {
		if qty <= 0 {
			continue
		}
		query := `INSERT INTO refunded_order_item (order_id, order_item_id, quantity_refunded)
			VALUES (:orderId, :orderItemId, :quantityRefunded)
			ON DUPLICATE KEY UPDATE quantity_refunded = quantity_refunded + VALUES(quantity_refunded)`
		if err := storeutil.ExecNamed(ctx, db, query, map[string]any{
			"orderId":          orderId,
			"orderItemId":      orderItemId,
			"quantityRefunded": qty,
		}); err != nil {
			return fmt.Errorf("insert refunded_order_item: %w", err)
		}
	}
	return nil
}
