-- +migrate Up

-- production-costing Phase 4: auxiliary run planning has NEVER been savable. An auxiliary run's
-- single output line has no product AND no size (the output is a material — a dust bag has no size
-- grade), but 0110 made size_id NOT NULL and the DTO enforced it since the original production-run
-- commit, so the aux plan editor's save (sizeId: 0) has 400'd since the feature shipped
-- (adversarial review, line_key change-set, adjacent finding). Make size_id nullable; the DTO now
-- requires a size exactly when the line has a product (a garment line always has a size; a
-- product-less line may omit it). NULL never occupies a uniq_prl slot — same MySQL semantics the
-- 0230 parking dance relies on — and fk_prl_size tolerates NULL by definition.
SET @need := (SELECT COUNT(*) > 0 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'production_run_line' AND COLUMN_NAME = 'size_id'
      AND IS_NULLABLE = 'NO');
SET @sql := IF(@need,
    'ALTER TABLE production_run_line MODIFY COLUMN size_id INT NULL',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

-- +migrate Down

-- Cannot restore NOT NULL once aux lines carry NULL sizes. Intentional no-op.
SELECT 1;
