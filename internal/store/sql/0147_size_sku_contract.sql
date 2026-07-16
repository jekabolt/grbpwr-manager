-- +migrate Up

-- Problem 017 hardening: size-system and ordinal are SKU identity, not optional display metadata.
-- Migration 0131 seeded every built-in size. This follow-up deliberately fails on any custom/partial
-- row instead of inventing an ordinal: ordinals are frozen into sold SKUs, so allocation must be an
-- explicit reviewed write. There is currently no runtime size-create endpoint; any future one must
-- allocate (sku_system, sku_ord) transactionally and can no longer rely on defaults/free text.

-- Adding the CHECK validates existing data first. NULL is rejected explicitly because MySQL CHECK
-- treats UNKNOWN as passing unless the IS NOT NULL terms are present.
SET @need_size_sku_check := (SELECT COUNT(*) = 0 FROM information_schema.TABLE_CONSTRAINTS
    WHERE CONSTRAINT_SCHEMA = DATABASE() AND TABLE_NAME = 'size'
      AND CONSTRAINT_NAME = 'chk_size_sku_contract');
SET @sql := IF(@need_size_sku_check,
    'ALTER TABLE size ADD CONSTRAINT chk_size_sku_contract CHECK (
        sku_ord BETWEEN 1 AND 99
        AND sku_system IS NOT NULL
        AND BINARY sku_system IN (''apparel'',''shoe'',''composite_ta'',''composite_bo'')
    )',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

-- Ordinals are unique within their system. Overlap across systems is intentional; product-level
-- runtime validation below the DB layer guarantees a colourway uses exactly one system.
SET @need_size_sku_unique := (SELECT COUNT(*) = 0 FROM information_schema.STATISTICS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'size'
      AND INDEX_NAME = 'uniq_size_sku_system_ord');
SET @sql := IF(@need_size_sku_unique,
    'ALTER TABLE size ADD UNIQUE INDEX uniq_size_sku_system_ord (sku_system, sku_ord)',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

-- Remove the old 0/NULL defaults. sku_system intentionally remains VARCHAR + CHECK rather than a
-- MySQL ENUM: a NOT NULL ENUM can acquire its first value as an implicit default, which would turn an
-- omitted system into "apparel" instead of rejecting the write.
SET @need_size_sku_types := (SELECT COUNT(*) != 2 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'size'
      AND (
        (COLUMN_NAME = 'sku_ord' AND COLUMN_TYPE = 'smallint unsigned'
          AND IS_NULLABLE = 'NO' AND COLUMN_DEFAULT IS NULL)
        OR
        (COLUMN_NAME = 'sku_system'
          AND COLUMN_TYPE = 'varchar(16)'
          AND IS_NULLABLE = 'NO' AND COLUMN_DEFAULT IS NULL)
      ));
SET @sql := IF(@need_size_sku_types,
    'ALTER TABLE size
        MODIFY COLUMN sku_ord SMALLINT UNSIGNED NOT NULL,
        MODIFY COLUMN sku_system VARCHAR(16) NOT NULL',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;
