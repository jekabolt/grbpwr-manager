-- +migrate Up

-- The card's packaging items become the one source of what an order takes and of what packaging costs.
-- The recipes move onto the cards as items and go dark; nothing reads packaging_recipe after this.
--
-- Nothing here assumes which rows exist. On 2026-11-04 beta held six product rows on two cards and one
-- global row, prod none, but the new packaging screen reaches prod before this file, so prod may hold
-- items and recipes by the time it runs. The global recipe is gone by decision: no card takes it over.
--
-- WHAT MOVES IS WHAT EACH COLOUR TOOK. A recipe resolved most specific first and the first scope with an
-- active row won whole: a colour with a product recipe never took the style recipe. So each colour's own
-- recipe is worked out first, and equal results group into items: one per (card, material, per order,
-- quantity); qty_per_item makes a per-garment item, qty_per_order a per-order one, both make two. An item
-- names the colours that took it, and one taken by every colour of its card names none. A style recipe on
-- a card with no colours yet moves unlimited. A card already holding that material with that per-order
-- flag keeps its own item.
--
-- A FRACTIONAL QUANTITY STOPS THE FILE. qty_per_garment is whole; rounding here would change what orders
-- take. Beta has none. If one appears, the first insert below breaks chk_tc_packaging_item_qty on purpose.
--
-- THE WRITES COME BEFORE THE TABLES. The first CREATE commits them, so a stop above leaves nothing moved.
--
-- THERE IS NO WAY BACK DOWN. Nothing records which recipes were active or which items were moved, and the
-- exact needs, the carry and the shortages hold stock that exists nowhere else. Down refuses; going back
-- means restoring the database. A rolled back binary without that reads the dark recipes as empty: new
-- orders take no packaging, open claims still ship as placed.

INSERT INTO tech_card_packaging_item (tech_card_id, item_key, qty_per_garment)
SELECT COALESCE(p.style_id, pr.tech_card_id), 'other', 0
FROM packaging_recipe pr LEFT JOIN product p ON p.id = pr.product_id
WHERE pr.active = TRUE AND pr.scope <> 'global'
  AND (pr.qty_per_item <> FLOOR(pr.qty_per_item) OR pr.qty_per_order <> FLOOR(pr.qty_per_order));

SET @items_before := (SELECT COALESCE(MAX(id), 0) FROM tech_card_packaging_item);

INSERT INTO tech_card_packaging_item (tech_card_id, item_key, qty_per_garment, material_id, per_order, display_order)
SELECT r.card_id,
       CASE WHEN LOWER(m.name) LIKE '%dust%' THEN 'dust_bag'
            WHEN LOWER(m.name) LIKE '%box%' THEN 'box'
            WHEN LOWER(m.name) LIKE '%insert%' THEN 'insert_card'
            WHEN LOWER(m.name) LIKE '%hanger%' THEN 'hanger'
            ELSE 'other' END,
       r.qty, r.material_id, r.per_order, 100
FROM (
    SELECT DISTINCT e.card_id, e.material_id, e.per_order, e.qty FROM (
    SELECT p.style_id AS card_id, p.id AS product_id, pr.material_id, f.per_order,
           CAST(IF(f.per_order = 1, pr.qty_per_order, pr.qty_per_item) AS SIGNED) AS qty
    FROM packaging_recipe pr
    JOIN (SELECT 0 AS per_order UNION ALL SELECT 1) f
    JOIN product p ON p.style_id IS NOT NULL
     AND ((pr.scope = 'product' AND p.id = pr.product_id)
       OR (pr.scope = 'style' AND p.style_id = pr.tech_card_id
           AND NOT EXISTS (SELECT 1 FROM packaging_recipe o
                           WHERE o.active = TRUE AND o.scope = 'product' AND o.product_id = p.id)))
    WHERE pr.active = TRUE AND IF(f.per_order = 1, pr.qty_per_order, pr.qty_per_item) > 0
    ) e
    UNION
    SELECT pr.tech_card_id, pr.material_id, f.per_order,
           CAST(IF(f.per_order = 1, pr.qty_per_order, pr.qty_per_item) AS SIGNED)
    FROM packaging_recipe pr
    JOIN (SELECT 0 AS per_order UNION ALL SELECT 1) f
    WHERE pr.active = TRUE AND pr.scope = 'style' AND IF(f.per_order = 1, pr.qty_per_order, pr.qty_per_item) > 0
      AND NOT EXISTS (SELECT 1 FROM product n WHERE n.style_id = pr.tech_card_id)
) r
JOIN material m ON m.id = r.material_id
WHERE r.card_id IS NOT NULL
  AND NOT EXISTS (SELECT 1 FROM tech_card_packaging_item x
                  WHERE x.tech_card_id = r.card_id AND x.material_id = r.material_id
                    AND COALESCE(x.per_order, 0) = r.per_order);

INSERT INTO tech_card_packaging_item_colorway (item_id, colorway_id)
SELECT DISTINCT i.id, e.product_id
FROM (
    SELECT p.style_id AS card_id, p.id AS product_id, pr.material_id, f.per_order,
           CAST(IF(f.per_order = 1, pr.qty_per_order, pr.qty_per_item) AS SIGNED) AS qty
    FROM packaging_recipe pr
    JOIN (SELECT 0 AS per_order UNION ALL SELECT 1) f
    JOIN product p ON p.style_id IS NOT NULL
     AND ((pr.scope = 'product' AND p.id = pr.product_id)
       OR (pr.scope = 'style' AND p.style_id = pr.tech_card_id
           AND NOT EXISTS (SELECT 1 FROM packaging_recipe o
                           WHERE o.active = TRUE AND o.scope = 'product' AND o.product_id = p.id)))
    WHERE pr.active = TRUE AND IF(f.per_order = 1, pr.qty_per_order, pr.qty_per_item) > 0
) e
JOIN tech_card_packaging_item i ON i.tech_card_id = e.card_id AND i.material_id = e.material_id
 AND COALESCE(i.per_order, 0) = e.per_order AND i.qty_per_garment = e.qty
WHERE i.id > @items_before;

DELETE c FROM tech_card_packaging_item_colorway c
JOIN tech_card_packaging_item i ON i.id = c.item_id
WHERE i.id > @items_before
  AND (SELECT COUNT(*) FROM (SELECT * FROM tech_card_packaging_item_colorway) n WHERE n.item_id = i.id)
    = (SELECT COUNT(*) FROM product p WHERE p.style_id = i.tech_card_id);

UPDATE packaging_recipe SET active = FALSE WHERE active = TRUE;

CREATE TABLE IF NOT EXISTS order_packaging_shortage (
    order_id INT NOT NULL,
    material_id INT NOT NULL,
    qty DECIMAL(12,3) NOT NULL,
    PRIMARY KEY (order_id, material_id),
    CONSTRAINT fk_order_packaging_shortage_order FOREIGN KEY (order_id) REFERENCES customer_order(id) ON DELETE CASCADE,
    CONSTRAINT fk_order_packaging_shortage_material FOREIGN KEY (material_id) REFERENCES material(id) ON DELETE CASCADE
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4 COMMENT 'Packaging a shipped order could not take from stock yet; a re-ship tries again (0450)';

CREATE TABLE IF NOT EXISTS packaging_fraction_carry (
    material_id INT NOT NULL PRIMARY KEY,
    carry DECIMAL(20,12) NOT NULL DEFAULT 0,
    CONSTRAINT fk_packaging_fraction_carry_material FOREIGN KEY (material_id) REFERENCES material(id) ON DELETE CASCADE
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4 COMMENT 'What rounding a box share to three places left over, per material (0450)';

CREATE TABLE IF NOT EXISTS order_packaging_need (
    order_id INT NOT NULL,
    material_id INT NOT NULL,
    qty DECIMAL(20,12) NOT NULL,
    closed BOOLEAN NOT NULL DEFAULT FALSE,
    PRIMARY KEY (order_id, material_id),
    CONSTRAINT fk_order_packaging_need_order FOREIGN KEY (order_id) REFERENCES customer_order(id) ON DELETE CASCADE,
    CONSTRAINT fk_order_packaging_need_material FOREIGN KEY (material_id) REFERENCES material(id) ON DELETE CASCADE
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4 COMMENT 'Exactly what an order needs of a material, unrounded; shipping or cancelling closes it (0450)';

-- +migrate Down

-- Refuses, see the top of this file: the name of the missing table is the message.
SELECT 1 FROM migration_0450_has_no_down_restore_the_database;
