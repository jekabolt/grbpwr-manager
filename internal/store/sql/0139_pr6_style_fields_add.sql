-- +migrate Up

-- PR6 phase 2 (style-level de-dup), step 1 of 3: give the STYLE (tech_card) the garment-level fields
-- that are invariant across a style's colourways. A style is one pattern/design; its colourways differ
-- only by colour. So brand/season/collection/target_gender/fit/composition/care/model_wears and the
-- category taxonomy (top/sub/type) belong to the STYLE, not to each colourway (product) — today they
-- are duplicated onto every product. This step is purely ADDITIVE: it adds the missing columns to
-- tech_card and backfills them from the representative linked product (today's source of truth).
-- Nothing reads them yet, so the app behaves identically; the next steps switch product's read/write
-- to the style and drop the duplicated product columns.
--
-- brand/season/collection/target_gender already exist on tech_card. A non-NULL value is preserved only
-- when it agrees exactly with the catalogue; disagreement stops for explicit reconciliation. NULL
-- targets are backfilled from the agreed product source. The other fields are freshly added.
--
-- Representative product = MIN(product.id) per style_id, but only after a persisted fail-fast
-- reconciliation proves that every sibling agrees and that already populated style values do not
-- disagree with the catalogue. Styles with no product keep NULLs. The typed season pair is not
-- inferred here: 0134 owns real tech-card seasons and 0138 creates a complete pair for synthetic
-- standalone styles. Missing/partial/mismatched target season is therefore an explicit conflict.
-- care_instructions is added WITHOUT the product's belt-and-suspenders regex CHECK — validation is
-- enforced in the write path (which moves to the style in step 3); backfilled values already passed it.
--
-- Idempotent: guarded ADD COLUMN / ADD FK via information_schema (multi-line PREPARE/EXECUTE/DEALLOCATE
-- — a single line trips 1064 on the managed DSN, see 0124); audit rows are rebuilt deterministically,
-- and the backfill only fills NULL targets, so a re-run yields the same result.

-- 1) Columns (guard on top_category_id — all added together).
SET @need_cols := (SELECT COUNT(*) = 0 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card' AND COLUMN_NAME = 'top_category_id');
SET @sql := IF(@need_cols,
    'ALTER TABLE tech_card
        ADD COLUMN fit                   VARCHAR(50)  NULL,
        ADD COLUMN composition           JSON         NULL,
        ADD COLUMN care_instructions     VARCHAR(255) NULL,
        ADD COLUMN model_wears_height_cm INT          NULL,
        ADD COLUMN model_wears_size_id   INT          NULL,
        ADD COLUMN top_category_id       INT          NULL,
        ADD COLUMN sub_category_id       INT          NULL,
        ADD COLUMN type_id               INT          NULL',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

-- 2) Foreign keys (each guarded on its own so a partial prior run self-heals).
SET @need_fk := (SELECT COUNT(*) = 0 FROM information_schema.TABLE_CONSTRAINTS
    WHERE CONSTRAINT_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card'
      AND CONSTRAINT_NAME = 'fk_tech_card_model_wears_size');
SET @sql := IF(@need_fk,
    'ALTER TABLE tech_card ADD CONSTRAINT fk_tech_card_model_wears_size
        FOREIGN KEY (model_wears_size_id) REFERENCES size(id)',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

SET @need_fk := (SELECT COUNT(*) = 0 FROM information_schema.TABLE_CONSTRAINTS
    WHERE CONSTRAINT_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card'
      AND CONSTRAINT_NAME = 'fk_tech_card_top_category');
SET @sql := IF(@need_fk,
    'ALTER TABLE tech_card ADD CONSTRAINT fk_tech_card_top_category
        FOREIGN KEY (top_category_id) REFERENCES category(id)',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

SET @need_fk := (SELECT COUNT(*) = 0 FROM information_schema.TABLE_CONSTRAINTS
    WHERE CONSTRAINT_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card'
      AND CONSTRAINT_NAME = 'fk_tech_card_sub_category');
SET @sql := IF(@need_fk,
    'ALTER TABLE tech_card ADD CONSTRAINT fk_tech_card_sub_category
        FOREIGN KEY (sub_category_id) REFERENCES category(id)',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

SET @need_fk := (SELECT COUNT(*) = 0 FROM information_schema.TABLE_CONSTRAINTS
    WHERE CONSTRAINT_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card'
      AND CONSTRAINT_NAME = 'fk_tech_card_type_category');
SET @sql := IF(@need_fk,
    'ALTER TABLE tech_card ADD CONSTRAINT fk_tech_card_type_category
        FOREIGN KEY (type_id) REFERENCES category(id)',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

-- 3) Persist every unresolved source/target conflict before touching tech_card. This table is an
-- operator-facing reconciliation report and deliberately survives a failed migration. Exact binary
-- comparison is used for strings (case and NULL/empty differences are material); JSON uses MySQL's
-- structural NULL-safe equality. product_id_b=0 denotes a style-target or orphan-reference check.
CREATE TABLE IF NOT EXISTS migration_0139_style_field_conflict (
    conflict_type              VARCHAR(32) NOT NULL,
    style_id                   INT NOT NULL,
    product_id_a               INT NOT NULL,
    product_id_b               INT NOT NULL,
    brand_diff                 BOOLEAN NOT NULL DEFAULT FALSE,
    season_diff                BOOLEAN NOT NULL DEFAULT FALSE,
    season_year_invalid        BOOLEAN NOT NULL DEFAULT FALSE,
    collection_diff            BOOLEAN NOT NULL DEFAULT FALSE,
    target_gender_diff         BOOLEAN NOT NULL DEFAULT FALSE,
    fit_diff                   BOOLEAN NOT NULL DEFAULT FALSE,
    composition_diff           BOOLEAN NOT NULL DEFAULT FALSE,
    care_instructions_diff     BOOLEAN NOT NULL DEFAULT FALSE,
    model_wears_height_cm_diff BOOLEAN NOT NULL DEFAULT FALSE,
    model_wears_size_id_diff   BOOLEAN NOT NULL DEFAULT FALSE,
    top_category_id_diff       BOOLEAN NOT NULL DEFAULT FALSE,
    sub_category_id_diff       BOOLEAN NOT NULL DEFAULT FALSE,
    type_id_diff               BOOLEAN NOT NULL DEFAULT FALSE,
    model_wears_size_orphan    BOOLEAN NOT NULL DEFAULT FALSE,
    top_category_orphan        BOOLEAN NOT NULL DEFAULT FALSE,
    sub_category_orphan        BOOLEAN NOT NULL DEFAULT FALSE,
    type_category_orphan       BOOLEAN NOT NULL DEFAULT FALSE,
    detected_at                TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (conflict_type, style_id, product_id_a, product_id_b)
);

DELETE FROM migration_0139_style_field_conflict;

-- Sibling products mapped to one style must be byte-for-byte equal in every field about to be
-- removed. A pair is recorded once (a.id < b.id), with field-level flags for reconciliation.
INSERT INTO migration_0139_style_field_conflict (
    conflict_type, style_id, product_id_a, product_id_b,
    brand_diff, season_diff, collection_diff, target_gender_diff, fit_diff, composition_diff,
    care_instructions_diff, model_wears_height_cm_diff, model_wears_size_id_diff,
    top_category_id_diff, sub_category_id_diff, type_id_diff
)
SELECT
    'sibling_mismatch', a.style_id, a.id, b.id,
    NOT (CAST(a.brand AS BINARY) <=> CAST(b.brand AS BINARY)),
    NOT (CAST(a.season AS BINARY) <=> CAST(b.season AS BINARY)),
    NOT (CAST(a.collection AS BINARY) <=> CAST(b.collection AS BINARY)),
    NOT (CAST(a.target_gender AS BINARY) <=> CAST(b.target_gender AS BINARY)),
    NOT (CAST(a.fit AS BINARY) <=> CAST(b.fit AS BINARY)),
    NOT (a.composition <=> b.composition),
    NOT (CAST(a.care_instructions AS BINARY) <=> CAST(b.care_instructions AS BINARY)),
    NOT (a.model_wears_height_cm <=> b.model_wears_height_cm),
    NOT (a.model_wears_size_id <=> b.model_wears_size_id),
    NOT (a.top_category_id <=> b.top_category_id),
    NOT (a.sub_category_id <=> b.sub_category_id),
    NOT (a.type_id <=> b.type_id)
FROM product a
JOIN product b ON b.style_id = a.style_id AND b.id > a.id
WHERE NOT (
       (CAST(a.brand AS BINARY) <=> CAST(b.brand AS BINARY))
   AND (CAST(a.season AS BINARY) <=> CAST(b.season AS BINARY))
   AND (CAST(a.collection AS BINARY) <=> CAST(b.collection AS BINARY))
   AND (CAST(a.target_gender AS BINARY) <=> CAST(b.target_gender AS BINARY))
   AND (CAST(a.fit AS BINARY) <=> CAST(b.fit AS BINARY))
   AND (a.composition <=> b.composition)
   AND (CAST(a.care_instructions AS BINARY) <=> CAST(b.care_instructions AS BINARY))
   AND (a.model_wears_height_cm <=> b.model_wears_height_cm)
   AND (a.model_wears_size_id <=> b.model_wears_size_id)
   AND (a.top_category_id <=> b.top_category_id)
   AND (a.sub_category_id <=> b.sub_category_id)
   AND (a.type_id <=> b.type_id)
);

-- Equal siblings can still disagree with an already populated PLM/style value. NULL target fields
-- are safe to backfill, but any non-NULL mismatch requires an explicit winner. A complete canonical
-- season pair is mandatory because product.season is about to be dropped.
INSERT INTO migration_0139_style_field_conflict (
    conflict_type, style_id, product_id_a, product_id_b,
    brand_diff, season_diff, season_year_invalid, collection_diff, target_gender_diff,
    fit_diff, composition_diff, care_instructions_diff, model_wears_height_cm_diff,
    model_wears_size_id_diff, top_category_id_diff, sub_category_id_diff, type_id_diff
)
SELECT
    'target_mismatch', t.id, p.id, 0,
    t.brand IS NOT NULL AND NOT (CAST(t.brand AS BINARY) <=> CAST(p.brand AS BINARY)),
    NOT (CAST(t.season_code AS BINARY) <=> CAST(p.season AS BINARY)),
    t.season_year IS NULL OR t.season_year < 2000 OR t.season_year > 2099,
    t.collection IS NOT NULL AND NOT (CAST(t.collection AS BINARY) <=> CAST(p.collection AS BINARY)),
    t.target_gender IS NOT NULL AND NOT (CAST(t.target_gender AS BINARY) <=> CAST(p.target_gender AS BINARY)),
    t.fit IS NOT NULL AND NOT (CAST(t.fit AS BINARY) <=> CAST(p.fit AS BINARY)),
    t.composition IS NOT NULL AND NOT (t.composition <=> p.composition),
    t.care_instructions IS NOT NULL AND NOT (CAST(t.care_instructions AS BINARY) <=> CAST(p.care_instructions AS BINARY)),
    t.model_wears_height_cm IS NOT NULL AND NOT (t.model_wears_height_cm <=> p.model_wears_height_cm),
    t.model_wears_size_id IS NOT NULL AND NOT (t.model_wears_size_id <=> p.model_wears_size_id),
    t.top_category_id IS NOT NULL AND NOT (t.top_category_id <=> p.top_category_id),
    t.sub_category_id IS NOT NULL AND NOT (t.sub_category_id <=> p.sub_category_id),
    t.type_id IS NOT NULL AND NOT (t.type_id <=> p.type_id)
FROM tech_card t
JOIN (SELECT style_id, MIN(id) AS pid FROM product GROUP BY style_id) rep ON rep.style_id = t.id
JOIN product p ON p.id = rep.pid
WHERE (t.brand IS NOT NULL AND NOT (CAST(t.brand AS BINARY) <=> CAST(p.brand AS BINARY)))
   OR NOT (CAST(t.season_code AS BINARY) <=> CAST(p.season AS BINARY))
   OR t.season_year IS NULL OR t.season_year < 2000 OR t.season_year > 2099
   OR (t.collection IS NOT NULL AND NOT (CAST(t.collection AS BINARY) <=> CAST(p.collection AS BINARY)))
   OR (t.target_gender IS NOT NULL AND NOT (CAST(t.target_gender AS BINARY) <=> CAST(p.target_gender AS BINARY)))
   OR (t.fit IS NOT NULL AND NOT (CAST(t.fit AS BINARY) <=> CAST(p.fit AS BINARY)))
   OR (t.composition IS NOT NULL AND NOT (t.composition <=> p.composition))
   OR (t.care_instructions IS NOT NULL AND NOT (CAST(t.care_instructions AS BINARY) <=> CAST(p.care_instructions AS BINARY)))
   OR (t.model_wears_height_cm IS NOT NULL AND NOT (t.model_wears_height_cm <=> p.model_wears_height_cm))
   OR (t.model_wears_size_id IS NOT NULL AND NOT (t.model_wears_size_id <=> p.model_wears_size_id))
   OR (t.top_category_id IS NOT NULL AND NOT (t.top_category_id <=> p.top_category_id))
   OR (t.sub_category_id IS NOT NULL AND NOT (t.sub_category_id <=> p.sub_category_id))
   OR (t.type_id IS NOT NULL AND NOT (t.type_id <=> p.type_id));

-- Inline/legacy references that cannot be copied are data conflicts, not values to silently turn
-- into NULL. Record every affected product and stop before the backfill.
INSERT INTO migration_0139_style_field_conflict (
    conflict_type, style_id, product_id_a, product_id_b,
    model_wears_size_orphan, top_category_orphan, sub_category_orphan, type_category_orphan
)
SELECT
    'orphan_reference', p.style_id, p.id, 0,
    p.model_wears_size_id IS NOT NULL AND s.id IS NULL,
    p.top_category_id IS NOT NULL AND top_c.id IS NULL,
    p.sub_category_id IS NOT NULL AND sub_c.id IS NULL,
    p.type_id IS NOT NULL AND type_c.id IS NULL
FROM product p
LEFT JOIN size s ON s.id = p.model_wears_size_id
LEFT JOIN category top_c ON top_c.id = p.top_category_id
LEFT JOIN category sub_c ON sub_c.id = p.sub_category_id
LEFT JOIN category type_c ON type_c.id = p.type_id
WHERE (p.model_wears_size_id IS NOT NULL AND s.id IS NULL)
   OR (p.top_category_id IS NOT NULL AND top_c.id IS NULL)
   OR (p.sub_category_id IS NOT NULL AND sub_c.id IS NULL)
   OR (p.type_id IS NOT NULL AND type_c.id IS NULL);

-- A named CHECK is a top-level, PREPARE-safe assertion. CREATE after rebuilding the report also
-- forces MySQL to persist the report despite sql-migrate's transaction wrapper and DDL auto-commit.
CREATE TABLE IF NOT EXISTS migration_0139_style_field_guard (
    singleton      TINYINT NOT NULL PRIMARY KEY,
    conflict_count INT NOT NULL,
    CONSTRAINT migration_0139_no_style_field_conflicts CHECK (conflict_count = 0)
);

DELETE FROM migration_0139_style_field_guard;
INSERT INTO migration_0139_style_field_guard (singleton, conflict_count)
SELECT 1, COUNT(*) FROM migration_0139_style_field_conflict;

-- 4) Backfill from the representative product (MIN id per style). Existing non-NULL target values
-- are preserved; the preflight above has proved that each is identical to the agreed source.
-- NOTE on season: product.season is the 2-char catalogue CODE (SS/FW/PF/RC), which corresponds to the
-- style's normalized season_code (0134), NOT an independent label. The preflight requires the typed
-- target pair to exist and match; this migration never derives a real style's season from created_at.
--
-- ORPHAN SAFETY on the four FK-backed columns (model_wears_size_id, top/sub/type_category_id): product
-- declared these with INLINE column REFERENCES, which InnoDB parses but does NOT enforce — so a product
-- can carry a value whose size/category row was later deleted. The tech_card copies are guarded by REAL
-- foreign keys (added in step 2), so copying an orphan verbatim would trip ERROR 1452 and HALT PROD BOOT
-- (auto-migrate). The preflight records and rejects every orphan. The defensive subqueries below can
-- therefore only resolve healthy references; no broken value is silently converted to NULL.
UPDATE tech_card t
JOIN (SELECT style_id, MIN(id) AS pid FROM product GROUP BY style_id) rep ON rep.style_id = t.id
JOIN product p ON p.id = rep.pid
SET
    t.brand                 = COALESCE(t.brand, p.brand),
    t.collection            = COALESCE(t.collection, p.collection),
    t.target_gender         = COALESCE(t.target_gender, p.target_gender),
    t.fit                   = COALESCE(t.fit, p.fit),
    t.composition           = COALESCE(t.composition, p.composition),
    t.care_instructions     = COALESCE(t.care_instructions, p.care_instructions),
    t.model_wears_height_cm = COALESCE(t.model_wears_height_cm, p.model_wears_height_cm),
    t.model_wears_size_id   = COALESCE(t.model_wears_size_id, (SELECT s.id FROM size s WHERE s.id = p.model_wears_size_id)),
    t.top_category_id       = COALESCE(t.top_category_id, (SELECT c.id FROM category c WHERE c.id = p.top_category_id)),
    t.sub_category_id       = COALESCE(t.sub_category_id, (SELECT c.id FROM category c WHERE c.id = p.sub_category_id)),
    t.type_id               = COALESCE(t.type_id, (SELECT c.id FROM category c WHERE c.id = p.type_id));
