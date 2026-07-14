-- +migrate Up

-- Auto-delivery: AfterShip tracking integration + a timer safety net. Two carrier-level knobs:
--   * aftership_slug: the AfterShip courier slug used to register/poll this carrier's trackings.
--     NULL/empty = the carrier has no tracking API → only the timer fallback applies to its orders.
--   * auto_deliver_after_hours: hours after shipment (shipment.shipping_date) after which an order
--     is silently marked delivered when no real "Delivered" signal arrived (uncovered carrier, stuck
--     tracking, or a missed webhook). Default 336 (14 days) — conservative, "late not false".
--
-- Both defaults (NULL slug, 336h) are backward-compatible: existing carriers keep working with no
-- AfterShip calls and a 14-day safety net. The delivery-sync worker only ever considers orders whose
-- shipment.shipping_date is set, which is populated from this release onward — so no historical order
-- is retroactively auto-delivered.
--
-- Idempotent: guarded ADD COLUMN via information_schema (mirrors 0120). MySQL DDL auto-commits, so a
-- mid-file failure leaves no gorp_migrations row and the file re-runs from the top; the guard makes a
-- re-run a no-op instead of a duplicate-column error. One statement per line for PREPARE/EXECUTE/
-- DEALLOCATE — managed MySQL rejects them joined on one line without multiStatements.

SET @need_slug := (SELECT COUNT(*) = 0 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'shipment_carrier' AND COLUMN_NAME = 'aftership_slug');
SET @sql := IF(@need_slug,
    'ALTER TABLE shipment_carrier ADD COLUMN aftership_slug VARCHAR(64) NULL DEFAULT NULL',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

SET @need_hours := (SELECT COUNT(*) = 0 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'shipment_carrier' AND COLUMN_NAME = 'auto_deliver_after_hours');
SET @sql := IF(@need_hours,
    'ALTER TABLE shipment_carrier ADD COLUMN auto_deliver_after_hours INT NOT NULL DEFAULT 336',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

-- +migrate Down

SET @has_slug := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'shipment_carrier' AND COLUMN_NAME = 'aftership_slug');
SET @sql := IF(@has_slug,
    'ALTER TABLE shipment_carrier DROP COLUMN aftership_slug',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

SET @has_hours := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'shipment_carrier' AND COLUMN_NAME = 'auto_deliver_after_hours');
SET @sql := IF(@has_hours,
    'ALTER TABLE shipment_carrier DROP COLUMN auto_deliver_after_hours',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;
SELECT 1;
