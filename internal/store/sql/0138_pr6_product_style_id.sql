-- +migrate Up

-- PR6 phase 1 (style spine): every product IS a colourway, and in the target model every colourway
-- belongs to a style (tech_card). This adds product.style_id and backfills it so the invariant
-- "every product has a style" holds — the additive foundation the later phases (merge colourway,
-- move POM/style fields, rename product->colourway) build on. Nothing is renamed or dropped here, so
-- the app keeps working unchanged (style_id is just a new column).
--
-- Backfill: a linked product takes its existing style (primary_tech_card_id, else its
-- tech_card_colorway card, else its tech_card_product link). A standalone product (27/29 have no card)
-- gets a freshly synthesised minimal tech_card — "одиночка = стиль с 1 цветомоделью" (North Star). The
-- synthetic card uses a deterministic style_number 'AUTO-<id>' so we can correlate it back to the
-- product; the operator can flesh it out later. Only style_number + name are NOT NULL on tech_card
-- (everything else defaults), so a minimal insert is valid.
--
-- Idempotent: guarded ADD COLUMN / FK / MODIFY via information_schema; the backfill UPDATEs are
-- predicated on style_id IS NULL and the synth INSERT on NOT EXISTS(style_number), so a re-run is a
-- no-op (multi-line PREPARE/EXECUTE/DEALLOCATE — a single line trips 1064 on the managed DSN, 0124).

-- 1) Column + FK (nullable during backfill).
SET @need_col := (SELECT COUNT(*) = 0 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'product' AND COLUMN_NAME = 'style_id');
SET @sql := IF(@need_col,
    'ALTER TABLE product ADD COLUMN style_id INT NULL',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

SET @need_fk := (SELECT COUNT(*) = 0 FROM information_schema.TABLE_CONSTRAINTS
    WHERE CONSTRAINT_SCHEMA = DATABASE() AND TABLE_NAME = 'product'
      AND CONSTRAINT_NAME = 'fk_product_style');
SET @sql := IF(@need_fk,
    'ALTER TABLE product ADD CONSTRAINT fk_product_style
        FOREIGN KEY (style_id) REFERENCES tech_card(id) ON DELETE RESTRICT',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

-- 2) Linked products: take the existing style. Prefer the authoritative primary card, then a
--    colourway-published card, then any tech_card_product link.
UPDATE product p
SET p.style_id = COALESCE(
        p.primary_tech_card_id,
        (SELECT c.tech_card_id FROM tech_card_colorway c WHERE c.product_id = p.id ORDER BY c.id LIMIT 1),
        (SELECT MIN(tp.tech_card_id) FROM tech_card_product tp WHERE tp.product_id = p.id)
    )
WHERE p.style_id IS NULL;

-- 3) Standalone products: synthesise a minimal style. Deterministic style_number 'AUTO-<id>' (globally
--    unique -> no (style_number, season) clash). Name from the default-language translation, else any,
--    else 'Product <id>'.
INSERT INTO tech_card (style_number, name, brand, season, collection, target_gender)
SELECT
    CONCAT('AUTO-', p.id),
    COALESCE(
        (SELECT pt.name FROM product_translation pt JOIN language l ON pt.language_id = l.id
         WHERE pt.product_id = p.id AND l.is_default = TRUE LIMIT 1),
        (SELECT pt2.name FROM product_translation pt2 WHERE pt2.product_id = p.id ORDER BY pt2.language_id LIMIT 1),
        CONCAT('Product ', p.id)
    ),
    NULLIF(p.brand, ''),
    NULLIF(p.season, ''),
    NULLIF(p.collection, ''),
    NULLIF(p.target_gender, '')
FROM product p
WHERE p.style_id IS NULL
  AND NOT EXISTS (SELECT 1 FROM tech_card t WHERE t.style_number = CONCAT('AUTO-', p.id));

-- 4) Link the synthesised styles back to their product.
UPDATE product p
JOIN tech_card t ON t.style_number = CONCAT('AUTO-', p.id)
SET p.style_id = t.id
WHERE p.style_id IS NULL;

-- 5) Enforce the invariant: every product now has a style. MODIFY to NOT NULL only if still nullable
--    (idempotent) — if any row were still NULL the ALTER would fail loudly, which is the intended guard.
SET @is_nullable := (SELECT IS_NULLABLE FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'product' AND COLUMN_NAME = 'style_id');
SET @sql := IF(@is_nullable = 'YES',
    'ALTER TABLE product MODIFY COLUMN style_id INT NOT NULL',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

-- 6) Index for style-scoped queries.
SET @need_idx := (SELECT COUNT(*) = 0 FROM information_schema.STATISTICS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'product' AND INDEX_NAME = 'idx_product_style_id');
SET @sql := IF(@need_idx,
    'ALTER TABLE product ADD INDEX idx_product_style_id (style_id)',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;
