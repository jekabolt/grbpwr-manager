-- Garment-label geometry gives every label a stable line_key, physical width and height, and a
-- closed fold token. A label key shares the piece/cut-strip key space so a seam-table pose row can
-- store the label position on a step picture.
--
-- The key is intentionally nullable. Existing rows keep NULL and receive a key only when a current
-- binary reads and saves the whole card. The unique key is therefore soft for old data while still
-- protecting every keyed label within its card.
--
-- tech_card_operation_label is also deliberately a soft label_key, not an FK to a label id. Garment
-- labels are delete+insert children of a whole-card save and their ids are not stable. The operation
-- FK is stable for the lifetime of the save and cascades with the operation.
--
-- WHAT A ROLLBACK OF THE BINARY DOES. The schema stays and the old binary starts because every new
-- label column is nullable and all inserts name their columns. It cannot see geometry, fold, stable
-- keys or operation-label links. Its first whole-card save deletes and reinserts both operations and
-- labels: cascades remove the operation-label rows, and the old label INSERT writes NULL into every
-- new column. Rolling forward cannot reconstruct those values. Existing label pose seams remain in
-- the seam table until that save changes them, but the old binary does not recognise the label key as
-- a legal seam anchor and therefore cannot write such a seam back.
--
-- Safe under ANSI_QUOTES (single-quoted literals only). One statement per PREPARE, no
-- multiStatements. Every DDL below is gated, so a retry after a partial failure is a no-op.

-- +migrate Up

SET @has_col = (SELECT COUNT(*) FROM information_schema.COLUMNS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_garment_label' AND COLUMN_NAME = 'line_key');
SET @ddl = IF(@has_col = 0, 'ALTER TABLE tech_card_garment_label ADD COLUMN line_key CHAR(26) NULL COMMENT ''ULID, same key space as pieces and cut strips'', ALGORITHM=INSTANT', 'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @has_col = (SELECT COUNT(*) FROM information_schema.COLUMNS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_garment_label' AND COLUMN_NAME = 'width_mm');
SET @ddl = IF(@has_col = 0, 'ALTER TABLE tech_card_garment_label ADD COLUMN width_mm DECIMAL(7,2) NULL COMMENT ''label width, mm'', ALGORITHM=INSTANT', 'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @has_col = (SELECT COUNT(*) FROM information_schema.COLUMNS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_garment_label' AND COLUMN_NAME = 'height_mm');
SET @ddl = IF(@has_col = 0, 'ALTER TABLE tech_card_garment_label ADD COLUMN height_mm DECIMAL(7,2) NULL COMMENT ''label height, mm'', ALGORITHM=INSTANT', 'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @has_col = (SELECT COUNT(*) FROM information_schema.COLUMNS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_garment_label' AND COLUMN_NAME = 'fold');
SET @ddl = IF(@has_col = 0, 'ALTER TABLE tech_card_garment_label ADD COLUMN fold VARCHAR(16) NULL COMMENT ''flat, half, ends, mitre or loop'', ALGORITHM=INSTANT', 'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

UPDATE tech_card_garment_label SET fold = 'flat', folding = NULL WHERE fold IS NULL AND LOWER(TRIM(folding)) IN ('flat', 'flat fold', 'no fold');
UPDATE tech_card_garment_label SET fold = 'half', folding = NULL WHERE fold IS NULL AND LOWER(TRIM(folding)) IN ('half', 'half fold', 'centre fold', 'center fold', 'folded in half', 'centerfold', 'centrefold');
UPDATE tech_card_garment_label SET fold = 'ends', folding = NULL WHERE fold IS NULL AND LOWER(TRIM(folding)) IN ('ends', 'end fold', 'ends folded', 'end folded', 'folded ends');
UPDATE tech_card_garment_label SET fold = 'mitre', folding = NULL WHERE fold IS NULL AND LOWER(TRIM(folding)) IN ('mitre', 'miter', 'mitre fold', 'miter fold');
UPDATE tech_card_garment_label SET fold = 'loop', folding = NULL WHERE fold IS NULL AND LOWER(TRIM(folding)) IN ('loop', 'loop fold', 'looped');

-- migrationlint:allow-retroactive line_key was added NULL and is not SQL-backfilled, so every existing row is exempt from the unique comparison
SET @has_uniq = (SELECT COUNT(*) FROM information_schema.STATISTICS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_garment_label' AND INDEX_NAME = 'uniq_garment_label_line_key');
SET @ddl = IF(@has_uniq = 0, 'ALTER TABLE tech_card_garment_label ADD UNIQUE INDEX uniq_garment_label_line_key (tech_card_id, line_key)', 'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

CREATE TABLE IF NOT EXISTS tech_card_operation_label (
    operation_id INT NOT NULL COMMENT 'FK tech_card_operation(id)',
    label_key    CHAR(26) NOT NULL COMMENT 'soft reference to a garment label line_key of the card',
    display_order INT NOT NULL DEFAULT 0,
    PRIMARY KEY (operation_id, label_key),
    CONSTRAINT fk_operation_label_operation FOREIGN KEY (operation_id) REFERENCES tech_card_operation(id) ON DELETE CASCADE
) ENGINE = InnoDB COMMENT 'garment labels attached or hung by an operation';

-- +migrate Down

DROP TABLE IF EXISTS tech_card_operation_label;

SET @has_col = (SELECT COUNT(*) FROM information_schema.COLUMNS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_garment_label' AND COLUMN_NAME = 'fold');
SET @ddl = IF(@has_col > 0, 'UPDATE tech_card_garment_label SET folding = fold WHERE folding IS NULL AND fold IN (''flat'', ''half'', ''ends'', ''mitre'', ''loop'')', 'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @has_uniq = (SELECT COUNT(*) FROM information_schema.STATISTICS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_garment_label' AND INDEX_NAME = 'uniq_garment_label_line_key');
SET @ddl = IF(@has_uniq > 0, 'ALTER TABLE tech_card_garment_label DROP INDEX uniq_garment_label_line_key', 'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @has_col = (SELECT COUNT(*) FROM information_schema.COLUMNS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_garment_label' AND COLUMN_NAME = 'fold');
SET @ddl = IF(@has_col > 0, 'ALTER TABLE tech_card_garment_label DROP COLUMN fold', 'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @has_col = (SELECT COUNT(*) FROM information_schema.COLUMNS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_garment_label' AND COLUMN_NAME = 'height_mm');
SET @ddl = IF(@has_col > 0, 'ALTER TABLE tech_card_garment_label DROP COLUMN height_mm', 'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @has_col = (SELECT COUNT(*) FROM information_schema.COLUMNS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_garment_label' AND COLUMN_NAME = 'width_mm');
SET @ddl = IF(@has_col > 0, 'ALTER TABLE tech_card_garment_label DROP COLUMN width_mm', 'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @has_col = (SELECT COUNT(*) FROM information_schema.COLUMNS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_garment_label' AND COLUMN_NAME = 'line_key');
SET @ddl = IF(@has_col > 0, 'ALTER TABLE tech_card_garment_label DROP COLUMN line_key', 'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;
