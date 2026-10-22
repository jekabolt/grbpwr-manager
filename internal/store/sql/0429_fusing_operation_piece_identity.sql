-- +migrate Up

-- Automatic FUSING steps move from one step per INTERLINING line (holding every fused piece) to one
-- step per fused piece. The step's identity becomes the line_key of the piece it fuses. Per-piece
-- rows deliberately leave auto_fusing_line_key NULL and carry their material through the ordinary
-- INTERLINING BOM link: a pre-0429 binary therefore sees them as manual and cannot regroup or delete
-- them during a rollback. Private write metadata, never on the wire.

SET @col_exists := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_operation'
      AND COLUMN_NAME = 'auto_fusing_piece_key');
SET @ddl := IF(@col_exists = 0,
    'ALTER TABLE tech_card_operation ADD COLUMN auto_fusing_piece_key CHAR(26) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NULL COMMENT ''private automatic FUSING identity: line_key of the one piece this step fuses; NULL = grouped legacy or manual step'' AFTER auto_fusing_line_key',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- No backfill. A grouped automatic step (auto_fusing_line_key set, this column NULL) is split into
-- per-piece steps by the reconciler on the card's next save.

-- +migrate Down

SET @col_exists := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_operation'
      AND COLUMN_NAME = 'auto_fusing_piece_key');
SET @ddl := IF(@col_exists = 1,
    'ALTER TABLE tech_card_operation DROP COLUMN auto_fusing_piece_key',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;
