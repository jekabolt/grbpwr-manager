-- Garment label on a fabric piece, and prints on a label.
--
-- 1. The piece (attachment on_fabric_piece): the label is sewn onto a small spare piece of fabric whose
-- top edge goes into the seam. New columns on tech_card_garment_label, all with defaults so every
-- existing row reads as "no piece":
--   piece_bom_item_id  the fabric the piece is cut from, a BOM line of the same card; NULL = none.
--                      FK ON DELETE SET NULL, like the label's own bom_item_id.
--   piece_width_mm, piece_height_mm  0 = no piece.
--   piece_seam_mm      how deep the piece's top edge goes into the seam.
--   label_x_mm, label_y_mm  the label's top-left on the piece, from the piece's top-left.
--   label_on_piece     '' | 4_sides | top_edge (the server normalises anything else to '').
-- With a piece the server refuses a label that does not fit inside it below the seam allowance.
--
-- 2. tech_card_garment_label_print: printed artworks on a label, placed in mm from the label's top-left,
-- unfolded. The label row is delete-insert on save and its prints are written back in the same save,
-- so ON DELETE CASCADE from the label is safe. The library file FK is RESTRICT, like the label files.
--
-- WHAT A ROLLBACK OF THE BINARY DOES. The columns and the table stay; the old binary's label INSERT names
-- its columns, so the defaults are written and the cascade drops the prints: its first whole-card save
-- loses the piece and the prints of that card.
--
-- Safe under ANSI_QUOTES (single-quoted literals only). One statement per PREPARE, no multiStatements.
-- The columns are one INSTANT ALTER (atomic), gated on one of them; the FK is its own ALTER, gated on its
-- name; the table is IF NOT EXISTS. A retry after a partial failure is a no-op.

-- migrationlint:allow-retroactive the FK covers piece_bom_item_id, added NULL just above, so no existing row can fail it

-- +migrate Up

SET @has_col = (SELECT COUNT(*) FROM information_schema.COLUMNS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_garment_label' AND COLUMN_NAME = 'piece_width_mm');
SET @ddl = IF(@has_col = 0, 'ALTER TABLE tech_card_garment_label ADD COLUMN piece_bom_item_id INT NULL COMMENT ''fabric of the piece: tech_card_bom_item of this card'', ADD COLUMN piece_width_mm DECIMAL(7,2) NOT NULL DEFAULT 0 COMMENT ''0 = no piece'', ADD COLUMN piece_height_mm DECIMAL(7,2) NOT NULL DEFAULT 0 COMMENT ''0 = no piece'', ADD COLUMN piece_seam_mm DECIMAL(5,2) NOT NULL DEFAULT 0 COMMENT ''depth of the piece top edge in the seam'', ADD COLUMN label_x_mm DECIMAL(7,2) NOT NULL DEFAULT 0 COMMENT ''label left edge from the piece left edge'', ADD COLUMN label_y_mm DECIMAL(7,2) NOT NULL DEFAULT 0 COMMENT ''label top edge from the piece top edge'', ADD COLUMN label_on_piece VARCHAR(16) NOT NULL DEFAULT '''' COMMENT ''empty, 4_sides or top_edge'', ALGORITHM=INSTANT', 'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @has_fk = (SELECT COUNT(*) FROM information_schema.TABLE_CONSTRAINTS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_garment_label' AND CONSTRAINT_NAME = 'fk_tc_garment_label_piece_bom');
SET @ddl = IF(@has_fk = 0, 'ALTER TABLE tech_card_garment_label ADD CONSTRAINT fk_tc_garment_label_piece_bom FOREIGN KEY (piece_bom_item_id) REFERENCES tech_card_bom_item(id) ON DELETE SET NULL', 'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

CREATE TABLE IF NOT EXISTS tech_card_garment_label_print (
    id INT NOT NULL PRIMARY KEY AUTO_INCREMENT,
    label_id INT NOT NULL,
    display_order INT NOT NULL DEFAULT 0,
    name VARCHAR(64) NOT NULL DEFAULT '',
    library_file_id INT NULL COMMENT 'the artwork; NULL = none yet',
    method VARCHAR(32) NOT NULL COMMENT 'screen, heat_transfer, woven, embroidery or pad_print',
    colour VARCHAR(64) NOT NULL DEFAULT '',
    width_mm DECIMAL(7,2) NOT NULL,
    height_mm DECIMAL(7,2) NOT NULL,
    x_mm DECIMAL(7,2) NOT NULL COMMENT 'from the label left edge, unfolded',
    y_mm DECIMAL(7,2) NOT NULL COMMENT 'from the label top edge, unfolded',
    INDEX idx_tc_garment_label_print_label (label_id, display_order),
    INDEX idx_tc_garment_label_print_library (library_file_id),
    CONSTRAINT fk_tc_garment_label_print_label FOREIGN KEY (label_id) REFERENCES tech_card_garment_label(id) ON DELETE CASCADE,
    CONSTRAINT fk_tc_garment_label_print_library FOREIGN KEY (library_file_id) REFERENCES library_file(id) ON DELETE RESTRICT
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4 COMMENT 'Printed artworks on a garment label (0467)';

-- +migrate Down

DROP TABLE IF EXISTS tech_card_garment_label_print;

SET @has_fk = (SELECT COUNT(*) FROM information_schema.TABLE_CONSTRAINTS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_garment_label' AND CONSTRAINT_NAME = 'fk_tc_garment_label_piece_bom');
SET @ddl = IF(@has_fk > 0, 'ALTER TABLE tech_card_garment_label DROP FOREIGN KEY fk_tc_garment_label_piece_bom', 'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @has_col = (SELECT COUNT(*) FROM information_schema.COLUMNS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_garment_label' AND COLUMN_NAME = 'piece_width_mm');
SET @ddl = IF(@has_col > 0, 'ALTER TABLE tech_card_garment_label DROP COLUMN piece_bom_item_id, DROP COLUMN piece_width_mm, DROP COLUMN piece_height_mm, DROP COLUMN piece_seam_mm, DROP COLUMN label_x_mm, DROP COLUMN label_y_mm, DROP COLUMN label_on_piece', 'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;
