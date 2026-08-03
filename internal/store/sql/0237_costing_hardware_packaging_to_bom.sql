-- +migrate Up

-- production-costing Phase 2 (plan 01 §1.1): the manual hardware_cost / packaging_cost scalars
-- leave the costing contract — both duplicated first-class BOM sections priced per colourway
-- through the recipe, and a flat per-garment scalar cannot express a colourway that swaps a metal
-- zip for a nylon one. DRAFT cards' scalars migrate into synthetic BOM lines (one per article,
-- usage consumption 1 on every colourway), so no draft card's displayed unit cost changes; every
-- population that CANNOT be migrated mechanically lands in tech_card_costing_migration_exception
-- for the owner's manual review:
--   double_counted  — scalar AND a WIRED authored BOM row in the same section (priced AND carried
--                     by at least one colourway usage — only such a row actually contributes money,
--                     so only then was the unit cost double-counting; scalar is cleared, the BOM
--                     row wins). An authored row that prices nothing (no usage, or no unit_price)
--                     does NOT count: the scalar was the section's only money, so it migrates into
--                     a synthetic row beside the descriptive authored one.
--   zero_colorways  — scalar but no colourways to hang a usage on (post-R1 a colourway IS a
--                     product with style_id = the card, and usage.colorway_id references
--                     product(id)); the BOM row would price nothing, scalar kept in the retained
--                     column, invisible to the app;
--   not_draft       — review S2: a released card's live BOM must not drift from its release
--                     snapshot, so non-draft cards are never touched by SQL (scalar kept, manual
--                     re-release migrates it).
-- The hardware_cost / packaging_cost COLUMNS are deliberately RETAINED: they hold the values the
-- exception report points at. The application stops reading and writing them from this deploy.
-- Residual risk, accepted: rows are recognised as OURS by the sentinel name ('… (migrated from
-- costing)'), not by provenance — an operator row carrying that exact name would confuse the
-- classification. Verified zero such rows on beta and prod before shipping.

CREATE TABLE IF NOT EXISTS tech_card_costing_migration_exception (
    id INT AUTO_INCREMENT PRIMARY KEY,
    tech_card_id INT NOT NULL,
    article VARCHAR(16) NOT NULL,
    kind VARCHAR(32) NOT NULL,
    amount DECIMAL(12, 2) NOT NULL,
    currency VARCHAR(4) NULL,
    approval_state VARCHAR(32) NOT NULL,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT fk_tccme_card FOREIGN KEY (tech_card_id) REFERENCES tech_card(id) ON DELETE CASCADE,
    CONSTRAINT chk_tccme_article CHECK (article REGEXP '^(hardware|packaging)$'),
    CONSTRAINT chk_tccme_kind CHECK (kind REGEXP '^(double_counted|zero_colorways|not_draft)$'),
    CONSTRAINT uq_tccme UNIQUE (tech_card_id, article, kind)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

-- --- exceptions first (they read the scalars the happy path is about to clear) ---

INSERT IGNORE INTO tech_card_costing_migration_exception (tech_card_id, article, kind, amount, currency, approval_state)
SELECT tc.id, 'hardware', 'not_draft', c.hardware_cost, NULLIF(c.currency, ''), tc.approval_state
FROM tech_card tc JOIN tech_card_costing c ON c.tech_card_id = tc.id
WHERE COALESCE(c.hardware_cost, 0) <> 0 AND tc.approval_state <> 'draft';

INSERT IGNORE INTO tech_card_costing_migration_exception (tech_card_id, article, kind, amount, currency, approval_state)
SELECT tc.id, 'packaging', 'not_draft', c.packaging_cost, NULLIF(c.currency, ''), tc.approval_state
FROM tech_card tc JOIN tech_card_costing c ON c.tech_card_id = tc.id
WHERE COALESCE(c.packaging_cost, 0) <> 0 AND tc.approval_state <> 'draft';

INSERT IGNORE INTO tech_card_costing_migration_exception (tech_card_id, article, kind, amount, currency, approval_state)
SELECT tc.id, 'hardware', 'zero_colorways', c.hardware_cost, NULLIF(c.currency, ''), tc.approval_state
FROM tech_card tc JOIN tech_card_costing c ON c.tech_card_id = tc.id
WHERE COALESCE(c.hardware_cost, 0) <> 0 AND tc.approval_state = 'draft'
  AND NOT EXISTS (SELECT 1 FROM product p WHERE p.style_id = tc.id);

INSERT IGNORE INTO tech_card_costing_migration_exception (tech_card_id, article, kind, amount, currency, approval_state)
SELECT tc.id, 'packaging', 'zero_colorways', c.packaging_cost, NULLIF(c.currency, ''), tc.approval_state
FROM tech_card tc JOIN tech_card_costing c ON c.tech_card_id = tc.id
WHERE COALESCE(c.packaging_cost, 0) <> 0 AND tc.approval_state = 'draft'
  AND NOT EXISTS (SELECT 1 FROM product p WHERE p.style_id = tc.id);

-- double_counted: a WIRED authored row in the section — priced (unit_price set) and carried by a
-- colourway usage with any consumption axis (per-garment, countable, or per-size). Only such a row
-- contributes to LineTotal/SizeRunTotal, so only such a row makes the scalar a double-count. The
-- sentinel name excludes rows a crashed earlier run of THIS migration inserted, so a re-run cannot
-- misclassify a half-migrated card.

INSERT IGNORE INTO tech_card_costing_migration_exception (tech_card_id, article, kind, amount, currency, approval_state)
SELECT tc.id, 'hardware', 'double_counted', c.hardware_cost, NULLIF(c.currency, ''), tc.approval_state
FROM tech_card tc JOIN tech_card_costing c ON c.tech_card_id = tc.id
WHERE COALESCE(c.hardware_cost, 0) <> 0 AND tc.approval_state = 'draft'
  AND EXISTS (SELECT 1 FROM tech_card_bom_item b
              WHERE b.tech_card_id = tc.id AND b.section = 'hardware'
                AND b.name <> 'Hardware (migrated from costing)'
                AND b.unit_price IS NOT NULL
                AND EXISTS (SELECT 1 FROM tech_card_colorway_usage u
                            WHERE u.bom_item_id = b.id
                              AND (u.consumption IS NOT NULL OR u.quantity IS NOT NULL
                                   OR EXISTS (SELECT 1 FROM tech_card_colorway_usage_consumption sc
                                              WHERE sc.usage_id = u.id))));

INSERT IGNORE INTO tech_card_costing_migration_exception (tech_card_id, article, kind, amount, currency, approval_state)
SELECT tc.id, 'packaging', 'double_counted', c.packaging_cost, NULLIF(c.currency, ''), tc.approval_state
FROM tech_card tc JOIN tech_card_costing c ON c.tech_card_id = tc.id
WHERE COALESCE(c.packaging_cost, 0) <> 0 AND tc.approval_state = 'draft'
  AND EXISTS (SELECT 1 FROM tech_card_bom_item b
              WHERE b.tech_card_id = tc.id AND b.section = 'packaging'
                AND b.name <> 'Packaging (migrated from costing)'
                AND b.unit_price IS NOT NULL
                AND EXISTS (SELECT 1 FROM tech_card_colorway_usage u
                            WHERE u.bom_item_id = b.id
                              AND (u.consumption IS NOT NULL OR u.quantity IS NOT NULL
                                   OR EXISTS (SELECT 1 FROM tech_card_colorway_usage_consumption sc
                                              WHERE sc.usage_id = u.id))));

-- --- the migration itself: draft + colourways + no WIRED row and no sentinel row in the section
--     -> synthetic BOM line (an unwired authored row stays beside it as description) ---

INSERT INTO tech_card_bom_item (tech_card_id, section, name, unit, unit_price, currency, display_order)
SELECT tc.id, 'hardware', 'Hardware (migrated from costing)', 'pcs', c.hardware_cost, NULLIF(c.currency, ''),
       COALESCE((SELECT MAX(b2.display_order) + 1 FROM tech_card_bom_item b2 WHERE b2.tech_card_id = tc.id), 0)
FROM tech_card tc JOIN tech_card_costing c ON c.tech_card_id = tc.id
WHERE COALESCE(c.hardware_cost, 0) <> 0 AND tc.approval_state = 'draft'
  AND EXISTS (SELECT 1 FROM product p WHERE p.style_id = tc.id)
  AND NOT EXISTS (SELECT 1 FROM tech_card_bom_item b
                  WHERE b.tech_card_id = tc.id AND b.section = 'hardware'
                    AND b.name <> 'Hardware (migrated from costing)'
                    AND b.unit_price IS NOT NULL
                    AND EXISTS (SELECT 1 FROM tech_card_colorway_usage u
                                WHERE u.bom_item_id = b.id
                                  AND (u.consumption IS NOT NULL OR u.quantity IS NOT NULL
                                       OR EXISTS (SELECT 1 FROM tech_card_colorway_usage_consumption sc
                                                  WHERE sc.usage_id = u.id))))
  AND NOT EXISTS (SELECT 1 FROM tech_card_bom_item b
                  WHERE b.tech_card_id = tc.id AND b.section = 'hardware'
                    AND b.name = 'Hardware (migrated from costing)');

INSERT INTO tech_card_bom_item (tech_card_id, section, name, unit, unit_price, currency, display_order)
SELECT tc.id, 'packaging', 'Packaging (migrated from costing)', 'pcs', c.packaging_cost, NULLIF(c.currency, ''),
       COALESCE((SELECT MAX(b2.display_order) + 1 FROM tech_card_bom_item b2 WHERE b2.tech_card_id = tc.id), 0)
FROM tech_card tc JOIN tech_card_costing c ON c.tech_card_id = tc.id
WHERE COALESCE(c.packaging_cost, 0) <> 0 AND tc.approval_state = 'draft'
  AND EXISTS (SELECT 1 FROM product p WHERE p.style_id = tc.id)
  AND NOT EXISTS (SELECT 1 FROM tech_card_bom_item b
                  WHERE b.tech_card_id = tc.id AND b.section = 'packaging'
                    AND b.name <> 'Packaging (migrated from costing)'
                    AND b.unit_price IS NOT NULL
                    AND EXISTS (SELECT 1 FROM tech_card_colorway_usage u
                                WHERE u.bom_item_id = b.id
                                  AND (u.consumption IS NOT NULL OR u.quantity IS NOT NULL
                                       OR EXISTS (SELECT 1 FROM tech_card_colorway_usage_consumption sc
                                                  WHERE sc.usage_id = u.id))))
  AND NOT EXISTS (SELECT 1 FROM tech_card_bom_item b
                  WHERE b.tech_card_id = tc.id AND b.section = 'packaging'
                    AND b.name = 'Packaging (migrated from costing)');

-- line_key for the rows just inserted (0159 pattern: LEGACY + zero-padded id = 26 chars, unique
-- because id is; every pre-0159 row was already backfilled, so only OUR rows are keyless here).
UPDATE tech_card_bom_item SET line_key = CONCAT('LEGACY', LPAD(id, 20, '0'))
    WHERE line_key IS NULL OR line_key = '';

-- one usage per colourway (consumption 1: the scalar was per garment, so is the BOM line now).
-- Post-R1 a colourway IS a product of the style: usage.colorway_id references product(id).
-- The section filter keeps a like-named row in any OTHER section out of reach.
INSERT INTO tech_card_colorway_usage (colorway_id, bom_item_id, consumption, display_order)
SELECT p.id, b.id, 1, 0
FROM tech_card_bom_item b
JOIN product p ON p.style_id = b.tech_card_id
WHERE ((b.section = 'hardware' AND b.name = 'Hardware (migrated from costing)')
       OR (b.section = 'packaging' AND b.name = 'Packaging (migrated from costing)'))
  AND NOT EXISTS (SELECT 1 FROM tech_card_colorway_usage u
                  WHERE u.colorway_id = p.id AND u.bom_item_id = b.id);

-- clear the migrated scalars: draft cards whose section now carries the money — either our
-- synthetic sentinel row (the migrated population, incl. beside an unwired authored row) or a
-- WIRED authored row (the recorded double_counted population, where the BOM row wins).
UPDATE tech_card_costing c
JOIN tech_card tc ON tc.id = c.tech_card_id
SET c.hardware_cost = NULL
WHERE COALESCE(c.hardware_cost, 0) <> 0 AND tc.approval_state = 'draft'
  AND (EXISTS (SELECT 1 FROM tech_card_bom_item b
               WHERE b.tech_card_id = tc.id AND b.section = 'hardware'
                 AND b.name = 'Hardware (migrated from costing)')
       OR EXISTS (SELECT 1 FROM tech_card_bom_item b
                  WHERE b.tech_card_id = tc.id AND b.section = 'hardware'
                    AND b.name <> 'Hardware (migrated from costing)'
                    AND b.unit_price IS NOT NULL
                    AND EXISTS (SELECT 1 FROM tech_card_colorway_usage u
                                WHERE u.bom_item_id = b.id
                                  AND (u.consumption IS NOT NULL OR u.quantity IS NOT NULL
                                       OR EXISTS (SELECT 1 FROM tech_card_colorway_usage_consumption sc
                                                  WHERE sc.usage_id = u.id)))));

UPDATE tech_card_costing c
JOIN tech_card tc ON tc.id = c.tech_card_id
SET c.packaging_cost = NULL
WHERE COALESCE(c.packaging_cost, 0) <> 0 AND tc.approval_state = 'draft'
  AND (EXISTS (SELECT 1 FROM tech_card_bom_item b
               WHERE b.tech_card_id = tc.id AND b.section = 'packaging'
                 AND b.name = 'Packaging (migrated from costing)')
       OR EXISTS (SELECT 1 FROM tech_card_bom_item b
                  WHERE b.tech_card_id = tc.id AND b.section = 'packaging'
                    AND b.name <> 'Packaging (migrated from costing)'
                    AND b.unit_price IS NOT NULL
                    AND EXISTS (SELECT 1 FROM tech_card_colorway_usage u
                                WHERE u.bom_item_id = b.id
                                  AND (u.consumption IS NOT NULL OR u.quantity IS NOT NULL
                                       OR EXISTS (SELECT 1 FROM tech_card_colorway_usage_consumption sc
                                                  WHERE sc.usage_id = u.id)))));

-- +migrate Down

-- The scalar->BOM migration is not mechanically reversible (the synthetic rows may have been edited
-- since); the exception table is a report. Intentional no-op.
SELECT 1;
