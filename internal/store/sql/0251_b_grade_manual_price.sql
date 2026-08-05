-- +migrate Up

-- B-grade manual pricing (owner decision 2026-08-04: seconds are priced by hand, not by a fixed
-- percentage). Two pieces:
--
--   1) product_size_price — a per-VARIANT catalogue price, same (currency, price) shape as the
--      product-level product_price. v1 writes it only for grade='B' rows (the A price stays
--      product-level); a B variant with no price row in the order currency remains unsellable
--      (fail-closed), which preserves 0249's "B is not sellable until priced" invariant as the
--      default state rather than a hard-coded pin.
--
--   2) order_item.grade — the sale-time grade snapshot. Metrics segment unit-based views
--      (sell-through must not count a B unit against A stock) without joining product_size,
--      and a refund of a sold B line restocks the B row, not the A row. Backfill is the
--      column DEFAULT 'A': before this migration only grade='A' variants were sellable, so
--      every existing line is an A fact.
--
-- Idempotent: guarded via information_schema (multi-line PREPARE/EXECUTE/DEALLOCATE — a
-- single-line trio trips 1064 on the managed DSN, see 0124); constraints carry explicit names.

CREATE TABLE IF NOT EXISTS product_size_price (
    id INT PRIMARY KEY AUTO_INCREMENT,
    product_size_id INT NOT NULL,
    currency VARCHAR(3) NOT NULL COMMENT 'ISO 4217 currency code, matches product_price.currency',
    price DECIMAL(10, 2) NOT NULL,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    UNIQUE KEY uq_product_size_price (product_size_id, currency),
    CONSTRAINT fk_product_size_price_variant FOREIGN KEY (product_size_id) REFERENCES product_size(id) ON DELETE CASCADE,
    CONSTRAINT chk_product_size_price_positive CHECK (price > 0)
);

SET @need_col := (SELECT COUNT(*) = 0 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'order_item' AND COLUMN_NAME = 'grade');
SET @sql := IF(@need_col,
    'ALTER TABLE order_item ADD COLUMN grade CHAR(1) NOT NULL DEFAULT ''A'' COMMENT ''product_size.grade of the sold variant at sale time''',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

SET @need_chk := (SELECT COUNT(*) = 0 FROM information_schema.TABLE_CONSTRAINTS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'order_item'
      AND CONSTRAINT_NAME = 'chk_order_item_grade');
SET @sql := IF(@need_chk,
    'ALTER TABLE order_item ADD CONSTRAINT chk_order_item_grade CHECK (grade IN (''A'', ''B''))',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

-- +migrate Down

-- Forward-only (matches 0249): dropping the grade snapshot or the price table would orphan
-- sold-B history. No-op by design.
SELECT 1;
