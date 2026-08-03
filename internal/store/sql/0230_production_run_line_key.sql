-- +migrate Up
-- Production-costing pre-PR (adversarial review B3): give a production run's plan lines a STABLE
-- wire identity, the same way 0159 did for BOM lines and 0168 for cut-pieces.
--
-- production_run_line has an AUTO_INCREMENT PK, but UpdateProductionRun FULL-REPLACES the grid on
-- every save (DELETE FROM production_run_line WHERE run_id = ... + reinsert, productionrun.go), so
-- the id is destroyed and reminted whenever anything on the run changes — including a header-only
-- edit. That makes a foreign key FROM a future receipt line TO a run line impossible: the receipt
-- would either dangle on a vanished id or, with ON DELETE CASCADE, be wiped by the next edit of the
-- run, taking the received history (and its accounting) with it. Receipt tables land in Phase 5;
-- the line has to be durable BEFORE anything can point at it.
--
-- line_key is the identity, and it deliberately is NOT (product_id, size_id). Those two are editable
-- ATTRIBUTES of a line — fixing a size or attaching the colour-model that was still unpublished at
-- planning time is a normal edit — and it is exactly such an edit that a natural-key match could not
-- express without reminting the row. The existing uniq_prl (run_id, product_id, size_id) stays as
-- what it always was: the business invariant (one line per colour-model × size), not the identity.
--
-- Backfill, exactly as 0159/0168: legacy rows get a deterministic 'LEGACY' + zero-padded id key
-- rather than being left NULL. Left NULL, the first save after this deploy would see keyless rows,
-- treat every incoming line as new and delete every stored one — i.e. one last full id churn, on
-- live runs, which is the very thing this migration exists to stop. With the backfill the key
-- round-trips through the client on the very next read/save and ids survive from day one. The
-- column still allows NULL (like 0159/0168) so a partially-applied ALTER is re-runnable and the
-- UNIQUE index tolerates a row created between the ADD COLUMN and the backfill.
--
-- Crash-idempotent: every DDL is guarded on information_schema (MySQL 8 has no ADD COLUMN IF NOT
-- EXISTS) and the backfill is re-runnable (WHERE ... IS NULL OR = '').

SET @need := (SELECT COUNT(*) = 0 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'production_run_line' AND COLUMN_NAME = 'line_key');
SET @sql := IF(@need,
    'ALTER TABLE production_run_line ADD COLUMN line_key CHAR(26) NULL COMMENT ''stable client-generated line identity; the keyed upsert-diff matches on it''',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

-- Deterministic, unique 26-char key for legacy rows (real 26-char keys come from clients on new
-- lines). 'LEGACY' + zero-padded id = 26 chars; id is unique so the key is unique run-wide.
UPDATE production_run_line SET line_key = CONCAT('LEGACY', LPAD(id, 20, '0'))
    WHERE line_key IS NULL OR line_key = '';

-- UNIQUE (run_id, line_key): the upsert-diff matches on it, so the invariant must hold now.
SET @need := (SELECT COUNT(*) = 0 FROM information_schema.STATISTICS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'production_run_line' AND INDEX_NAME = 'uq_production_run_line_key');
SET @sql := IF(@need,
    'ALTER TABLE production_run_line ADD CONSTRAINT uq_production_run_line_key UNIQUE (run_id, line_key)',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

-- +migrate Down
SET @need := (SELECT COUNT(*) = 1 FROM information_schema.STATISTICS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'production_run_line' AND INDEX_NAME = 'uq_production_run_line_key'
      AND SEQ_IN_INDEX = 1);
SET @sql := IF(@need, 'ALTER TABLE production_run_line DROP INDEX uq_production_run_line_key', 'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

SET @need := (SELECT COUNT(*) = 1 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'production_run_line' AND COLUMN_NAME = 'line_key');
SET @sql := IF(@need, 'ALTER TABLE production_run_line DROP COLUMN line_key', 'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;
