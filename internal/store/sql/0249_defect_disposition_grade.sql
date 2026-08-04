-- +migrate Up

-- production-costing Phase 7 (plan 09): defect disposition + seconds-as-B-grade.
--
-- 1. production_run_receipt_line.defect_disposition — where this line's defect units went:
--    'scrap' (recorded fact, no stock; cost resolved by the posting rule P1: normal loss is
--    absorbed by the good units, the abnormal excess is written off Dr 5040 / Cr 1120) or
--    'seconds' (units land in the product's B-grade variant stock at zero cost v1). Every
--    pre-Phase-7 defect was an undispositioned recorded fact — 'scrap' is the exact backfill.
SET @have_col := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'production_run_receipt_line' AND COLUMN_NAME = 'defect_disposition');
SET @sql := IF(@have_col = 0,
    'ALTER TABLE production_run_receipt_line ADD COLUMN defect_disposition VARCHAR(8) NOT NULL DEFAULT ''scrap'' COMMENT ''where defect units went (Phase 7)''',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

SET @have := (SELECT COUNT(*) FROM information_schema.TABLE_CONSTRAINTS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'production_run_receipt_line' AND CONSTRAINT_NAME = 'chk_prrl_disposition');
SET @sql := IF(@have = 0,
    'ALTER TABLE production_run_receipt_line ADD CONSTRAINT chk_prrl_disposition CHECK (defect_disposition IN (''scrap'', ''seconds''))',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

-- 2. product_size.grade — the variant's quality grade. 'A' is every pre-Phase-7 row; 'B' rows are
--    factory seconds of the SAME (product, size), created only by a receipt line dispositioned
--    'seconds'. NOT NULL DEFAULT 'A' (not NULL-means-A) because grade joins the variant unique key
--    and MySQL treats NULLs in a unique index as distinct — NULLable grade would stop enforcing
--    one-A-row-per-size. B variants are NOT sellable in v1 (their discount price is an open owner
--    decision): every storefront/order/metrics read pins grade = 'A'.
SET @have_col := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'product_size' AND COLUMN_NAME = 'grade');
SET @sql := IF(@have_col = 0,
    'ALTER TABLE product_size ADD COLUMN grade CHAR(1) NOT NULL DEFAULT ''A'' COMMENT ''variant quality grade; B = factory seconds (Phase 7)''',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

SET @have := (SELECT COUNT(*) FROM information_schema.TABLE_CONSTRAINTS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'product_size' AND CONSTRAINT_NAME = 'chk_product_size_grade');
SET @sql := IF(@have = 0,
    'ALTER TABLE product_size ADD CONSTRAINT chk_product_size_grade CHECK (grade IN (''A'', ''B''))',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

-- The variant identity becomes (product, size, grade). ADD the named replacement key FIRST so the
-- product_id FK always has a serving index, THEN drop 0001's auto-named UNIQUE(product_id, size_id)
-- — located dynamically by its exact column composition (its auto name is positional history).
SET @have := (SELECT COUNT(*) FROM information_schema.STATISTICS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'product_size' AND INDEX_NAME = 'uq_product_size_variant');
SET @sql := IF(@have = 0,
    'ALTER TABLE product_size ADD CONSTRAINT uq_product_size_variant UNIQUE (product_id, size_id, grade)',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

SET @old_idx := (SELECT INDEX_NAME FROM information_schema.STATISTICS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'product_size' AND NON_UNIQUE = 0
      AND INDEX_NAME <> 'PRIMARY' AND INDEX_NAME <> 'uq_product_size_variant'
      AND INDEX_NAME <> 'uniq_product_size_sku'
    GROUP BY INDEX_NAME
    HAVING COUNT(*) = 2
       AND SUM(SEQ_IN_INDEX = 1 AND COLUMN_NAME = 'product_id') = 1
       AND SUM(SEQ_IN_INDEX = 2 AND COLUMN_NAME = 'size_id') = 1
    LIMIT 1);
SET @sql := IF(@old_idx IS NOT NULL,
    CONCAT('ALTER TABLE product_size DROP INDEX ', @old_idx), 'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

-- FAIL CLOSED: if any 2-column UNIQUE(product_id, size_id) survived (a shape this locator did not
-- anticipate), the B-grade upsert's ON DUPLICATE KEY would silently collide with the A row and
-- overwrite its quantity — halt the migration instead of shipping that time bomb.
SET @leftover := (SELECT COUNT(*) FROM (
    SELECT INDEX_NAME FROM information_schema.STATISTICS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'product_size' AND NON_UNIQUE = 0
      AND INDEX_NAME <> 'PRIMARY' AND INDEX_NAME <> 'uq_product_size_variant'
      AND INDEX_NAME <> 'uniq_product_size_sku'
    GROUP BY INDEX_NAME
    HAVING COUNT(*) = 2
       AND SUM(SEQ_IN_INDEX = 1 AND COLUMN_NAME = 'product_id') = 1
       AND SUM(SEQ_IN_INDEX = 2 AND COLUMN_NAME = 'size_id') = 1) leftover);
SET @sql := IF(@leftover = 0, 'SELECT 1',
    'SIGNAL SQLSTATE ''45000'' SET MESSAGE_TEXT = ''0249 post-condition failed, a 2-column unique index on product_size survived the variant-key rebuild''');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

-- 3. product_stock_change_history.grade — which grade row a journalled movement touched. The A/B
--    stocks are separate quantities, so quantity_before/after are per-grade facts; without the
--    marker a B movement would corrupt the (product, size) journal stream's continuity.
SET @have_col := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'product_stock_change_history' AND COLUMN_NAME = 'grade');
SET @sql := IF(@have_col = 0,
    'ALTER TABLE product_stock_change_history ADD COLUMN grade CHAR(1) NOT NULL DEFAULT ''A'' COMMENT ''which grade stock the movement touched (Phase 7)''',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

-- +migrate Down

-- grade columns and the widened variant key are load-bearing once any B row exists; narrowing back
-- would fail on that data. Intentional no-op.
SELECT 1;
