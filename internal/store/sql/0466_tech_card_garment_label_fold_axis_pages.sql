-- Garment label fold axis and booklet pages.
-- fold_axis: '' = fold and stitch lines run along the label's height, measured from the top edge, which is
-- the sewn one (legacy, every existing row); 'width' = they are measured from the left edge, which is sewn,
-- and the range checks use width_mm. The server normalises any other value to ''.
-- pages: pages of a booklet label, 1..12 (the server clamps); 1 = an ordinary label.
-- NOT NULL with defaults so every existing row reads as before.
--
-- WHAT A ROLLBACK OF THE BINARY DOES. The columns stay and the old binary starts: its label INSERT names
-- its columns, so the defaults are written. Its first whole-card save therefore resets axis and pages of
-- that card; rolling forward cannot reconstruct them.
--
-- Safe under ANSI_QUOTES (single-quoted literals only). One statement per PREPARE, no
-- multiStatements. Every DDL below is gated, so a retry after a partial failure is a no-op.

-- +migrate Up

SET @has_col = (SELECT COUNT(*) FROM information_schema.COLUMNS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_garment_label' AND COLUMN_NAME = 'fold_axis');
SET @ddl = IF(@has_col = 0, 'ALTER TABLE tech_card_garment_label ADD COLUMN fold_axis VARCHAR(8) NOT NULL DEFAULT '''' COMMENT ''fold/stitch axis: empty = along the height, width = along the width'', ALGORITHM=INSTANT', 'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @has_col = (SELECT COUNT(*) FROM information_schema.COLUMNS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_garment_label' AND COLUMN_NAME = 'pages');
SET @ddl = IF(@has_col = 0, 'ALTER TABLE tech_card_garment_label ADD COLUMN pages INT NOT NULL DEFAULT 1 COMMENT ''booklet pages, 1..12'', ALGORITHM=INSTANT', 'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- +migrate Down

SET @has_col = (SELECT COUNT(*) FROM information_schema.COLUMNS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_garment_label' AND COLUMN_NAME = 'pages');
SET @ddl = IF(@has_col > 0, 'ALTER TABLE tech_card_garment_label DROP COLUMN pages', 'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @has_col = (SELECT COUNT(*) FROM information_schema.COLUMNS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_garment_label' AND COLUMN_NAME = 'fold_axis');
SET @ddl = IF(@has_col > 0, 'ALTER TABLE tech_card_garment_label DROP COLUMN fold_axis', 'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;
