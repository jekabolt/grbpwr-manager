-- +migrate Up

-- FUSING wave L1: the cut-piece owns both the interlining choice and the geometry used to cut it.
-- Every new column is nullable so the existing rows keep their exact meaning: no selected line means
-- «auto-resolve the sole interlining line», and absent geometry/settings mean the documented defaults.
--
-- The old fusing-mode CHECK cannot be extended safely here. MySQL re-validates the whole table when
-- ADD CONSTRAINT CHECK runs, which turns one prod-only historical row into a failed boot. Go closes
-- the vocabulary on every write; this migration only removes the now-too-small schema vocabulary so
-- the new `zone` token can be stored. The independent width CHECK remains valid: every non-strip mode
-- requires a NULL width.
--
-- Every DDL statement has its own information_schema guard. MySQL auto-commits DDL, so a mid-file
-- failure leaves a partial schema and the next boot must be able to replay the file from the top.

SET @fusing_mode_check_exists := (SELECT COUNT(*) FROM information_schema.TABLE_CONSTRAINTS
    WHERE CONSTRAINT_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_piece'
      AND CONSTRAINT_NAME = 'chk_tcp_fusing_mode' AND CONSTRAINT_TYPE = 'CHECK');
SET @ddl := IF(@fusing_mode_check_exists = 1,
    'ALTER TABLE tech_card_piece DROP CHECK chk_tcp_fusing_mode',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @col_exists := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_piece'
      AND COLUMN_NAME = 'fusing_bom_item_id');
SET @ddl := IF(@col_exists = 0,
    'ALTER TABLE tech_card_piece ADD COLUMN fusing_bom_item_id INT NULL COMMENT ''selected INTERLINING BOM line; NULL auto-resolves the sole line'' AFTER fusing_width_mm',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @col_exists := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_piece'
      AND COLUMN_NAME = 'fusing_edges');
SET @ddl := IF(@col_exists = 0,
    'ALTER TABLE tech_card_piece ADD COLUMN fusing_edges JSON NULL COMMENT ''strip edges: [{edge,start_mm,end_mm}]'' AFTER fusing_bom_item_id',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @col_exists := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_piece'
      AND COLUMN_NAME = 'fusing_zone');
SET @ddl := IF(@col_exists = 0,
    'ALTER TABLE tech_card_piece ADD COLUMN fusing_zone JSON NULL COMMENT ''zone geometry: rect or dxf_piece_key'' AFTER fusing_edges',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @col_exists := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_piece'
      AND COLUMN_NAME = 'fusing_inset_mm');
SET @ddl := IF(@col_exists = 0,
    'ALTER TABLE tech_card_piece ADD COLUMN fusing_inset_mm DECIMAL(5,1) NULL COMMENT ''inset in mm; NULL defaults to 3.0'' AFTER fusing_zone',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @col_exists := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_piece'
      AND COLUMN_NAME = 'fusing_inset_net');
SET @ddl := IF(@col_exists = 0,
    'ALTER TABLE tech_card_piece ADD COLUMN fusing_inset_net TINYINT(1) NULL COMMENT ''1 means inset equals the piece seam allowance'' AFTER fusing_inset_mm',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @col_exists := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_piece'
      AND COLUMN_NAME = 'fusing_grain');
SET @ddl := IF(@col_exists = 0,
    'ALTER TABLE tech_card_piece ADD COLUMN fusing_grain VARCHAR(16) NULL COMMENT ''as_piece|lengthwise|crosswise|bias|any; NULL means as_piece'' AFTER fusing_inset_net',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @col_exists := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_piece'
      AND COLUMN_NAME = 'fusing_darts');
SET @ddl := IF(@col_exists = 0,
    'ALTER TABLE tech_card_piece ADD COLUMN fusing_darts VARCHAR(8) NULL COMMENT ''cut|fuse; NULL defaults to cut'' AFTER fusing_grain',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @fk_exists := (SELECT COUNT(*) FROM information_schema.TABLE_CONSTRAINTS
    WHERE CONSTRAINT_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_piece'
      AND CONSTRAINT_NAME = 'fk_tcp_fusing_bom_item' AND CONSTRAINT_TYPE = 'FOREIGN KEY');
SET @ddl := IF(@fk_exists = 0,
    'ALTER TABLE tech_card_piece ADD CONSTRAINT fk_tcp_fusing_bom_item FOREIGN KEY (fusing_bom_item_id) REFERENCES tech_card_bom_item(id) ON DELETE SET NULL',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- +migrate Down

SET @fk_exists := (SELECT COUNT(*) FROM information_schema.TABLE_CONSTRAINTS
    WHERE CONSTRAINT_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_piece'
      AND CONSTRAINT_NAME = 'fk_tcp_fusing_bom_item' AND CONSTRAINT_TYPE = 'FOREIGN KEY');
SET @ddl := IF(@fk_exists = 1,
    'ALTER TABLE tech_card_piece DROP FOREIGN KEY fk_tcp_fusing_bom_item',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @col_exists := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_piece'
      AND COLUMN_NAME = 'fusing_darts');
SET @ddl := IF(@col_exists = 1, 'ALTER TABLE tech_card_piece DROP COLUMN fusing_darts', 'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @col_exists := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_piece'
      AND COLUMN_NAME = 'fusing_grain');
SET @ddl := IF(@col_exists = 1, 'ALTER TABLE tech_card_piece DROP COLUMN fusing_grain', 'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @col_exists := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_piece'
      AND COLUMN_NAME = 'fusing_inset_net');
SET @ddl := IF(@col_exists = 1, 'ALTER TABLE tech_card_piece DROP COLUMN fusing_inset_net', 'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @col_exists := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_piece'
      AND COLUMN_NAME = 'fusing_inset_mm');
SET @ddl := IF(@col_exists = 1, 'ALTER TABLE tech_card_piece DROP COLUMN fusing_inset_mm', 'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @col_exists := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_piece'
      AND COLUMN_NAME = 'fusing_zone');
SET @ddl := IF(@col_exists = 1, 'ALTER TABLE tech_card_piece DROP COLUMN fusing_zone', 'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @col_exists := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_piece'
      AND COLUMN_NAME = 'fusing_edges');
SET @ddl := IF(@col_exists = 1, 'ALTER TABLE tech_card_piece DROP COLUMN fusing_edges', 'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @col_exists := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_piece'
      AND COLUMN_NAME = 'fusing_bom_item_id');
SET @ddl := IF(@col_exists = 1, 'ALTER TABLE tech_card_piece DROP COLUMN fusing_bom_item_id', 'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;
