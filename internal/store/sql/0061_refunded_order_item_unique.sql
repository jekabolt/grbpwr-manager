-- +migrate Up
-- Make refunded_order_item the authoritative idempotency ledger for refunds:
-- one row per (order_id, order_item_id). The RefundOrder transaction restores stock
-- only for order items NOT already present here, and inserts with ON DUPLICATE KEY,
-- so a retry after a partial failure (Stripe succeeded, DB step failed) cannot
-- double-restore stock or double-count refunded quantity.
--
-- Historically the table allowed many rows per (order_id, order_item_id): each partial
-- refund inserted a new row and reads aggregated with SUM(quantity_refunded). Before
-- adding the UNIQUE index we must collapse those groups WITHOUT losing already-refunded
-- quantity: fold the summed quantity into the surviving row, then delete the rest.

-- 1) Fold each duplicate group's total refunded quantity into the lowest-id row of the
--    group, so the surviving row carries the full historical quantity.
UPDATE refunded_order_item r
JOIN (
  SELECT MIN(id) AS keep_id, order_id, order_item_id, SUM(quantity_refunded) AS total_qty
  FROM refunded_order_item
  GROUP BY order_id, order_item_id
  HAVING COUNT(*) > 1
) agg
  ON r.id = agg.keep_id
SET r.quantity_refunded = agg.total_qty;

-- 2) Delete the now-redundant duplicate rows, keeping only the lowest-id row per
--    (order_id, order_item_id).
DELETE r FROM refunded_order_item r
JOIN (
  SELECT id,
         ROW_NUMBER() OVER (
           PARTITION BY order_id, order_item_id
           ORDER BY id ASC
         ) AS rn
  FROM refunded_order_item
) ranked
  ON r.id = ranked.id
WHERE ranked.rn > 1;

-- 3) Enforce one ledger row per (order_id, order_item_id).
ALTER TABLE refunded_order_item
  ADD UNIQUE INDEX uniq_refunded_order_item (order_id, order_item_id);

-- +migrate Down
ALTER TABLE refunded_order_item
  DROP INDEX uniq_refunded_order_item;
