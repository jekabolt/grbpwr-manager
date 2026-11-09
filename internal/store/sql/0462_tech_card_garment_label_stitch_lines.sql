-- Stitch lines of a garment label: a CSV of positions in mm along the label's height from its top edge,
-- ascending, no spaces ("0,40", "100"). 0 is the top edge and the height the bottom edge, both allowed
-- (that edge is sewn). The server normalises the list on write; '' = none. NOT NULL DEFAULT '' so every
-- existing row reads as no stitch lines.
--
-- WHAT A ROLLBACK OF THE BINARY DOES. The column stays and the old binary starts: its label INSERT
-- names its columns, so the default '' is written. Its first whole-card save therefore clears the lines
-- of that card; rolling forward cannot reconstruct them.
--
-- Safe under ANSI_QUOTES (single-quoted literals only). One statement per PREPARE, no
-- multiStatements. Every DDL below is gated, so a retry after a partial failure is a no-op.

-- +migrate Up

SET @has_col = (SELECT COUNT(*) FROM information_schema.COLUMNS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_garment_label' AND COLUMN_NAME = 'stitch_lines_mm');
SET @ddl = IF(@has_col = 0, 'ALTER TABLE tech_card_garment_label ADD COLUMN stitch_lines_mm VARCHAR(255) NOT NULL DEFAULT '''' COMMENT ''stitch lines, CSV of mm from the top edge'', ALGORITHM=INSTANT', 'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- +migrate Down

SET @has_col = (SELECT COUNT(*) FROM information_schema.COLUMNS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_garment_label' AND COLUMN_NAME = 'stitch_lines_mm');
SET @ddl = IF(@has_col > 0, 'ALTER TABLE tech_card_garment_label DROP COLUMN stitch_lines_mm', 'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;
