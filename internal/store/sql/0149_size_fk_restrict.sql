-- +migrate Up

-- Problem 018: deleting a dictionary size must never cascade-destroy commerce data. The original
-- schema declared size_id foreign keys ON DELETE CASCADE on product_size (0001), order_item (0001) and
-- the style size-chart tech_card_size_measurement (0141) — so removing a size would silently wipe
-- variants, immutable order-history lines and the style chart. Stable SKUs and immutable order history
-- are incompatible with that. Flip these three to ON DELETE RESTRICT: a size that any product, order
-- or style chart references can no longer be physically deleted (it must be archived instead). Because
-- an in-use size always has a product_size row, RESTRICT here also transitively blocks the delete
-- before any other size cascade (waitlist/stock-history) could fire.
--
-- The two 0001 foreign keys are auto-named (product_size_ibfk_N / order_item_ibfk_N) and the positional
-- suffix drifts across schema history, so the old name is discovered from information_schema at run
-- time rather than hard-coded. Idempotent: each block only acts while the FK is still CASCADE, and the
-- DROP+ADD is a single atomic ALTER (no half-applied state on re-run). 0001/0141 are not edited.

-- product_size.size_id: CASCADE -> RESTRICT (named fk_product_size_size).
SET @old_fk := (SELECT CONSTRAINT_NAME FROM information_schema.KEY_COLUMN_USAGE
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'product_size'
      AND COLUMN_NAME = 'size_id' AND REFERENCED_TABLE_NAME = 'size' LIMIT 1);
SET @is_cascade := (SELECT COUNT(*) FROM information_schema.REFERENTIAL_CONSTRAINTS
    WHERE CONSTRAINT_SCHEMA = DATABASE() AND TABLE_NAME = 'product_size'
      AND CONSTRAINT_NAME = @old_fk AND DELETE_RULE = 'CASCADE');
SET @sql := IF(@old_fk IS NOT NULL AND @is_cascade = 1,
    CONCAT('ALTER TABLE product_size DROP FOREIGN KEY ', @old_fk,
           ', ADD CONSTRAINT fk_product_size_size FOREIGN KEY (size_id) REFERENCES size(id) ON DELETE RESTRICT'),
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

-- order_item.size_id: CASCADE -> RESTRICT (named fk_order_item_size).
SET @old_fk := (SELECT CONSTRAINT_NAME FROM information_schema.KEY_COLUMN_USAGE
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'order_item'
      AND COLUMN_NAME = 'size_id' AND REFERENCED_TABLE_NAME = 'size' LIMIT 1);
SET @is_cascade := (SELECT COUNT(*) FROM information_schema.REFERENTIAL_CONSTRAINTS
    WHERE CONSTRAINT_SCHEMA = DATABASE() AND TABLE_NAME = 'order_item'
      AND CONSTRAINT_NAME = @old_fk AND DELETE_RULE = 'CASCADE');
SET @sql := IF(@old_fk IS NOT NULL AND @is_cascade = 1,
    CONCAT('ALTER TABLE order_item DROP FOREIGN KEY ', @old_fk,
           ', ADD CONSTRAINT fk_order_item_size FOREIGN KEY (size_id) REFERENCES size(id) ON DELETE RESTRICT'),
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

-- tech_card_size_measurement.size_id: CASCADE -> RESTRICT. The new FK gets a distinct name
-- (fk_tcsm_size_restrict) because MySQL forbids dropping and re-adding the same FK name in one ALTER.
SET @old_fk := (SELECT CONSTRAINT_NAME FROM information_schema.KEY_COLUMN_USAGE
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_size_measurement'
      AND COLUMN_NAME = 'size_id' AND REFERENCED_TABLE_NAME = 'size' LIMIT 1);
SET @is_cascade := (SELECT COUNT(*) FROM information_schema.REFERENTIAL_CONSTRAINTS
    WHERE CONSTRAINT_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_size_measurement'
      AND CONSTRAINT_NAME = @old_fk AND DELETE_RULE = 'CASCADE');
SET @sql := IF(@old_fk IS NOT NULL AND @is_cascade = 1,
    CONCAT('ALTER TABLE tech_card_size_measurement DROP FOREIGN KEY ', @old_fk,
           ', ADD CONSTRAINT fk_tcsm_size_restrict FOREIGN KEY (size_id) REFERENCES size(id) ON DELETE RESTRICT'),
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;
