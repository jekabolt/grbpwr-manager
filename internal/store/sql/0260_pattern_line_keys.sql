-- +migrate Up

-- Ф9.2 (PIECES-WASTAGE-DESIGN §2.1). A pattern row (выкройка sheet) gets a STABLE wire identity,
-- because «replace the sheet with a new file» changes the url, and the url is the only identity the
-- row had. line_key follows the exact mechanics of tech_card_bom_item/tech_card_piece (0159/0168) --
-- the client mints a ULID on first upload and keeps it across file replacement, the store keyed-
-- upserts instead of delete-all, and legacy rows are backfilled with a deterministic key.
--
-- bom_line_key is the «этот DXF кроится из этой ткани» binding, entered at upload time. A plain
-- string, deliberately NOT an FK -- the BOM is upsert-diffed inside the same UpdateTechCard save, a
-- RESTRICT here would abort the whole card save when a fabric slot is deleted (same argument as
-- tech_card_marker.bom_item_id in 0257). A dangling key reads as «слот удалён» in the UI.
--
-- NOTE for the merge train -- this file may be renumbered when parallel branches land; renaming is
-- safe while unapplied.

SET @need := (SELECT COUNT(*) = 0 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_size_pattern' AND COLUMN_NAME = 'line_key');
SET @sql := IF(@need,
    'ALTER TABLE tech_card_size_pattern ADD COLUMN line_key CHAR(26) NULL AFTER tech_card_id, ADD COLUMN bom_line_key CHAR(26) NULL AFTER line_key',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

-- Backfill a deterministic, unique 26-char line_key for legacy rows (real ULIDs come from clients on
-- new uploads). 'LEGACY' + zero-padded id = 26 chars; id is unique so the key is unique. Re-runnable.
UPDATE tech_card_size_pattern SET line_key = CONCAT('LEGACY', LPAD(id, 20, '0'))
    WHERE line_key IS NULL OR line_key = '';

-- UNIQUE (tech_card_id, line_key) -- the upsert-diff matches on it, so the invariant must hold now.
SET @need := (SELECT COUNT(*) = 0 FROM information_schema.STATISTICS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_size_pattern' AND INDEX_NAME = 'uniq_tcsp_line_key');
SET @sql := IF(@need,
    'ALTER TABLE tech_card_size_pattern ADD CONSTRAINT uniq_tcsp_line_key UNIQUE (tech_card_id, line_key)',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

-- +migrate Down
SET @need := (SELECT COUNT(*) > 0 FROM information_schema.STATISTICS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_size_pattern' AND INDEX_NAME = 'uniq_tcsp_line_key');
SET @sql := IF(@need, 'ALTER TABLE tech_card_size_pattern DROP INDEX uniq_tcsp_line_key', 'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

SET @need := (SELECT COUNT(*) > 0 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_size_pattern' AND COLUMN_NAME = 'line_key');
SET @sql := IF(@need, 'ALTER TABLE tech_card_size_pattern DROP COLUMN line_key, DROP COLUMN bom_line_key', 'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;
