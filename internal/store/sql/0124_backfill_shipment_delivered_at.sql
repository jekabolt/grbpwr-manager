-- +migrate Up

-- analytics-v2 task 04: ensure shipment.delivered_at exists, then backfill it from the
-- order_status_history audit trail so delivery-duration metrics have a real delivered timestamp for
-- already-delivered orders. Going forward DeliveredOrder / auto-delivery stamp it live. Metrics still
-- derive the timestamp from order_status_history directly (covering orders with no shipment row), so the
-- backfill is a convenience/consistency step, not the metric's only source.
--
-- WHY the guarded ADD COLUMN: delivered_at was originally added by 0053, but that ADD-COLUMN line was
-- edited INTO 0053 after 0053 had already been applied to prod. sql-migrate tracks by filename and never
-- re-runs an applied file, so prod (which recorded 0053 before the edit) never got the column, while a
-- freshly-migrated DB (beta) did — hence the prod-only "Unknown column 's.delivered_at'". Re-running
-- 0053 is impossible, so this migration reconciles the schema itself: add the column if it is missing,
-- then backfill. Self-contained and safe whether or not delivered_at already exists.
--
-- Idempotent: guarded ADD COLUMN via information_schema (multi-line PREPARE/EXECUTE/DEALLOCATE — a single
-- line trips 1064 on the managed DSN); backfill only fills still-NULL rows so a re-run is a no-op.

SET @need_col := (SELECT COUNT(*) = 0 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'shipment' AND COLUMN_NAME = 'delivered_at');
SET @sql := IF(@need_col,
    'ALTER TABLE shipment ADD COLUMN delivered_at DATETIME NULL DEFAULT NULL',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

-- Backfill delivered_at from the first 'delivered' status transition for orders that reached delivered
-- but have no timestamp yet (historical orders predating delivered_at capture).
UPDATE shipment s
JOIN (
    SELECT h.order_id, MIN(h.changed_at) AS delivered_at
    FROM order_status_history h
    JOIN order_status os ON os.id = h.order_status_id AND os.name = 'delivered'
    GROUP BY h.order_id
) d ON d.order_id = s.order_id
SET s.delivered_at = d.delivered_at
WHERE s.delivered_at IS NULL;
