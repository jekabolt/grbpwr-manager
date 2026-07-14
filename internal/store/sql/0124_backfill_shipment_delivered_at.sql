-- +migrate Up

-- analytics-v2 task 04: backfill shipment.delivered_at (added in 0053 but never written) from the
-- order_status_history audit trail, so delivery-duration metrics have a real delivered timestamp for
-- already-delivered orders. Going forward DeliveredOrder stamps it live. Metrics still derive the
-- timestamp from order_status_history directly (covering orders with no shipment row), so this is a
-- convenience/consistency backfill, not the metric's only source.
--
-- Idempotent: only fills rows still NULL; a re-run is a no-op. UPDATE-only (no DDL), safe on prod data.

UPDATE shipment s
JOIN (
    SELECT h.order_id, MIN(h.changed_at) AS delivered_at
    FROM order_status_history h
    JOIN order_status os ON os.id = h.order_status_id AND os.name = 'delivered'
    GROUP BY h.order_id
) d ON d.order_id = s.order_id
SET s.delivered_at = d.delivered_at
WHERE s.delivered_at IS NULL;
