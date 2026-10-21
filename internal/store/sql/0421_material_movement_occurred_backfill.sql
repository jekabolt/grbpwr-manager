-- +migrate Up

-- MOVEMENT DATES: every material_stock_movement row carries a business date (occurred_at).
--
-- 0105 left occurred_at NULLable and only receipts/issues that named a date filled it. Write-offs,
-- stock counts, packaging consume and production receipts were stored with NULL, so the Movements
-- journal showed them undated, sorted them above dated rows (it ordered by id) and dropped them from
-- every date window. The binary now dates every insert with the store clock's UTC day. This backfills
-- the legacy NULLs with the UTC day the row was recorded (created_at; the cluster runs in UTC).
--
-- Accounting: the posting worker already used created_at for an undated movement, and a posted
-- journal entry keeps its own occurred_at (movements are posted once, by id checkpoint), so no posted
-- amount or period moves. A not-yet-posted row lands on the same UTC day it would have used anyway.
--
-- idx_msm_occurred serves the journal's ORDER BY occurred_at DESC, id DESC and its date window.
--
-- IDEMPOTENT: the UPDATE touches only NULL rows; the index is added only when absent (guarded,
-- PREPARE/EXECUTE/DEALLOCATE one statement per line, prod runs without multiStatements). The column
-- stays NULLable and no CHECK is added: a retroactive constraint can halt the deploy.

UPDATE material_stock_movement SET occurred_at = DATE(created_at) WHERE occurred_at IS NULL;

SET @msm_need_occ_idx := (SELECT COUNT(*) = 0 FROM information_schema.STATISTICS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'material_stock_movement' AND INDEX_NAME = 'idx_msm_occurred');
SET @msm_occ_sql := IF(@msm_need_occ_idx,
    'ALTER TABLE material_stock_movement ADD INDEX idx_msm_occurred (occurred_at, id)', 'SELECT 1');
PREPARE msm_occ_stmt FROM @msm_occ_sql;
EXECUTE msm_occ_stmt;
DEALLOCATE PREPARE msm_occ_stmt;

-- +migrate Down

-- The backfilled dates stay: they are indistinguishable from dated rows and harmless to an older binary.
SET @msm_has_occ_idx := (SELECT COUNT(*) > 0 FROM information_schema.STATISTICS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'material_stock_movement' AND INDEX_NAME = 'idx_msm_occurred');
SET @msm_occ_down_sql := IF(@msm_has_occ_idx,
    'ALTER TABLE material_stock_movement DROP INDEX idx_msm_occurred', 'SELECT 1');
PREPARE msm_occ_down_stmt FROM @msm_occ_down_sql;
EXECUTE msm_occ_down_stmt;
DEALLOCATE PREPARE msm_occ_down_stmt;
