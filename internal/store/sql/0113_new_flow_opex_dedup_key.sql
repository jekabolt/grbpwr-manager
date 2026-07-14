-- +migrate Up

-- new-flow NF-08 fix: opex_line was deduped by UNIQUE (month, category, label). That key made
-- materialisation dedup on the template LABEL, which is wrong in two ways:
--   * renaming a template (label/category) changed the key of every future materialised row, so the
--     old rows stayed AND new rows were booked alongside — past months double-booked (nf08-01);
--   * two distinct templates sharing (category, label) — e.g. two seamstresses both «зарплата — швея»
--     — collided on the same key, so the second silently no-op'd every month (nf08-02).
--
-- The correct identity of a materialised line is (recurring_id, month): one row per template per
-- month, regardless of label. Manual/aggregate lines (recurring_id NULL) still dedup by
-- (month, category, label). MySQL has no partial index, so a generated `manual_key` (1 for manual
-- lines, NULL for materialised) makes a single UNIQUE serve only the manual rows — NULLs are distinct
-- in a UNIQUE index, so materialised rows never participate and two same-label templates coexist.
--
-- Safe against existing data: current materialised rows are already one-per-(recurring_id, month)
-- under the old key, and manual/aggregate rows are already unique on (month, category, label), so
-- both new UNIQUE keys accept the existing rows without dedupe. Idempotent: every DDL step is guarded
-- via information_schema (MySQL 8 has no ADD COLUMN / DROP INDEX IF (NOT) EXISTS) so a mid-file
-- failure re-runs cleanly from the top.

-- 1) manual_key: 1 for manual/aggregate lines (recurring_id IS NULL), NULL for materialised lines.
SET @need_col := (SELECT COUNT(*) = 0 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'opex_line' AND COLUMN_NAME = 'manual_key');
SET @sql := IF(@need_col,
    'ALTER TABLE opex_line ADD COLUMN manual_key TINYINT AS (IF(recurring_id IS NULL, 1, NULL)) VIRTUAL',
    'SELECT 1');
PREPARE s FROM @sql; EXECUTE s; DEALLOCATE PREPARE s;

-- 2) drop the label-based unique key (superseded by the two below).
SET @has_old := (SELECT COUNT(*) FROM information_schema.STATISTICS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'opex_line' AND INDEX_NAME = 'uniq_opex_line');
SET @sql := IF(@has_old > 0, 'ALTER TABLE opex_line DROP INDEX uniq_opex_line', 'SELECT 1');
PREPARE s FROM @sql; EXECUTE s; DEALLOCATE PREPARE s;

-- 3) manual/aggregate dedup: (month, category, label) among manual rows only (manual_key = 1).
SET @has_manual := (SELECT COUNT(*) FROM information_schema.STATISTICS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'opex_line' AND INDEX_NAME = 'uniq_opex_manual');
SET @sql := IF(@has_manual = 0,
    'ALTER TABLE opex_line ADD UNIQUE KEY uniq_opex_manual (month, category, label, manual_key)',
    'SELECT 1');
PREPARE s FROM @sql; EXECUTE s; DEALLOCATE PREPARE s;

-- 4) materialised dedup: one line per template per month, independent of label.
SET @has_rec := (SELECT COUNT(*) FROM information_schema.STATISTICS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'opex_line' AND INDEX_NAME = 'uniq_opex_recurring_month');
SET @sql := IF(@has_rec = 0,
    'ALTER TABLE opex_line ADD UNIQUE KEY uniq_opex_recurring_month (recurring_id, month)',
    'SELECT 1');
PREPARE s FROM @sql; EXECUTE s; DEALLOCATE PREPARE s;

-- +migrate Down

-- restore the label-based key (best-effort; Down is not exercised in prod automigrate).
SET @has_manual := (SELECT COUNT(*) FROM information_schema.STATISTICS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'opex_line' AND INDEX_NAME = 'uniq_opex_manual');
SET @sql := IF(@has_manual > 0, 'ALTER TABLE opex_line DROP INDEX uniq_opex_manual', 'SELECT 1');
PREPARE s FROM @sql; EXECUTE s; DEALLOCATE PREPARE s;

SET @has_rec := (SELECT COUNT(*) FROM information_schema.STATISTICS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'opex_line' AND INDEX_NAME = 'uniq_opex_recurring_month');
SET @sql := IF(@has_rec > 0, 'ALTER TABLE opex_line DROP INDEX uniq_opex_recurring_month', 'SELECT 1');
PREPARE s FROM @sql; EXECUTE s; DEALLOCATE PREPARE s;

SELECT 1;
