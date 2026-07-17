-- +migrate Up

-- WS5 (PLM rework, S10): category -> permitted size-system(s) mapping. Closes the size-picker gap
-- (02-CURRENT-STATE-AUDIT.md:261, CONFIRMED): the admin size picker groups sizes by gender only and
-- never by the style's category, and there is no backend size<->category validation at all -- a
-- style can be handed shoe sizes on a jacket. Domain model: tmp/plm-rework/01-DOMAIN-MODEL.md §2.6.
--
-- category_size_system maps a category-tree node to one or more entity.SizeSKUSystem values
-- (apparel|shoe|composite_ta|composite_bo -- migration 0147/entity.ValidSizeSKUSystems). A row
-- targets EITHER a broad category node (category_id set; seeded here at level_id=1/top_category) OR
-- a specific leaf type (type_id set) for a narrower override -- never both NULL
-- (chk_category_size_system_target). Server-side resolution (internal/entity.ResolveSizeSystemPolicy,
-- WS5) walks most-specific-first: a style's type_id match wins outright over its sub/top category,
-- then sub_category_id, then top_category_id.
--
-- Fallback rules (WS5 brief, documented in full in tmp/plm-rework/workstreams/ws5-sizes-colorways.md):
--   - A style with NO category assigned at all (top_category_id NULL -- mid-creation, category not
--     chosen yet) is UNRESTRICTED: there is nothing yet to validate against.
--   - A style with a category assigned but whose full path (type -> sub -> top) matches ZERO rows
--     here ("categories without a grid", e.g. bags/objects) falls back to the 'os' one-size apparel
--     entry (0131) ONLY -- not to "anything goes". This is why bags/objects deliberately get no rows
--     below: an empty mapping on a set category IS the OS-fallback signal, not an oversight.
--   - 'os' already lives inside the apparel system (0131: apparel = os|xxs|xs|s|m|l|xl|xxl), so
--     "accessories -> apparel" also covers one-size accessories; OS is not a separate size-system.
--
-- Idempotent: CREATE TABLE IF NOT EXISTS covers the table plus its inline UNIQUE/CHECK constraints
-- (a brand-new table needs no guarded ALTER). Every seed INSERT is guarded by NOT EXISTS with the
-- NULL-safe equality operator (<=>): category_id/type_id are nullable and a UNIQUE KEY does NOT
-- dedupe NULLs in MySQL (two rows that agree only where NULL is involved are not "duplicates" to the
-- index), so a plain INSERT IGNORE would re-insert a NULL-column seed row on every rerun.

CREATE TABLE IF NOT EXISTS category_size_system (
    id           INT PRIMARY KEY AUTO_INCREMENT,
    category_id  INT NULL COMMENT 'FK category(id); broad category/sub-category node',
    type_id      INT NULL COMMENT 'FK category(id); specific leaf type, narrower than category_id',
    size_system  VARCHAR(24) NOT NULL COMMENT 'entity.SizeSKUSystem: apparel|shoe|composite_ta|composite_bo',
    created_at   TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT fk_category_size_system_category FOREIGN KEY (category_id) REFERENCES category(id) ON DELETE CASCADE,
    CONSTRAINT fk_category_size_system_type     FOREIGN KEY (type_id)     REFERENCES category(id) ON DELETE CASCADE,
    CONSTRAINT chk_category_size_system_system  CHECK (BINARY size_system IN ('apparel','shoe','composite_ta','composite_bo')),
    CONSTRAINT chk_category_size_system_target  CHECK (category_id IS NOT NULL OR type_id IS NOT NULL),
    CONSTRAINT uniq_category_size_system UNIQUE (category_id, type_id, size_system)
) COMMENT 'S10/WS5: which SizeSKUSystem(s) a category-tree node permits';

-- Cache-invalidation namespace, parity with color/collection/tag/country/size/measurement (0154): the
-- row exists so a future admin-editable mapping doesn't need a follow-up migration to register the
-- namespace. No CRUD RPC bumps it yet -- WS5 ships this table seed-only, read through the existing
-- dictionary cache refresh (internal/cache.RefreshDictionary), same as `category`/`size` themselves.
INSERT IGNORE INTO dictionary_revision (namespace, revision) VALUES ('category_size_system', 1);

-- ---------------------------------------------------------------------------
-- Seed: top-level (level_id=1) category -> system. Matches the 9 top-level categories seeded by
-- 0001_initial_setup.sql exactly (outerwear, tops, bottoms, dresses, loungewear_sleepwear,
-- accessories, shoes, bags, objects) -- a name that does not match is silently a no-op (MySQL
-- INSERT...SELECT...WHERE with no matching row inserts nothing, no error), which is exactly why
-- migrationlint's TestCategorySizeSystemMappingCoversKnownCategories statically checks these names
-- against the 0001 seed text.
-- ---------------------------------------------------------------------------

INSERT INTO category_size_system (category_id, type_id, size_system)
SELECT c.id, NULL, x.size_system
FROM category c
JOIN (
    SELECT 'outerwear'            AS name, 'apparel' AS size_system UNION ALL
    SELECT 'tops',                          'apparel'               UNION ALL
    SELECT 'bottoms',                       'apparel'               UNION ALL
    SELECT 'dresses',                       'apparel'               UNION ALL
    SELECT 'loungewear_sleepwear',          'apparel'               UNION ALL
    SELECT 'accessories',                   'apparel'               UNION ALL
    SELECT 'shoes',                         'shoe'
) x ON x.name = c.name
WHERE c.level_id = 1
  AND NOT EXISTS (
    SELECT 1 FROM category_size_system e
    WHERE e.category_id <=> c.id AND e.type_id <=> NULL AND e.size_system = x.size_system
  );

-- bags / objects: deliberately NO rows inserted for them -- "categories without a grid" (WS5 brief)
-- fall back to the 'os' one-size apparel entry at validation time (entity.ResolveSizeSystemPolicy),
-- per the fallback rule documented above. An absent row IS the fallback signal, not a gap to fill.

-- ---------------------------------------------------------------------------
-- AUTONOMOUS-CALL (WS5, narrow + additive, easy to revise -- see the Dump for full rationale): the
-- tailored composite systems (composite_ta/composite_bo; 0018/0019 seeded the sizes, 0147 sealed the
-- SKU contract) predate this table and carry no prior category linkage anywhere in the codebase.
-- Leaving them completely unmapped would make every category_size_system-validated write reject them
-- outright, silently retiring 2 of the 4 SKU systems R7 established as canonical. Mapped narrowly to
-- the one leaf type each is unambiguously for (chest-tailored jacket / waist-tailored trouser),
-- ADDITIONAL to -- not instead of -- the inherited apparel letter-size option, so a blazer/trousers
-- style keeps the ordinary S/M/L route available too. No other type gets a composite mapping.
-- ---------------------------------------------------------------------------

INSERT INTO category_size_system (category_id, type_id, size_system)
SELECT NULL, t.id, y.size_system
FROM category t
JOIN category sub ON sub.id = t.parent_id AND sub.name = 'jackets'
CROSS JOIN (SELECT 'composite_ta' AS size_system UNION ALL SELECT 'apparel') y
WHERE t.name = 'blazer' AND t.level_id = 3
  AND NOT EXISTS (
    SELECT 1 FROM category_size_system e
    WHERE e.category_id <=> NULL AND e.type_id <=> t.id AND e.size_system = y.size_system
  );

INSERT INTO category_size_system (category_id, type_id, size_system)
SELECT NULL, t.id, y.size_system
FROM category t
JOIN category sub ON sub.id = t.parent_id AND sub.name = 'pants'
CROSS JOIN (SELECT 'composite_bo' AS size_system UNION ALL SELECT 'apparel') y
WHERE t.name = 'trousers' AND t.level_id = 3
  AND NOT EXISTS (
    SELECT 1 FROM category_size_system e
    WHERE e.category_id <=> NULL AND e.type_id <=> t.id AND e.size_system = y.size_system
  );
