-- +migrate Up

-- Shipping-label fields on shipment for carrier-generated labels (AfterShip Shipping / Postmen v3).
-- Filled by GenerateShippingLabel when an operator generates a label; the tracking_code and the
-- Shipped status transition are still written by the shared SetTrackingNumber path (shipOrder), so a
-- manually-entered tracking number keeps working unchanged. carrier_shipment_id is the AfterShip
-- label id, kept both for a future void/refetch and as the idempotency guard against a double
-- CreateLabel (which would double-charge the carrier account).
--
-- Idempotent: guarded ADD COLUMN via information_schema (multi-line PREPARE/EXECUTE/DEALLOCATE — a
-- single line trips 1064 on the managed DSN; see 0124). Guards on label_url so a mid-file re-run
-- after a partial DDL apply is a no-op.

SET @need_cols := (SELECT COUNT(*) = 0 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'shipment' AND COLUMN_NAME = 'label_url');
SET @sql := IF(@need_cols,
    'ALTER TABLE shipment
        ADD COLUMN label_url           VARCHAR(1024) NULL DEFAULT NULL,
        ADD COLUMN carrier_shipment_id VARCHAR(255)  NULL DEFAULT NULL,
        ADD COLUMN label_service_type  VARCHAR(64)   NULL DEFAULT NULL,
        ADD COLUMN label_created_at    DATETIME      NULL DEFAULT NULL,
        ADD COLUMN parcel_weight_grams INT           NULL DEFAULT NULL,
        ADD COLUMN parcel_dimensions   VARCHAR(128)  NULL DEFAULT NULL',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;
