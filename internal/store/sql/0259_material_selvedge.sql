-- +migrate Up

-- Кромка (selvedge) as a property of the ROLL, per the owner's model (PIECES-WASTAGE-DESIGN §2.1).
-- The catalog's width_cm has always been the FULL roll width in practice (0095 called the flat copy
-- "usable" but nothing was ever subtracted from it); selvedge_cm is the unusable strip per EDGE in
-- cm, so usable width derives as width_cm minus 2 x selvedge_cm. NOT NULL DEFAULT 0 keeps every
-- existing fabric behaving bit-for-bit as before until someone fills a selvedge in.
--
-- NOTE this number may be renumbered at merge time; two parallel branches hold 0257 (markers) and
-- 0258 (pattern access). Renaming is safe while unapplied.

SET @col_exists := (
    SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'material_fabric_attr'
      AND COLUMN_NAME = 'selvedge_cm'
);
SET @ddl := IF(@col_exists = 0,
    'ALTER TABLE material_fabric_attr ADD COLUMN selvedge_cm DECIMAL(5,2) NOT NULL DEFAULT 0 AFTER width_cm, ADD CONSTRAINT chk_mfa_selvedge_nonneg CHECK (selvedge_cm >= 0)',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- +migrate Down
SET @col_exists := (
    SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'material_fabric_attr'
      AND COLUMN_NAME = 'selvedge_cm'
);
SET @ddl := IF(@col_exists = 1,
    'ALTER TABLE material_fabric_attr DROP CONSTRAINT chk_mfa_selvedge_nonneg, DROP COLUMN selvedge_cm',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;
