-- +migrate Up

-- PR6 phase 2 (style-level de-dup), step 2 of 2: drop the garment-level columns from product now that
-- the STYLE (tech_card) owns them (0139 added + backfilled them) and every reader/writer has been
-- repointed to the style. A style is one pattern; its colourways differ only by colour, so these
-- fields belong to the style, not to each colourway. This removes the duplication — the single home is
-- the style (North Star). season is dropped as product.season (the 2-char catalogue code): the style
-- carries it as season_code (0134/0139).
--
-- The write path can no longer INSERT a product without these (they were NOT NULL), so this drop MUST
-- accompany the read/write flip — it is one atomic change with the Go repoint, not a later cleanup.
--
-- Idempotent + safe against auto-generated constraint names: the FK (product_ibfk_N) and CHECK
-- (product_chk_N) names are POSITIONAL and drift across schema history (CLAUDE.md), so we NEVER drop
-- them by literal name — we resolve the actual name from information_schema by column/clause and drop
-- via a prepared statement. Columns are dropped with a single dynamically-built ALTER that names only
-- the columns still present, so a re-run (or a mid-file retry) is a no-op. Multi-line PREPARE/EXECUTE/
-- DEALLOCATE — a single line trips 1064 on the managed DSN (0124).

-- 1) Drop the category foreign keys (top/sub/type) by resolved name.
SET @fk := (SELECT CONSTRAINT_NAME FROM information_schema.KEY_COLUMN_USAGE
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'product'
      AND COLUMN_NAME = 'top_category_id' AND REFERENCED_TABLE_NAME IS NOT NULL LIMIT 1);
SET @sql := IF(@fk IS NOT NULL, CONCAT('ALTER TABLE product DROP FOREIGN KEY ', @fk), 'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

SET @fk := (SELECT CONSTRAINT_NAME FROM information_schema.KEY_COLUMN_USAGE
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'product'
      AND COLUMN_NAME = 'sub_category_id' AND REFERENCED_TABLE_NAME IS NOT NULL LIMIT 1);
SET @sql := IF(@fk IS NOT NULL, CONCAT('ALTER TABLE product DROP FOREIGN KEY ', @fk), 'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

SET @fk := (SELECT CONSTRAINT_NAME FROM information_schema.KEY_COLUMN_USAGE
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'product'
      AND COLUMN_NAME = 'type_id' AND REFERENCED_TABLE_NAME IS NOT NULL LIMIT 1);
SET @sql := IF(@fk IS NOT NULL, CONCAT('ALTER TABLE product DROP FOREIGN KEY ', @fk), 'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

-- 2) Drop the CHECK constraints tied to the columns being removed (target_gender, care_instructions,
--    season), resolved by clause content so the positional chk_N name never matters.
SET @ck := (SELECT tc.CONSTRAINT_NAME FROM information_schema.TABLE_CONSTRAINTS tc
    JOIN information_schema.CHECK_CONSTRAINTS cc
      ON cc.CONSTRAINT_SCHEMA = tc.CONSTRAINT_SCHEMA AND cc.CONSTRAINT_NAME = tc.CONSTRAINT_NAME
    WHERE tc.TABLE_SCHEMA = DATABASE() AND tc.TABLE_NAME = 'product' AND tc.CONSTRAINT_TYPE = 'CHECK'
      AND cc.CHECK_CLAUSE LIKE '%`target_gender`%' LIMIT 1);
SET @sql := IF(@ck IS NOT NULL, CONCAT('ALTER TABLE product DROP CHECK ', @ck), 'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

SET @ck := (SELECT tc.CONSTRAINT_NAME FROM information_schema.TABLE_CONSTRAINTS tc
    JOIN information_schema.CHECK_CONSTRAINTS cc
      ON cc.CONSTRAINT_SCHEMA = tc.CONSTRAINT_SCHEMA AND cc.CONSTRAINT_NAME = tc.CONSTRAINT_NAME
    WHERE tc.TABLE_SCHEMA = DATABASE() AND tc.TABLE_NAME = 'product' AND tc.CONSTRAINT_TYPE = 'CHECK'
      AND cc.CHECK_CLAUSE LIKE '%`care_instructions`%' LIMIT 1);
SET @sql := IF(@ck IS NOT NULL, CONCAT('ALTER TABLE product DROP CHECK ', @ck), 'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

SET @ck := (SELECT tc.CONSTRAINT_NAME FROM information_schema.TABLE_CONSTRAINTS tc
    JOIN information_schema.CHECK_CONSTRAINTS cc
      ON cc.CONSTRAINT_SCHEMA = tc.CONSTRAINT_SCHEMA AND cc.CONSTRAINT_NAME = tc.CONSTRAINT_NAME
    WHERE tc.TABLE_SCHEMA = DATABASE() AND tc.TABLE_NAME = 'product' AND tc.CONSTRAINT_TYPE = 'CHECK'
      AND cc.CHECK_CLAUSE LIKE '%`season`%' LIMIT 1);
SET @sql := IF(@ck IS NOT NULL, CONCAT('ALTER TABLE product DROP CHECK ', @ck), 'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

-- 3) Drop the columns. Build the ALTER from only the columns still present, so a re-run is a no-op.
--    Dropping a column also drops the single-column index auto-created for its FK (top/sub/type) and
--    idx_product_target_gender.
SET @cols := (SELECT GROUP_CONCAT(CONCAT('DROP COLUMN ', COLUMN_NAME) SEPARATOR ', ')
    FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'product'
      AND COLUMN_NAME IN ('brand','season','collection','target_gender','fit','composition',
                          'care_instructions','model_wears_height_cm','model_wears_size_id',
                          'top_category_id','sub_category_id','type_id'));
SET @sql := IF(@cols IS NOT NULL, CONCAT('ALTER TABLE product ', @cols), 'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;
