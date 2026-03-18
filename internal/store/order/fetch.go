package order

import (
	"context"
	"errors"
	"fmt"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/dto"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
	"github.com/shopspring/decimal"
)

// fetchAndMapByOrderId is a generic helper that fetches items and maps them by order ID.
func fetchAndMapByOrderId[T any](
	ctx context.Context,
	db dependency.DB,
	orderIds []int,
	query string,
	extractor func(T) int,
) (map[int]T, error) {
	if len(orderIds) == 0 {
		return map[int]T{}, nil
	}

	items, err := storeutil.QueryListNamed[T](ctx, db, query, map[string]interface{}{
		"orderIds": orderIds,
	})
	if err != nil {
		return nil, err
	}

	result := make(map[int]T, len(orderIds))
	for _, item := range items {
		result[extractor(item)] = item
	}
	return result, nil
}

type addressFull struct {
	shipping entity.Address
	billing  entity.Address
}

type refundedQuantityRow struct {
	OrderItemId      int   `db:"order_item_id"`
	OrderId          int   `db:"order_id"`
	QuantityRefunded int64 `db:"quantity_refunded"`
}

func getOrderById(ctx context.Context, db dependency.DB, orderId int) (*entity.Order, error) {
	query := `
	SELECT * from customer_order WHERE id = :orderId`
	order, err := storeutil.QueryNamedOne[entity.Order](ctx, db, query, map[string]interface{}{
		"orderId": orderId,
	})
	if err != nil {
		return nil, err
	}
	return &order, nil
}

func getOrderByUUID(ctx context.Context, db dependency.DB, uuid string) (*entity.Order, error) {
	query := `
	SELECT * from customer_order WHERE uuid = :uuid`
	order, err := storeutil.QueryNamedOne[entity.Order](ctx, db, query, map[string]interface{}{
		"uuid": uuid,
	})
	if err != nil {
		return nil, err
	}
	return &order, nil
}

func getOrderByUUIDForUpdate(ctx context.Context, db dependency.DB, uuid string) (*entity.Order, error) {
	query := `
	SELECT * from customer_order WHERE uuid = :uuid FOR UPDATE`
	order, err := storeutil.QueryNamedOne[entity.Order](ctx, db, query, map[string]interface{}{
		"uuid": uuid,
	})
	if err != nil {
		return nil, err
	}
	return &order, nil
}

func getOrderByUUIDAndEmailForUpdate(ctx context.Context, db dependency.DB, orderUUID string, email string) (*entity.Order, error) {
	query := `
		SELECT co.*
		FROM customer_order co
		INNER JOIN buyer b ON co.id = b.order_id
		WHERE co.uuid = :orderUUID AND b.email = :email
		FOR UPDATE
	`
	order, err := storeutil.QueryNamedOne[entity.Order](ctx, db, query, map[string]interface{}{
		"orderUUID": orderUUID,
		"email":     email,
	})
	if err != nil {
		return nil, fmt.Errorf("can't get order by uuid and email: %w", err)
	}
	return &order, nil
}

func getOrdersItems(ctx context.Context, db dependency.DB, orderIds ...int) (map[int][]entity.OrderItem, error) {
	if len(orderIds) == 0 {
		return map[int][]entity.OrderItem{}, nil
	}

	query := `
        SELECT 
			oi.id,
			oi.order_id,
			oi.product_id,
			oi.quantity,
			oi.size_id,
			oi.product_price,
			oi.product_sale_percentage,
			oi.product_price * (1 - COALESCE(oi.product_sale_percentage, 0) / 100) AS product_price_with_sale,
			m.thumbnail,
			m.blur_hash,
			p.brand AS product_brand,
			p.sku AS product_sku,
			p.color AS color,
			p.top_category_id AS top_category_id,
			p.sub_category_id AS sub_category_id,
			p.type_id AS type_id,
			p.target_gender AS target_gender,
			p.preorder AS preorder
        FROM order_item oi
        JOIN product p ON oi.product_id = p.id
		JOIN media m ON p.thumbnail_id = m.id
        WHERE oi.order_id IN (:orderIds)
    `

	ois, err := storeutil.QueryListNamed[entity.OrderItem](ctx, db, query, map[string]interface{}{
		"orderIds": orderIds,
	})
	if err != nil {
		return nil, err
	}

	productIds := make([]int, 0, len(ois))
	for _, oi := range ois {
		productIds = append(productIds, oi.ProductId)
	}

	translationMap, err := fetchProductTranslations(ctx, db, productIds)
	if err != nil {
		return nil, fmt.Errorf("can't get product translations: %w", err)
	}

	orderItemsMap := make(map[int][]entity.OrderItem, len(orderIds))

	for _, oi := range ois {
		oi.Translations = translationMap[oi.ProductId]

		productName := "product"
		if len(translationMap[oi.ProductId]) > 0 {
			productName = translationMap[oi.ProductId][0].Name
		}

		oi.Slug = dto.GetProductSlug(oi.ProductId, oi.ProductBrand, productName, oi.TargetGender.String())
		orderItemsMap[oi.OrderId] = append(orderItemsMap[oi.OrderId], oi)
	}

	return orderItemsMap, nil
}

func fetchProductTranslations(ctx context.Context, db dependency.DB, productIds []int) (map[int][]entity.ProductTranslationInsert, error) {
	if len(productIds) == 0 {
		return map[int][]entity.ProductTranslationInsert{}, nil
	}

	query := `SELECT product_id, language_id, name, description FROM product_translation WHERE product_id IN (:productIds) ORDER BY product_id, language_id`
	type translationRow struct {
		ProductId int `db:"product_id"`
		entity.ProductTranslationInsert
	}

	rows, err := storeutil.QueryListNamed[translationRow](ctx, db, query, map[string]any{
		"productIds": productIds,
	})
	if err != nil {
		return nil, fmt.Errorf("fetch product translations: %w", err)
	}

	result := make(map[int][]entity.ProductTranslationInsert, len(productIds))
	for _, r := range rows {
		result[r.ProductId] = append(result[r.ProductId], r.ProductTranslationInsert)
	}
	return result, nil
}

func getOrderItemsInsert(ctx context.Context, db dependency.DB, orderId int) ([]entity.OrderItemInsert, error) {
	query := `
		SELECT 
			product_id,
			product_price,
			product_sale_percentage,
			product_price * (1 - COALESCE(product_sale_percentage, 0) / 100) AS product_price_with_sale,
			quantity,
			size_id
		FROM order_item 
		WHERE order_id = :orderId
	`

	ois, err := storeutil.QueryListNamed[entity.OrderItemInsert](ctx, db, query, map[string]any{
		"orderId": orderId,
	})
	if err != nil {
		return nil, fmt.Errorf("can't get order items by order id: %w", err)
	}

	return ois, nil
}

func getOrderShipment(ctx context.Context, db dependency.DB, orderId int) (*entity.Shipment, error) {
	query := `
	SELECT 
		s.* 
	FROM shipment s 
	WHERE s.order_id = :orderId`

	s, err := storeutil.QueryNamedOne[entity.Shipment](ctx, db, query, map[string]any{
		"orderId": orderId,
	})
	if err != nil {
		return nil, err
	}
	return &s, nil
}

func shipmentsByOrderIds(ctx context.Context, db dependency.DB, orderIds []int) (map[int]entity.Shipment, error) {
	query := `
	SELECT 
		s.*
	FROM shipment s 
	WHERE s.order_id IN (:orderIds)`

	shipments, err := fetchAndMapByOrderId[entity.Shipment](ctx, db, orderIds, query, func(s entity.Shipment) int {
		return s.OrderId
	})
	if err != nil {
		return nil, fmt.Errorf("can't get shipments by order ids: %w", err)
	}

	return shipments, nil
}

func getBuyerById(ctx context.Context, db dependency.DB, orderId int) (*entity.Buyer, error) {
	query := `
	SELECT * FROM buyer WHERE order_id = :orderId`
	buyer, err := storeutil.QueryNamedOne[entity.Buyer](ctx, db, query, map[string]interface{}{
		"orderId": orderId,
	})
	if err != nil {
		return nil, err
	}
	return &buyer, nil
}

func buyersByOrderIds(ctx context.Context, db dependency.DB, orderIds []int) (map[int]entity.Buyer, error) {
	query := `
	SELECT 
		customer_order.id AS order_id,
		buyer.id AS id,
		buyer.first_name AS first_name,
		buyer.last_name AS last_name,
		buyer.email AS email,
		buyer.phone AS phone,
		buyer.billing_address_id AS billing_address_id,
		buyer.shipping_address_id AS shipping_address_id,
		COALESCE(subscriber.receive_promo_emails, FALSE) AS receive_promo_emails
	FROM buyer
	JOIN customer_order ON buyer.order_id = customer_order.id
	LEFT JOIN subscriber ON buyer.email = subscriber.email
	WHERE customer_order.id IN (:orderIds)`

	buyers, err := fetchAndMapByOrderId[entity.Buyer](ctx, db, orderIds, query, func(b entity.Buyer) int {
		return b.OrderId
	})
	if err != nil {
		return nil, fmt.Errorf("can't get buyers by order ids: %w", err)
	}

	if len(buyers) != len(orderIds) {
		return nil, fmt.Errorf("not all order IDs were found: expected %d, got %d", len(orderIds), len(buyers))
	}

	return buyers, nil
}

func paymentsByOrderIds(ctx context.Context, db dependency.DB, orderIds []int) (map[string]entity.Payment, error) {
	if len(orderIds) == 0 {
		return map[string]entity.Payment{}, nil
	}

	type paymentOrderUUID struct {
		OrderUUID string `db:"order_uuid"`
		entity.Payment
	}

	query := `
	SELECT 
		customer_order.uuid as order_uuid,
		payment.id,
		payment.order_id, 
		payment.payment_method_id,
		payment.transaction_id, 
		payment.transaction_amount, 
		payment.transaction_amount_payment_currency,
		payment.client_secret,
		payment.is_transaction_done,
		payment.created_at,
		payment.modified_at,
		payment.expired_at
	FROM payment
	JOIN customer_order ON payment.order_id = customer_order.id
	WHERE customer_order.id IN (:orderIds)`

	query, params, err := storeutil.MakeQuery(query, map[string]interface{}{
		"orderIds": orderIds,
	})
	if err != nil {
		return nil, fmt.Errorf("can't make query: %w", err)
	}

	dbRows, err := db.QueryxContext(ctx, query, params...)
	if err != nil {
		return nil, fmt.Errorf("query execution failed: %w", err)
	}
	defer dbRows.Close()

	payments := make(map[string]entity.Payment, len(orderIds))

	for dbRows.Next() {
		var paymentRow paymentOrderUUID

		if err := dbRows.StructScan(&paymentRow); err != nil {
			return nil, fmt.Errorf("row scan failed: %w", err)
		}
		payments[paymentRow.OrderUUID] = paymentRow.Payment
	}

	if err := dbRows.Err(); err != nil {
		return nil, fmt.Errorf("row iteration error: %w", err)
	}

	return payments, nil
}

func addressesByOrderIds(ctx context.Context, db dependency.DB, orderIds []int) (map[int]addressFull, error) {
	if len(orderIds) == 0 {
		return map[int]addressFull{}, nil
	}

	query := `
	SELECT 
		co.id AS order_id,
		billing.country AS billing_country,
		billing.state AS billing_state,
		billing.city AS billing_city,
		billing.address_line_one AS billing_address_line_one,
		billing.address_line_two AS billing_address_line_two,
		billing.company AS billing_company,
		billing.postal_code AS billing_postal_code,
		shipping.country AS shipping_country,
		shipping.state AS shipping_state,
		shipping.city AS shipping_city,
		shipping.address_line_one AS shipping_address_line_one,
		shipping.address_line_two AS shipping_address_line_two,
		shipping.company AS shipping_company,
		shipping.postal_code AS shipping_postal_code
	FROM 
		customer_order co
	INNER JOIN 
		buyer b ON co.id = b.order_id
	INNER JOIN 
		address billing ON b.billing_address_id = billing.id
	INNER JOIN 
		address shipping ON b.shipping_address_id = shipping.id
	WHERE 
		co.id IN (:orderIds)`

	query, params, err := storeutil.MakeQuery(query, map[string]interface{}{
		"orderIds": orderIds,
	})
	if err != nil {
		return nil, fmt.Errorf("can't make query: %w", err)
	}

	dbRows, err := db.QueryxContext(ctx, query, params...)
	if err != nil {
		return nil, err
	}
	defer dbRows.Close()

	addresses := make(map[int]addressFull)

	for dbRows.Next() {
		var shipping entity.Address
		var billing entity.Address
		var orderId int

		err := dbRows.Scan(
			&orderId,
			&billing.Country,
			&billing.State,
			&billing.City,
			&billing.AddressLineOne,
			&billing.AddressLineTwo,
			&billing.Company,
			&billing.PostalCode,
			&shipping.Country,
			&shipping.State,
			&shipping.City,
			&shipping.AddressLineOne,
			&shipping.AddressLineTwo,
			&shipping.Company,
			&shipping.PostalCode,
		)
		if err != nil {
			return nil, err
		}

		addresses[orderId] = addressFull{
			shipping: shipping,
			billing:  billing,
		}
	}

	if err := dbRows.Err(); err != nil {
		return nil, err
	}

	if len(addresses) != len(orderIds) {
		return nil, errors.New("not all order IDs were found")
	}

	return addresses, nil
}

func promosByOrderIds(ctx context.Context, db dependency.DB, orderIds []int) (map[int]entity.PromoCode, error) {
	if len(orderIds) == 0 {
		return map[int]entity.PromoCode{}, nil
	}

	type promoWithOrderId struct {
		OrderId int `db:"order_id"`
		entity.PromoCode
	}

	query := `
    SELECT 
		customer_order.id as order_id,
        promo_code.id, 
        promo_code.code, 
        promo_code.free_shipping, 
        promo_code.discount, 
        promo_code.expiration, 
		promo_code.start,
        promo_code.voucher, 
        promo_code.allowed
    FROM promo_code
    JOIN customer_order ON promo_code.id = customer_order.promo_id
    WHERE customer_order.id IN (:orderIds)`

	query, params, err := storeutil.MakeQuery(query, map[string]interface{}{
		"orderIds": orderIds,
	})
	if err != nil {
		return nil, fmt.Errorf("can't make query: %w", err)
	}

	dbRows, err := db.QueryxContext(ctx, query, params...)
	if err != nil {
		return nil, err
	}
	defer dbRows.Close()

	promos := make(map[int]entity.PromoCode)

	for dbRows.Next() {
		var promoRow promoWithOrderId

		if err := dbRows.StructScan(&promoRow); err != nil {
			return nil, err
		}

		promos[promoRow.OrderId] = promoRow.PromoCode
	}

	if err := dbRows.Err(); err != nil {
		return nil, err
	}

	return promos, nil
}

func getRefundedQuantitiesByOrderIds(ctx context.Context, db dependency.DB, orderIds []int) (map[int]map[int]int64, error) {
	if len(orderIds) == 0 {
		return map[int]map[int]int64{}, nil
	}
	query := `
		SELECT order_item_id, order_id, SUM(quantity_refunded) AS quantity_refunded
		FROM refunded_order_item
		WHERE order_id IN (:orderIds)
		GROUP BY order_item_id, order_id
	`
	rows, err := storeutil.QueryListNamed[refundedQuantityRow](ctx, db, query, map[string]any{"orderIds": orderIds})
	if err != nil {
		return nil, fmt.Errorf("get refunded quantities: %w", err)
	}
	result := make(map[int]map[int]int64)
	for _, r := range rows {
		if result[r.OrderId] == nil {
			result[r.OrderId] = make(map[int]int64)
		}
		result[r.OrderId][r.OrderItemId] = r.QuantityRefunded
	}
	return result, nil
}

func mergeRefundedOrderItems(orderItems map[int][]entity.OrderItem, refundedByItem map[int]map[int]int64) map[int][]entity.OrderItem {
	result := make(map[int][]entity.OrderItem, len(orderItems))
	for orderId, items := range orderItems {
		qtyMap := refundedByItem[orderId]
		if qtyMap == nil {
			continue
		}
		for _, item := range items {
			if qty := qtyMap[item.Id]; qty > 0 {
				item.Quantity = decimal.NewFromInt(qty)
				result[orderId] = append(result[orderId], item)
			}
		}
	}
	return result
}
