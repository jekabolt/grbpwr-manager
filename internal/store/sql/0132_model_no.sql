-- +migrate Up

-- SKU redesign task 03: the 5-digit MODEL segment of the SKU (SS26-00021-BLK). A single shared
-- counter (model_no_seq) hands numbers to BOTH styles (tech_card) and standalone products, so their
-- namespaces can never collide: a style has one model_no shared by all its colourway-products; a
-- standalone product has its own. A colourway-linked product gets NO model_no (it uses the style's).
--
-- model_no_seq is an AUTO_INCREMENT allocation table: a new number is minted at runtime with
-- INSERT INTO model_no_seq () VALUES () + LAST_INSERT_ID(). The backfill uses the well-known
-- INSERT..SELECT..ORDER BY trick to allocate sequential numbers in created_at order in one pass.
--
-- Idempotent: table create is guarded with IF NOT EXISTS; guarded ADD COLUMN / ADD UNIQUE via information_schema
-- (multi-line PREPARE/EXECUTE/DEALLOCATE — a single line trips 1064 on the managed DSN, see 0124);
-- every backfill step is gated on model_no IS NULL, so a re-run allocates nothing new.

CREATE TABLE IF NOT EXISTS model_no_seq (
    id         INT PRIMARY KEY AUTO_INCREMENT,
    ref_type   VARCHAR(16) NULL COMMENT 'tech_card|product|NULL(runtime alloc) — provenance only',
    ref_id     INT         NULL,
    created_at TIMESTAMP   NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- tech_card.model_no (UNIQUE; NULL until assigned).
SET @need_tc := (SELECT COUNT(*) = 0 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card' AND COLUMN_NAME = 'model_no');
SET @sql := IF(@need_tc,
    'ALTER TABLE tech_card ADD COLUMN model_no INT NULL',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

SET @need_tc_uq := (SELECT COUNT(*) = 0 FROM information_schema.STATISTICS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card' AND INDEX_NAME = 'uniq_tech_card_model_no');
SET @sql := IF(@need_tc_uq,
    'ALTER TABLE tech_card ADD CONSTRAINT uniq_tech_card_model_no UNIQUE (model_no)',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

-- product.model_no (UNIQUE; NULL for colourway-linked products and until assigned).
SET @need_p := (SELECT COUNT(*) = 0 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'product' AND COLUMN_NAME = 'model_no');
SET @sql := IF(@need_p,
    'ALTER TABLE product ADD COLUMN model_no INT NULL',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

SET @need_p_uq := (SELECT COUNT(*) = 0 FROM information_schema.STATISTICS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'product' AND INDEX_NAME = 'uniq_product_model_no');
SET @sql := IF(@need_p_uq,
    'ALTER TABLE product ADD CONSTRAINT uniq_product_model_no UNIQUE (model_no)',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

-- Backfill 1: allocate a number for every tech_card lacking one, in created_at order. INSERT..SELECT
-- assigns model_no_seq.id sequentially in the ORDER BY order (single-threaded insert).
INSERT INTO model_no_seq (ref_type, ref_id)
    SELECT 'tech_card', tc.id FROM tech_card tc
    WHERE tc.model_no IS NULL
    ORDER BY tc.created_at, tc.id;
UPDATE tech_card tc
    JOIN model_no_seq q ON q.ref_type = 'tech_card' AND q.ref_id = tc.id
    SET tc.model_no = q.id
    WHERE tc.model_no IS NULL;

-- Backfill 2: allocate for every STANDALONE product (not realising any colourway) lacking one.
INSERT INTO model_no_seq (ref_type, ref_id)
    SELECT 'product', p.id FROM product p
    WHERE p.model_no IS NULL
      AND NOT EXISTS (SELECT 1 FROM tech_card_colorway cw WHERE cw.product_id = p.id)
    ORDER BY p.created_at, p.id;
UPDATE product p
    JOIN model_no_seq q ON q.ref_type = 'product' AND q.ref_id = p.id
    SET p.model_no = q.id
    WHERE p.model_no IS NULL;
