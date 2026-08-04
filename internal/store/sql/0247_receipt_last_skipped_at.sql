-- +migrate Up

-- production-costing Phase 5 (adversarial #2): last_skipped_at marks the worker's latest clean
-- "nothing to post" rebuild of a pending receipt. Phase 5 makes such empties potentially PERMANENT
-- (a defect-only partial after the manual delta was already capitalised), and without this stamp
-- they head the posting scan's oldest-first order every tick — once batch-size of them accumulate,
-- no new receipt ever posts — and inflate the stuck-pending gauge into a WARN that never clears.
-- The scan orders never-skipped receipts first; the gauge ignores receipts whose stamp is fresh.
-- The receipt itself STAYS pending, so a transient empty (costs arrive later) still self-heals.

SET @have_col := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'production_run_receipt' AND COLUMN_NAME = 'last_skipped_at');
SET @sql := IF(@have_col = 0,
    'ALTER TABLE production_run_receipt ADD COLUMN last_skipped_at DATETIME NULL COMMENT ''latest clean nothing-to-post rebuild by the posting worker (Phase 5)''',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

-- +migrate Down

SET @have_col := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'production_run_receipt' AND COLUMN_NAME = 'last_skipped_at');
SET @sql := IF(@have_col > 0, 'ALTER TABLE production_run_receipt DROP COLUMN last_skipped_at', 'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;
