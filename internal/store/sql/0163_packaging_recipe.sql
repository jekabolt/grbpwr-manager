-- +migrate Up

-- PLM rework Q3 (01-DOMAIN-MODEL §2.8): packaging configuration per product / style, with a global
-- fallback. `packaging_recipe` supersedes the flat global `packaging_bom` (0116): a shipped order's
-- packaging is resolved most-specific-first — a matching `product`-scope recipe wins over a `style`-
-- scope one, which wins over the `global` fallback (Q3 "product → style → global, first match wins").
-- Columns mirror packaging_bom: `qty_per_order` once per shipment (a box), `qty_per_item` × the
-- order's unit count (a dust bag). Exactly one of tech_card_id / product_id is set per the scope.
--
-- This wave is additive (M1): the table is created and the existing global packaging_bom is copied
-- into scope='global' so resolution keeps producing today's behaviour for orders that have no
-- product/style override. packaging_bom itself is dropped only in a later guarded destructive wave
-- (M3), after every consumer reads packaging_recipe.
--
-- Idempotent: CREATE TABLE IF NOT EXISTS (named FK/CHECK inline) + a NOT EXISTS-guarded backfill, so
-- a mid-file DDL failure re-runs cleanly from the top.

CREATE TABLE IF NOT EXISTS packaging_recipe (
    id INT AUTO_INCREMENT PRIMARY KEY,
    scope VARCHAR(8) NOT NULL,                        -- global | style | product
    tech_card_id INT NULL,                            -- set iff scope='style'
    product_id INT NULL,                              -- set iff scope='product'
    material_id INT NOT NULL,
    qty_per_order DECIMAL(12,3) NOT NULL DEFAULT 0,   -- consumed once per shipped order (a box)
    qty_per_item DECIMAL(12,3) NOT NULL DEFAULT 0,    -- consumed per unit in the order (a dust bag)
    active BOOLEAN NOT NULL DEFAULT TRUE,
    lock_version INT NOT NULL DEFAULT 0,              -- optimistic lock (S25 / §2.12)
    created_by VARCHAR(255) NOT NULL DEFAULT '',      -- audit (§2.11): server-stamped username, no FK
    updated_by VARCHAR(255) NOT NULL DEFAULT '',
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    CONSTRAINT fk_packaging_recipe_material FOREIGN KEY (material_id) REFERENCES material(id) ON DELETE CASCADE,
    CONSTRAINT fk_packaging_recipe_tech_card FOREIGN KEY (tech_card_id) REFERENCES tech_card(id) ON DELETE CASCADE,
    CONSTRAINT fk_packaging_recipe_product FOREIGN KEY (product_id) REFERENCES product(id) ON DELETE CASCADE,
    -- scope ↔ key consistency: exactly one target column is set per scope (also restricts scope to
    -- the three legal values — any other value satisfies no branch).
    CONSTRAINT chk_packaging_recipe_scope CHECK (
        (scope = 'global'  AND tech_card_id IS NULL     AND product_id IS NULL) OR
        (scope = 'style'   AND tech_card_id IS NOT NULL AND product_id IS NULL) OR
        (scope = 'product' AND product_id   IS NOT NULL AND tech_card_id IS NULL)
    ),
    CONSTRAINT chk_packaging_recipe_qty CHECK (qty_per_order >= 0 AND qty_per_item >= 0),
    INDEX idx_packaging_recipe_product (product_id),
    INDEX idx_packaging_recipe_style (tech_card_id),
    INDEX idx_packaging_recipe_material (material_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- Backfill the existing flat global recipe into scope='global'. NOT EXISTS keeps it idempotent — a
-- re-run (or a run after packaging_recipe already holds a global row for that material) is a no-op,
-- and it never clobbers a global row an operator has since edited through the new CRUD.
INSERT INTO packaging_recipe (scope, tech_card_id, product_id, material_id, qty_per_order, qty_per_item, active)
SELECT 'global', NULL, NULL, pb.material_id, pb.qty_per_order, pb.qty_per_item, pb.active
FROM packaging_bom pb
WHERE NOT EXISTS (
    SELECT 1 FROM packaging_recipe pr
    WHERE pr.scope = 'global' AND pr.material_id = pb.material_id
);

-- +migrate Down

DROP TABLE IF EXISTS packaging_recipe;
SELECT 1;
