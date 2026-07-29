-- +migrate Up
-- Site locale captured at purchase time (ISO-639-1: en/fr/de/it/ja/zh/ko), so an order's
-- transactional emails can be sent in the language the customer was shopping in when they have no
-- explicit account preference. Nullable: pre-feature orders and admin custom orders leave it NULL,
-- and the mailer falls back to the account language / EN.
--
-- Idempotent via the information_schema guard (managed MySQL rejects `ADD COLUMN IF NOT EXISTS` —
-- that is MariaDB-only), with PREPARE/EXECUTE/DEALLOCATE one-per-line so a mid-file failure re-runs
-- cleanly (auto-migrate runs on beta+prod and halts startup on any failed statement).
SET @sql = IF(
    (SELECT COUNT(*) FROM information_schema.columns WHERE table_schema = DATABASE() AND table_name = 'customer_order' AND column_name = 'locale') > 0,
    'SELECT 1',
    'ALTER TABLE customer_order ADD COLUMN locale VARCHAR(8) NULL COMMENT ''Storefront site locale at purchase (ISO-639-1), NULL on pre-feature or admin orders'''
);
PREPARE stmt FROM @sql;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- +migrate Down
SET @sql = IF(
    (SELECT COUNT(*) FROM information_schema.columns WHERE table_schema = DATABASE() AND table_name = 'customer_order' AND column_name = 'locale') > 0,
    'ALTER TABLE customer_order DROP COLUMN locale',
    'SELECT 1'
);
PREPARE stmt FROM @sql;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;
