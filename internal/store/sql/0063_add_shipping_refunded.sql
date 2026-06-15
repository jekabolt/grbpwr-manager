-- +migrate Up
-- Track whether the shipping cost has already been refunded for an order.
--
-- A PartiallyRefunded order can be refunded again (RefundOrder allows it), so two
-- partial refunds each with refundShipping=true would add shipment.cost to
-- refunded_amount (and the Stripe charge) twice. This boolean marker, set inside the
-- refund transaction that holds the customer_order row FOR UPDATE, lets the refund
-- logic add the shipping cost at most once.
--
-- Additive ADD COLUMN of a defaulted (NOT NULL DEFAULT FALSE) column is safe against
-- existing prod data — every existing row gets FALSE. The information_schema guard
-- (same idiom as 0033/0062) makes the statement idempotent so a re-run, or running it
-- when the column already exists, is a no-op; this matters because auto-migrate runs
-- on prod and halts startup on any failed statement.
SET @sql = IF(
    (SELECT COUNT(*) FROM information_schema.columns WHERE table_schema = DATABASE() AND table_name = 'customer_order' AND column_name = 'shipping_refunded') > 0,
    'SELECT 1',
    'ALTER TABLE customer_order ADD COLUMN shipping_refunded BOOLEAN NOT NULL DEFAULT FALSE COMMENT ''Whether the shipping cost has already been refunded for this order'''
);
PREPARE stmt FROM @sql;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- +migrate Down
SET @sql = IF(
    (SELECT COUNT(*) FROM information_schema.columns WHERE table_schema = DATABASE() AND table_name = 'customer_order' AND column_name = 'shipping_refunded') > 0,
    'ALTER TABLE customer_order DROP COLUMN shipping_refunded',
    'SELECT 1'
);
PREPARE stmt FROM @sql;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;
