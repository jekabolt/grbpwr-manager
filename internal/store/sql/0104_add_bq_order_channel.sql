-- +migrate Up
-- Description: Server-side order→channel attribution cache (task 20 step 2). A re-syncable BQ
--   precompute maps each GA4 client_id (customer_order.ga_client_id, from the browser _ga cookie)
--   to its LAST NON-DIRECT UTM channel. Joining orders to it lets ROAS/CAC use the authoritative
--   settled revenue (total_settled_base) instead of consent/ad-block-lossy GA4 purchase_revenue, and
--   yields per-channel CAC (removing the v1 global-CAC limitation). Like every bq_* table this is a
--   cache, not source of truth; it is rebuilt by the ga4sync worker and purged by `date`.
-- Affected tables: bq_order_channel (new)
-- Type: additive (non-breaking)

-- client_id and the utm_* columns are VARCHAR(191): client_id is the PRIMARY KEY and utm_* mirror
-- channel_spend (191 is the utf8mb4 index-safe length; a real GA client_id like
-- "1363967413.1772796350" is ~21 chars). `date` is the session date of the last non-direct touch,
-- present so the shared retention purge (DELETE ... WHERE date < cutoff) can drop stale mappings.
CREATE TABLE bq_order_channel (
    client_id VARCHAR(191) NOT NULL,
    date DATE NOT NULL,
    utm_source VARCHAR(191) NOT NULL DEFAULT '',
    utm_medium VARCHAR(191) NOT NULL DEFAULT '',
    utm_campaign VARCHAR(191) NOT NULL DEFAULT '',
    updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP NOT NULL,
    PRIMARY KEY (client_id),
    KEY idx_bq_order_channel_date (date)
);

-- +migrate Down

DROP TABLE IF EXISTS bq_order_channel;
