-- Fold lines of a garment label: a CSV of positions in mm along the label's height from its top edge,
-- ascending, no spaces ("12,88", "50"). The server normalises the list on write; '' = no lines, the
-- client derives them from fold as before. NOT NULL DEFAULT '' so every existing row reads as no lines.
--
-- WHAT A ROLLBACK OF THE BINARY DOES. The column stays and the old binary starts: its label INSERT
-- names its columns, so the default '' is written. Its first whole-card save therefore clears the lines
-- of that card; rolling forward cannot reconstruct them.
--
-- Safe under ANSI_QUOTES (single-quoted literals only). One statement per PREPARE, no
-- multiStatements. Every DDL below is gated, so a retry after a partial failure is a no-op.

-- +migrate Up

SET @has_col = (SELECT COUNT(*) FROM information_schema.COLUMNS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_garment_label' AND COLUMN_NAME = 'fold_lines_mm');
SET @ddl = IF(@has_col = 0, 'ALTER TABLE tech_card_garment_label ADD COLUMN fold_lines_mm VARCHAR(255) NOT NULL DEFAULT '''' COMMENT ''fold lines, CSV of mm from the top edge'', ALGORITHM=INSTANT', 'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- +migrate Down

SET @has_col = (SELECT COUNT(*) FROM information_schema.COLUMNS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_garment_label' AND COLUMN_NAME = 'fold_lines_mm');
SET @ddl = IF(@has_col > 0, 'ALTER TABLE tech_card_garment_label DROP COLUMN fold_lines_mm', 'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;
