-- Cut-to-length materials: a BOM line bought by the metre (elastic, cord, tape, binding, ribbon) can
-- carry a strip, a record that says how long the piece cut for one garment is. A strip is not a
-- pattern piece and never a row of tech_card_piece: fabric consumption, marker and cut sheet do not
-- see it.
--
-- tech_card_cut_strip hangs off the BOM line, whose id and line_key are stable across saves (keyed
-- upsert). Both FKs cascade and the refusal lives in Go (cut_strip_in_use). A RESTRICT here would make
-- DELETE FROM tech_card depend on the order InnoDB walks its cascades.
--
-- NO CHECK on length_mode or the numbers. They are validated in Go with field errors, the way the
-- folds are (0443).
--
-- tech_card_operation_input gets a third kind of input next to piece_id and unit_key (0307). The
-- column is nullable and every existing row stays NULL, so the FK and the unique key have nothing to
-- check on existing rows.
--
-- THE OLD BINARY SURVIVES THIS SCHEMA. It reads the input table by explicit column list, and a row
-- with piece_id and unit_key both NULL matches neither branch of its reader. It deletes inputs by
-- operation, and its inserts leave the new column NULL. The new tables are invisible to it.
--
-- No new value for chk_tccu_consumption_source. The source cut_length is computed on read and is never
-- written to tech_card_colorway_usage.
--
-- Safe under ANSI_QUOTES (single-quoted literals only). One statement per line for PREPARE, no
-- multiStatements. Every DDL below is gated, so a retry after a partial failure is a no-op.

-- +migrate Up

CREATE TABLE IF NOT EXISTS tech_card_cut_strip (
    id            INT PRIMARY KEY AUTO_INCREMENT,
    tech_card_id  INT NOT NULL COMMENT 'FK tech_card(id)',
    bom_item_id   INT NOT NULL COMMENT 'FK tech_card_bom_item(id), the line the strip is cut from',
    line_key      CHAR(26) NOT NULL COMMENT 'ULID, same key space as tech_card_piece.line_key',
    length_mode   VARCHAR(16) NOT NULL COMMENT 'from_seam, per_size or one_length',
    seam_key      CHAR(26) NULL COMMENT 'soft reference to tech_card_seam.seam_key of the same card',
    fit_pct       DECIMAL(5,2) NOT NULL DEFAULT 100.00 COMMENT 'from_seam: cut = seam x fit / 100 + overlap',
    overlap_mm    DECIMAL(7,2) NOT NULL DEFAULT 0.00 COMMENT 'sewn = cut - overlap',
    width_mm      DECIMAL(7,2) NOT NULL COMMENT 'strip width',
    one_length_mm INT NULL COMMENT 'one_length: the length of every size',
    qty           INT NOT NULL DEFAULT 1 COMMENT 'strips of this length per garment',
    display_order INT NOT NULL DEFAULT 0,
    created_at    TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at    TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    CONSTRAINT fk_cut_strip_tech_card FOREIGN KEY (tech_card_id) REFERENCES tech_card(id) ON DELETE CASCADE,
    CONSTRAINT fk_cut_strip_bom_item FOREIGN KEY (bom_item_id) REFERENCES tech_card_bom_item(id) ON DELETE CASCADE,
    CONSTRAINT uniq_cut_strip_line_key UNIQUE (tech_card_id, line_key),
    INDEX idx_cut_strip_bom_item (bom_item_id)
) ENGINE = InnoDB COMMENT 'cut-to-length strips of BOM lines';

CREATE TABLE IF NOT EXISTS tech_card_cut_strip_size (
    strip_id      INT NOT NULL COMMENT 'FK tech_card_cut_strip(id)',
    size_id       INT NOT NULL COMMENT 'size of the card range',
    seam_mm       DECIMAL(9,2) NULL COMMENT 'seam length, computed on the client and sent here',
    manual_cut_mm INT NULL COMMENT 'typed cut length, wins over the formula',
    PRIMARY KEY (strip_id, size_id),
    CONSTRAINT fk_cut_strip_size_strip FOREIGN KEY (strip_id) REFERENCES tech_card_cut_strip(id) ON DELETE CASCADE
) ENGINE = InnoDB COMMENT 'per-size inputs of a cut-to-length strip';

SET @has_col = (SELECT COUNT(*) FROM information_schema.COLUMNS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_operation_input' AND COLUMN_NAME = 'cut_strip_id');
SET @ddl = IF(@has_col = 0, 'ALTER TABLE tech_card_operation_input ADD COLUMN cut_strip_id INT NULL COMMENT ''FK tech_card_cut_strip(id), third input kind'', ALGORITHM=INSTANT', 'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- migrationlint:allow-retroactive cut_strip_id was added NULL on every row in this file, so the FK and the unique key check nothing on existing rows
SET @has_fk = (SELECT COUNT(*) FROM information_schema.TABLE_CONSTRAINTS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_operation_input' AND CONSTRAINT_NAME = 'fk_op_input_cut_strip');
SET @ddl = IF(@has_fk = 0, 'ALTER TABLE tech_card_operation_input ADD CONSTRAINT fk_op_input_cut_strip FOREIGN KEY (cut_strip_id) REFERENCES tech_card_cut_strip(id) ON DELETE CASCADE', 'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @has_uniq = (SELECT COUNT(*) FROM information_schema.STATISTICS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_operation_input' AND INDEX_NAME = 'uniq_op_input_cut_strip');
SET @ddl = IF(@has_uniq = 0, 'ALTER TABLE tech_card_operation_input ADD UNIQUE INDEX uniq_op_input_cut_strip (operation_id, cut_strip_id)', 'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- +migrate Down

SET @has_fk = (SELECT COUNT(*) FROM information_schema.TABLE_CONSTRAINTS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_operation_input' AND CONSTRAINT_NAME = 'fk_op_input_cut_strip');
SET @ddl = IF(@has_fk > 0, 'ALTER TABLE tech_card_operation_input DROP FOREIGN KEY fk_op_input_cut_strip', 'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @has_uniq = (SELECT COUNT(*) FROM information_schema.STATISTICS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_operation_input' AND INDEX_NAME = 'uniq_op_input_cut_strip');
SET @ddl = IF(@has_uniq > 0, 'ALTER TABLE tech_card_operation_input DROP INDEX uniq_op_input_cut_strip', 'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @has_col = (SELECT COUNT(*) FROM information_schema.COLUMNS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_operation_input' AND COLUMN_NAME = 'cut_strip_id');
SET @ddl = IF(@has_col > 0, 'ALTER TABLE tech_card_operation_input DROP COLUMN cut_strip_id', 'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

DROP TABLE IF EXISTS tech_card_cut_strip_size;
DROP TABLE IF EXISTS tech_card_cut_strip;
