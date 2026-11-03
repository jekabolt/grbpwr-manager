-- +migrate Up

-- A step states how far apart its seam sides may be before the assembly map calls them different
-- (TechCardOperation.seam_tolerance_mm, proto field 76).
--
-- ADDITIVE ONLY. Born NULL on every row: NULL = «the client's default», 0 is a real value, so no
-- DEFAULT and no backfill. No CHECK (it would COPY the table); the range is checked in dto.
-- Outside every digest, so no stale signatures.
--
-- ALGORITHM=INSTANT IS EXPLICIT: if MySQL could not do it instantly the ALTER fails loudly instead
-- of copying the table under the five-minute migration ceiling.
--
-- IDEMPOTENCY: DDL auto-commits, so the column has its own information_schema gate.
--
-- ROLLBACK: an older binary full-replaces operations without this column and returns the steps to
-- the default. That is a display default, nothing else is lost.

SET @op_seam_tolerance_col := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_operation'
      AND COLUMN_NAME = 'seam_tolerance_mm');
SET @ddl := IF(@op_seam_tolerance_col = 0,
    'ALTER TABLE tech_card_operation
        ADD COLUMN seam_tolerance_mm DECIMAL(6,3) NULL COMMENT ''mm the seam sides of this step may differ before the map flags them; NULL = client default'',
        ALGORITHM=INSTANT',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- +migrate Down

SET @op_seam_tolerance_back := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_operation'
      AND COLUMN_NAME = 'seam_tolerance_mm');
SET @ddl := IF(@op_seam_tolerance_back = 1,
    'ALTER TABLE tech_card_operation DROP COLUMN seam_tolerance_mm',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;
