-- +migrate Up
-- PLM-rework WS7 (AUTONOMOUS-CALL): aux_subtype sub-classifies an AUXILIARY tech card (purpose=auxiliary)
-- into the concrete kind of non-sold item it produces — brand/care/size label, hangtag, sticker, dust bag,
-- box, insert, hanger, or other. It REFINES tech_card.purpose (§2.1/§2.8, NF-07): an auxiliary item is a
-- tech card that produces a MATERIAL (output_material_id) and has no product/colourway row, so the sub-type
-- lives here beside `purpose`, NOT on `product` (a colourway is never a hangtag/dust bag). Additive +
-- nullable: every sellable card keeps NULL and nothing on the sellable path reads it.
--
-- Two named CHECKs: (1) the closed value set; (2) a purpose gate — a sub-type only makes sense for an
-- auxiliary card. Both hold for every row this migration touches (backfill is auxiliary-only) and for all
-- sellable rows (NULL). Naming them explicitly lets a later migration drop them by a stable name (never a
-- positional <table>_chk_<n>).
--
-- Backfill: a best-effort name heuristic over EXISTING auxiliary cards ONLY, kept 1:1 in lockstep with
-- entity.AuxSubtypeFromName (unit-tested). Unmatched names stay NULL — reported, not guessed. Idempotent:
-- fills only rows still NULL, so a re-run after a manual correction never clobbers it. Operators can see
-- what mapped vs. stayed NULL with:
--   SELECT id, name, aux_subtype FROM tech_card WHERE purpose='auxiliary' ORDER BY aux_subtype IS NULL, id;
--
-- Idempotent/guarded: ADD COLUMN + named CHECKs added only when absent (MySQL 8 has no IF NOT EXISTS for
-- these), so a mid-file DDL failure re-runs cleanly from the top.

SET @need_col := (SELECT COUNT(*) = 0 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card' AND COLUMN_NAME = 'aux_subtype');
SET @sql := IF(@need_col,
    'ALTER TABLE tech_card ADD COLUMN aux_subtype VARCHAR(16) NULL',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

SET @need_chk := (SELECT COUNT(*) = 0 FROM information_schema.TABLE_CONSTRAINTS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card' AND CONSTRAINT_NAME = 'chk_tech_card_aux_subtype');
SET @sql := IF(@need_chk,
    'ALTER TABLE tech_card ADD CONSTRAINT chk_tech_card_aux_subtype CHECK (aux_subtype IS NULL OR aux_subtype REGEXP ''^(brand_label|care_label|size_label|hangtag|sticker|dust_bag|box|insert|hanger|other)$'')',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

SET @need_gate := (SELECT COUNT(*) = 0 FROM information_schema.TABLE_CONSTRAINTS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card' AND CONSTRAINT_NAME = 'chk_tech_card_aux_subtype_purpose');
SET @sql := IF(@need_gate,
    'ALTER TABLE tech_card ADD CONSTRAINT chk_tech_card_aux_subtype_purpose CHECK (aux_subtype IS NULL OR purpose = ''auxiliary'')',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

-- Best-effort backfill (auxiliary + still-NULL only). MUST stay identical to entity.AuxSubtypeFromName;
-- first matching branch wins, most-specific first. Unmatched → left NULL for manual classification.
UPDATE tech_card
SET aux_subtype = CASE
    WHEN LOWER(name) LIKE '%dust%'                                          THEN 'dust_bag'
    WHEN LOWER(name) LIKE '%hangtag%' OR LOWER(name) LIKE '%hang tag%'
      OR LOWER(name) LIKE '%hang-tag%'                                      THEN 'hangtag'
    WHEN LOWER(name) LIKE '%care%'                                          THEN 'care_label'
    WHEN LOWER(name) LIKE '%size label%' OR LOWER(name) LIKE '%size-label%' THEN 'size_label'
    WHEN LOWER(name) LIKE '%brand%'                                         THEN 'brand_label'
    WHEN LOWER(name) LIKE '%sticker%'                                       THEN 'sticker'
    WHEN LOWER(name) LIKE '%hanger%'                                        THEN 'hanger'
    WHEN LOWER(name) LIKE '%insert%'                                        THEN 'insert'
    WHEN LOWER(name) LIKE '%box%'                                           THEN 'box'
    WHEN LOWER(name) LIKE '%shopper%' OR LOWER(name) LIKE '%garment bag%'   THEN 'dust_bag'
    ELSE NULL END
WHERE purpose = 'auxiliary' AND aux_subtype IS NULL;

-- +migrate Down
-- (leaves aux_subtype + CHECKs; a Down is not exercised in prod automigrate)
SELECT 1;
