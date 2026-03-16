-- +migrate Up
-- Fix: Backfill financial data using computed product_price_with_sale
-- product_price_with_sale is NOT a column in order_item, it's computed as:
-- product_price * (1 - COALESCE(product_sale_percentage, 0) / 100)

UPDATE product_stock_change_history psch
  JOIN customer_order co ON co.id = psch.order_id
  JOIN order_item oi ON oi.order_id = co.id AND oi.product_id = psch.product_id AND oi.size_id = psch.size_id
  LEFT JOIN promo_code pc ON pc.id = co.promo_id
  SET
    psch.paid_currency = co.currency,
    psch.price_before_discount = ROUND(oi.product_price * ABS(psch.quantity_delta), 2),
    psch.discount_amount = ROUND(
      (oi.product_price - oi.product_price * (1 - COALESCE(oi.product_sale_percentage, 0) / 100)) * ABS(psch.quantity_delta)
      + COALESCE(oi.product_price * (1 - COALESCE(oi.product_sale_percentage, 0) / 100) * ABS(psch.quantity_delta) * pc.discount / 100, 0),
      2
    ),
    psch.paid_amount = ROUND(
      oi.product_price * (1 - COALESCE(oi.product_sale_percentage, 0) / 100) * ABS(psch.quantity_delta)
      - COALESCE(oi.product_price * (1 - COALESCE(oi.product_sale_percentage, 0) / 100) * ABS(psch.quantity_delta) * pc.discount / 100, 0),
      2
    )
  WHERE psch.source IN ('order_paid', 'order_custom')
    AND psch.paid_currency IS NULL
    AND psch.product_id IS NOT NULL;

-- +migrate Down
-- Cannot reliably undo backfill, leave data as-is