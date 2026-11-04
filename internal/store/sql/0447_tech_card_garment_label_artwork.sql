-- +migrate Up

-- Sewn-in labels (T3a). A garment label gets a pin, an artwork mode and artwork files.
--
-- tech_card_garment_label.callout_number is the pin: the number of a tech_card_callout on a flat of
-- this card. No geometry on the label and no FK, callouts are delete-insert on save and the handler
-- clears a number no callout carries. 0 = no pin.
--
-- tech_card_garment_label.artwork_per_colorway and artwork_per_size are two independent modes: one
-- file per colourway, one file per size, both, or neither (one file for the whole style).
--
-- tech_card_garment_label_file is one artwork file of a label, keyed by colourway and size. 0 means
-- any colourway / all sizes, so those two columns carry no FK. The label row is delete-insert on save
-- and its files are written back in the same save, so ON DELETE CASCADE from the label is safe.
-- The library file FK is RESTRICT: a QR already printed depends on that file, DeleteFile names the
-- holding cards before the FK would have to speak, and deleting a file must not silently lose a row.
-- A label row with key composition holds the composition label's location, material and files.
--
-- ADDITIVE ONLY. Every new column gets a DEFAULT, so an older binary that names none still inserts.
-- Rollback to an older binary: its full-replace save drops the file rows, the library files stay.
--
-- ALGORITHM=INSTANT IS EXPLICIT: if MySQL could not do it instantly the ALTER fails loudly instead of
-- copying the table under the five-minute migration ceiling.
--
-- IDEMPOTENCY: DDL auto-commits, so the table is IF NOT EXISTS and each column has its own gate.

CREATE TABLE IF NOT EXISTS tech_card_garment_label_file (
    id INT NOT NULL PRIMARY KEY AUTO_INCREMENT,
    label_id INT NOT NULL,
    colorway_id INT NOT NULL DEFAULT 0 COMMENT 'product.id of this style; 0 = any colourway',
    size_id INT NOT NULL DEFAULT 0 COMMENT 'size.id; 0 = all sizes',
    library_file_id INT NOT NULL,
    source_hash VARCHAR(64) NOT NULL DEFAULT '' COMMENT 'hash of the inputs of a generated file; empty = uploaded by hand',
    UNIQUE KEY uq_tc_garment_label_file (label_id, colorway_id, size_id),
    INDEX idx_tc_garment_label_file_library (library_file_id),
    CONSTRAINT fk_tc_garment_label_file_label FOREIGN KEY (label_id) REFERENCES tech_card_garment_label(id) ON DELETE CASCADE,
    CONSTRAINT fk_tc_garment_label_file_library FOREIGN KEY (library_file_id) REFERENCES library_file(id) ON DELETE RESTRICT
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4 COMMENT 'Artwork files of a garment label, printed as QR on the tech pack (0447)';

SET @tc_label_callout_col := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_garment_label'
      AND COLUMN_NAME = 'callout_number');
SET @ddl := IF(@tc_label_callout_col = 0,
    'ALTER TABLE tech_card_garment_label
        ADD COLUMN callout_number INT NOT NULL DEFAULT 0 COMMENT ''number of a tech_card_callout on a flat of this card; 0 = no pin'',
        ALGORITHM=INSTANT',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @tc_label_per_size_col := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_garment_label'
      AND COLUMN_NAME = 'artwork_per_size');
SET @ddl := IF(@tc_label_per_size_col = 0,
    'ALTER TABLE tech_card_garment_label
        ADD COLUMN artwork_per_size TINYINT(1) NOT NULL DEFAULT 0 COMMENT ''1 = one artwork file per size, 0 = one for all sizes'',
        ALGORITHM=INSTANT',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @tc_label_per_colorway_col := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_garment_label'
      AND COLUMN_NAME = 'artwork_per_colorway');
SET @ddl := IF(@tc_label_per_colorway_col = 0,
    'ALTER TABLE tech_card_garment_label
        ADD COLUMN artwork_per_colorway TINYINT(1) NOT NULL DEFAULT 0 COMMENT ''1 = one artwork file per colourway, 0 = one for all colourways'',
        ALGORITHM=INSTANT',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- +migrate Down

DROP TABLE IF EXISTS tech_card_garment_label_file;

SET @tc_label_per_colorway_back := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_garment_label'
      AND COLUMN_NAME = 'artwork_per_colorway');
SET @ddl := IF(@tc_label_per_colorway_back = 1,
    'ALTER TABLE tech_card_garment_label DROP COLUMN artwork_per_colorway',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @tc_label_per_size_back := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_garment_label'
      AND COLUMN_NAME = 'artwork_per_size');
SET @ddl := IF(@tc_label_per_size_back = 1,
    'ALTER TABLE tech_card_garment_label DROP COLUMN artwork_per_size',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @tc_label_callout_back := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_garment_label'
      AND COLUMN_NAME = 'callout_number');
SET @ddl := IF(@tc_label_callout_back = 1,
    'ALTER TABLE tech_card_garment_label DROP COLUMN callout_number',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;
