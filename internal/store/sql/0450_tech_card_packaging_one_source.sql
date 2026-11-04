-- +migrate Up

-- The card's packaging items become the one source of what an order takes and of what packaging costs.
-- The recipes move onto the cards as items and go dark; nothing reads packaging_recipe after this.
--
-- Nothing here assumes which rows exist. On 2026-11-04 beta held six product rows on two cards and one
-- global row, prod none, but the new packaging screen reaches prod before this file, so prod may hold
-- items and recipes by the time it runs. The global recipe is gone by decision: no card takes it over.
--
-- WHAT MOVES. One item per (card, material, per order, quantity); qty_per_item makes a per-garment item,
-- qty_per_order a per-order one, both make two. Only colourways named by product rows get a scope row,
-- and an item left covering every colourway of its card carries none. A style row covers all colours.
-- A card already holding that material with that per-order flag keeps its own item.
--
-- A FRACTIONAL QUANTITY STOPS THE FILE. qty_per_garment is whole; rounding here would change what orders
-- take. Beta has none. If one appears, the first insert below breaks chk_tc_packaging_item_qty on purpose.
--
-- DOWN LEAVES THE DATA. Nothing records which rows were active before, and turning them all on could
-- raise a row an operator had switched off. An older binary reads the dark recipes as empty and writes
-- off no packaging on new orders; open claims still ship as placed.

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
    SELECT DISTINCT COALESCE(p.style_id, pr.tech_card_id) AS card_id, pr.material_id, 0 AS per_order, CAST(pr.qty_per_item AS SIGNED) AS qty
    FROM packaging_recipe pr LEFT JOIN product p ON p.id = pr.product_id
    WHERE pr.active = TRUE AND pr.scope <> 'global' AND pr.qty_per_item > 0
    UNION
    SELECT DISTINCT COALESCE(p.style_id, pr.tech_card_id), pr.material_id, 1, CAST(pr.qty_per_order AS SIGNED)
    FROM packaging_recipe pr LEFT JOIN product p ON p.id = pr.product_id
    WHERE pr.active = TRUE AND pr.scope <> 'global' AND pr.qty_per_order > 0
) r
JOIN material m ON m.id = r.material_id
WHERE r.card_id IS NOT NULL
  AND NOT EXISTS (SELECT 1 FROM tech_card_packaging_item e
                  WHERE e.tech_card_id = r.card_id AND e.material_id = r.material_id
                    AND COALESCE(e.per_order, 0) = r.per_order);

INSERT INTO tech_card_packaging_item_colorway (item_id, colorway_id)
SELECT DISTINCT i.id, pr.product_id
FROM packaging_recipe pr
JOIN product p ON p.id = pr.product_id
JOIN tech_card_packaging_item i ON i.tech_card_id = p.style_id AND i.material_id = pr.material_id
WHERE i.id > @items_before AND pr.active = TRUE AND pr.scope = 'product'
  AND ((COALESCE(i.per_order, 0) = 0 AND pr.qty_per_item = i.qty_per_garment)
    OR (COALESCE(i.per_order, 0) = 1 AND pr.qty_per_order = i.qty_per_garment));

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

CREATE TABLE IF NOT EXISTS order_packaging_settled (
    order_id INT NOT NULL PRIMARY KEY,
    CONSTRAINT fk_order_packaging_settled_order FOREIGN KEY (order_id) REFERENCES customer_order(id) ON DELETE CASCADE
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4 COMMENT 'Orders whose whole packaging went into the fraction carry: no claim, nothing more to resolve (0450)';

-- +migrate Down

DROP TABLE IF EXISTS order_packaging_settled;

DROP TABLE IF EXISTS packaging_fraction_carry;
DROP TABLE IF EXISTS order_packaging_shortage;
