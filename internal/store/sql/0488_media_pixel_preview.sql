-- +migrate Up

-- Tiny colour preview (6 px wide PNG as a data: URI, ~100-300 bytes) that frontends draw as a pixel
-- mosaic while the real image loads. Generated at upload from the thumbnail; old rows are filled by
-- cmd/backfill-pixel-preview. Nullable with no default, so an older binary keeps inserting.
-- No m.* join collides with this name (no other table has a pixel_preview column).
--
-- IDEMPOTENCY: DDL auto-commits, so the column has its own gate. ALGORITHM=INSTANT is explicit: if MySQL
-- cannot add the column instantly the ALTER fails loudly instead of copying the table.

SET @col := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'media' AND COLUMN_NAME = 'pixel_preview');
SET @ddl := IF(@col = 0,
    'ALTER TABLE media
        ADD COLUMN pixel_preview VARCHAR(1024) CHARACTER SET ascii NULL,
        ALGORITHM=INSTANT',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- +migrate Down

SET @col := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'media' AND COLUMN_NAME = 'pixel_preview');
SET @ddl := IF(@col = 1,
    'ALTER TABLE media DROP COLUMN pixel_preview',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;
