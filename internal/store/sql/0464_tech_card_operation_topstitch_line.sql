-- Measured topstitch lines belong to an operation. Operations are full-replaced on every card save,
-- so their lines cascade with them and are reinserted in the same transaction. The redundant card FK
-- owns the card-wide line-key uniqueness shared in Go with pieces, cut strips and garment labels.
--
-- Anchors use the same JSON shape as one tech_card_seam side. The client measures every size; the
-- server stores those numbers and stamps the pattern-source fingerprint used to report staleness.
--
-- Safe under ANSI_QUOTES (single-quoted literals only). Every DDL statement is retry-safe.

-- +migrate Up

CREATE TABLE IF NOT EXISTS tech_card_operation_topstitch_line (
    id                 INT PRIMARY KEY AUTO_INCREMENT,
    operation_id       INT NOT NULL COMMENT 'FK tech_card_operation(id)',
    tech_card_id       INT NOT NULL COMMENT 'FK tech_card(id)',
    line_key           CHAR(26) NOT NULL COMMENT 'ULID, shared card line-key space',
    bom_item_id        INT NOT NULL COMMENT 'FK tech_card_bom_item(id), thread line',
    anchors            JSON NOT NULL COMMENT 'TechCardSeamAnchor array',
    anchored_size      VARCHAR(16) NOT NULL DEFAULT '',
    stitch_rows        TINYINT NOT NULL,
    source_fingerprint CHAR(64) NULL,
    display_order      INT NOT NULL DEFAULT 0,
    created_at         TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at         TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    CONSTRAINT fk_op_topstitch_operation FOREIGN KEY (operation_id) REFERENCES tech_card_operation(id) ON DELETE CASCADE,
    CONSTRAINT fk_op_topstitch_tech_card FOREIGN KEY (tech_card_id) REFERENCES tech_card(id) ON DELETE CASCADE,
    CONSTRAINT fk_op_topstitch_bom_item FOREIGN KEY (bom_item_id) REFERENCES tech_card_bom_item(id) ON DELETE CASCADE,
    CONSTRAINT chk_op_topstitch_line_rows CHECK (stitch_rows BETWEEN 1 AND 4),
    CONSTRAINT uniq_op_topstitch_line_key UNIQUE (tech_card_id, line_key),
    INDEX idx_op_topstitch_operation (operation_id),
    INDEX idx_op_topstitch_bom_item (bom_item_id)
) ENGINE = InnoDB COMMENT 'measured topstitch lines owned by operations';

CREATE TABLE IF NOT EXISTS tech_card_operation_topstitch_line_size (
    line_id INT NOT NULL COMMENT 'FK tech_card_operation_topstitch_line(id)',
    size_id INT NOT NULL COMMENT 'size of the card range',
    line_mm DECIMAL(9,2) NOT NULL COMMENT 'measured topstitch length in mm',
    PRIMARY KEY (line_id, size_id),
    CONSTRAINT fk_op_topstitch_size_line FOREIGN KEY (line_id) REFERENCES tech_card_operation_topstitch_line(id) ON DELETE CASCADE
) ENGINE = InnoDB COMMENT 'per-size measured topstitch lengths';

SET @has_col = (SELECT COUNT(*) FROM information_schema.COLUMNS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_bom_item' AND COLUMN_NAME = 'thread_per_mm');
SET @ddl = IF(@has_col = 0, 'ALTER TABLE tech_card_bom_item ADD COLUMN thread_per_mm DECIMAL(6,3) NULL COMMENT ''thread mm per sewn mm'', ALGORITHM=INSTANT', 'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- +migrate Down

SET @has_col = (SELECT COUNT(*) FROM information_schema.COLUMNS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_bom_item' AND COLUMN_NAME = 'thread_per_mm');
SET @ddl = IF(@has_col > 0, 'ALTER TABLE tech_card_bom_item DROP COLUMN thread_per_mm', 'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

DROP TABLE IF EXISTS tech_card_operation_topstitch_line_size;
DROP TABLE IF EXISTS tech_card_operation_topstitch_line;
