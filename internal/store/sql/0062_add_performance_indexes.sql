-- +migrate Up
-- Forward-looking performance indexes.
--
-- These tables are small today (hundreds of rows), so the query planner still
-- prefers full scans and these indexes have little immediate effect. They are
-- added now because customer_order, payment and send_email_request grow with
-- traffic and are full-scanned by the metrics rollups and the cleanup/retry
-- workers; the indexes prevent that scan cost from growing linearly with data.
--
-- Each ADD is guarded via information_schema (same idiom as 0033) so the
-- statement is idempotent: re-running it, or running it when the index already
-- exists, is a no-op. This matters because auto-migrate runs on prod and halts
-- startup on any failed statement.

-- customer_order(order_status_id, placed)
-- Serves the metrics suite (order_status_id IN (...) AND placed >= :from AND placed < :to),
-- GetStuckPlacedOrders (order_status_id = ? AND placed < ?) and the expired-payment join.
-- For each status value the IN expands to a tight (status, placed-range) index scan.
-- Note: this is a superset of the existing single-column idx_customer_order_status_id
-- (left-most prefix), which is kept as-is to keep this migration minimal and low-risk.
SET @sql = IF(
    (SELECT COUNT(*) FROM information_schema.statistics WHERE table_schema = DATABASE() AND table_name = 'customer_order' AND index_name = 'idx_customer_order_status_placed') > 0,
    'SELECT 1',
    'ALTER TABLE customer_order ADD INDEX idx_customer_order_status_placed (order_status_id, placed)'
);
PREPARE stmt FROM @sql;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- payment(expired_at)
-- GetExpiredAwaitingPaymentOrders + stripereconcile filter: p.expired_at IS NOT NULL AND p.expired_at < :now.
SET @sql = IF(
    (SELECT COUNT(*) FROM information_schema.statistics WHERE table_schema = DATABASE() AND table_name = 'payment' AND index_name = 'idx_payment_expired_at') > 0,
    'SELECT 1',
    'ALTER TABLE payment ADD INDEX idx_payment_expired_at (expired_at)'
);
PREPARE stmt FROM @sql;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- send_email_request(sent, next_retry_at)
-- Retry worker scans: sent = false AND send_attempt_count < ? AND (next_retry_at IS NULL OR next_retry_at <= ?).
-- The index lets it seek straight to the few unsent rows instead of scanning every row
-- (each carries a large inline HTML body) on every tick.
SET @sql = IF(
    (SELECT COUNT(*) FROM information_schema.statistics WHERE table_schema = DATABASE() AND table_name = 'send_email_request' AND index_name = 'idx_send_email_request_pending') > 0,
    'SELECT 1',
    'ALTER TABLE send_email_request ADD INDEX idx_send_email_request_pending (sent, next_retry_at)'
);
PREPARE stmt FROM @sql;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- subscriber(created_at)
-- Newsletter time-series metrics range/group on created_at.
SET @sql = IF(
    (SELECT COUNT(*) FROM information_schema.statistics WHERE table_schema = DATABASE() AND table_name = 'subscriber' AND index_name = 'idx_subscriber_created_at') > 0,
    'SELECT 1',
    'ALTER TABLE subscriber ADD INDEX idx_subscriber_created_at (created_at)'
);
PREPARE stmt FROM @sql;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- +migrate Down
SET @sql = IF(
    (SELECT COUNT(*) FROM information_schema.statistics WHERE table_schema = DATABASE() AND table_name = 'customer_order' AND index_name = 'idx_customer_order_status_placed') > 0,
    'ALTER TABLE customer_order DROP INDEX idx_customer_order_status_placed',
    'SELECT 1'
);
PREPARE stmt FROM @sql;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @sql = IF(
    (SELECT COUNT(*) FROM information_schema.statistics WHERE table_schema = DATABASE() AND table_name = 'payment' AND index_name = 'idx_payment_expired_at') > 0,
    'ALTER TABLE payment DROP INDEX idx_payment_expired_at',
    'SELECT 1'
);
PREPARE stmt FROM @sql;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @sql = IF(
    (SELECT COUNT(*) FROM information_schema.statistics WHERE table_schema = DATABASE() AND table_name = 'send_email_request' AND index_name = 'idx_send_email_request_pending') > 0,
    'ALTER TABLE send_email_request DROP INDEX idx_send_email_request_pending',
    'SELECT 1'
);
PREPARE stmt FROM @sql;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @sql = IF(
    (SELECT COUNT(*) FROM information_schema.statistics WHERE table_schema = DATABASE() AND table_name = 'subscriber' AND index_name = 'idx_subscriber_created_at') > 0,
    'ALTER TABLE subscriber DROP INDEX idx_subscriber_created_at',
    'SELECT 1'
);
PREPARE stmt FROM @sql;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;
