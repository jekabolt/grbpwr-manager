-- +migrate Up
-- PR6 R1 — Colourway domain merge (step 1 of 2: expand + backfill + repoint; NON-destructive).
--
-- Physically folds tech_card_colorway (the development colourway) INTO product (the catalog
-- colourway) per contract decision R1. The two Colourway models become one: PLM/lab-dip columns
-- move onto product; a development colourway with no product_id becomes a DRAFT product row; every
-- child that referenced tech_card_colorway(id) is value-remapped to the merged product(id) and its
-- foreign key repointed at product. tech_card_colorway is NOT dropped here — 0152 drops it behind a
-- re-checked guard (mirrors 0141/0142 and 0139/0140), so a crashed/retried apply of THIS file never
-- reads a table it already dropped.
--
-- Crash-idempotent: guarded ADD COLUMN / MODIFY / ADD FK / ADD UNIQUE via information_schema
-- (multi-line PREPARE — a single line trips 1064 on the managed DSN, see 0124/0139); the
-- migration_colorway_merge_map ledger is filled with INSERT ... NOT EXISTS; draft rows carry a
-- merge_src_colorway_id back-reference so a re-run never double-inserts; the linked-merge UPDATE
-- overwrites with identical values; each child repoint is guarded on its FK still pointing at
-- tech_card_colorway and swaps the value-remap + drop-old + add-new so MySQL's implicit DDL commit
-- makes the remap persist only together with the marker flip (no double-remap on retry).

-- ------------------------------------------------------------------------------------------------
-- 0) Fail-fast preflight. Persist every reconciliation conflict into an operator-facing report, then
-- a named CHECK asserts it is empty. Big-bang R1/R10: no MIN()/primary-colourway heuristics — any
-- inconsistency STOPS the migration (the report survives a failed apply for triage).
-- ------------------------------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS migration_0151_merge_conflict (
    conflict_type VARCHAR(32)  NOT NULL,
    tech_card_id  INT          NOT NULL,
    colorway_id   INT          NOT NULL DEFAULT 0, -- legacy tech_card_colorway.id (0 for aggregate rows)
    product_id    INT          NOT NULL DEFAULT 0,
    color_code    VARCHAR(3)   NOT NULL DEFAULT '',
    detected_at   TIMESTAMP    NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (conflict_type, tech_card_id, colorway_id, product_id, color_code)
);
DELETE FROM migration_0151_merge_conflict;

-- (a) split-brain (problem 011): a linked colourway whose product points at a different style than
-- the card that owns the colourway. product.style_id is authoritative; a mismatch is never guessed.
INSERT INTO migration_0151_merge_conflict (conflict_type, tech_card_id, colorway_id, product_id)
SELECT 'linked_style_mismatch', cw.tech_card_id, cw.id, cw.product_id
FROM tech_card_colorway cw
JOIN product p ON p.id = cw.product_id
WHERE cw.product_id IS NOT NULL AND p.style_id <> cw.tech_card_id;

-- (b) multi-link: one product realised by more than one colourway (the 1:1 invariant is violated).
INSERT INTO migration_0151_merge_conflict (conflict_type, tech_card_id, colorway_id, product_id)
SELECT 'multi_link', cw.tech_card_id, cw.id, cw.product_id
FROM tech_card_colorway cw
WHERE cw.product_id IS NOT NULL
  AND cw.product_id IN (
      SELECT product_id FROM tech_card_colorway
      WHERE product_id IS NOT NULL GROUP BY product_id HAVING COUNT(*) > 1);

-- (c) UNIQUE(style_id, color_code) collision after merge. Post-merge every colourway lands at
-- (style_id, color_code) = the existing product's pair (linked) or (tech_card_id, cw.color_code)
-- (a dev colourway becoming a draft). A duplicate in the union stops the merge.
INSERT INTO migration_0151_merge_conflict (conflict_type, tech_card_id, colorway_id, color_code)
SELECT 'style_color_collision', k.style_id, 0, k.color_code
FROM (
    SELECT style_id, color_code FROM product
    UNION ALL
    SELECT tech_card_id AS style_id, color_code FROM tech_card_colorway WHERE product_id IS NULL
) k
GROUP BY k.style_id, k.color_code
HAVING COUNT(*) > 1;

CREATE TABLE IF NOT EXISTS migration_0151_merge_guard (
    singleton      TINYINT NOT NULL PRIMARY KEY,
    conflict_count INT     NOT NULL,
    CONSTRAINT migration_0151_no_merge_conflicts CHECK (conflict_count = 0)
);
DELETE FROM migration_0151_merge_guard;
INSERT INTO migration_0151_merge_guard (singleton, conflict_count)
SELECT 1, COUNT(*) FROM migration_0151_merge_conflict;

-- ------------------------------------------------------------------------------------------------
-- 1) PLM / lab-dip columns onto product (all nullable — a pure catalog colourway has no dev process).
-- Regex CHECKs are intentionally omitted (write-path validation, same precedent as 0139's
-- care_instructions): the enum surface is enforced in Go. dev_code/dev_name/dev_hex/dev_comment are
-- the ex tech_card_colorway.code/name/hex/comment (R8 rename). Guarded on the sentinel dev_code.
-- ------------------------------------------------------------------------------------------------
SET @need_plm := (SELECT COUNT(*) = 0 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'product' AND COLUMN_NAME = 'dev_code');
SET @sql := IF(@need_plm,
    'ALTER TABLE product
        ADD COLUMN dev_code             VARCHAR(64)  NULL,
        ADD COLUMN dev_name             VARCHAR(255) NULL,
        ADD COLUMN lab_dip_status       VARCHAR(16)  NULL,
        ADD COLUMN dev_comment          TEXT         NULL,
        ADD COLUMN display_order        INT          NOT NULL DEFAULT 0,
        ADD COLUMN pantone              VARCHAR(64)  NULL,
        ADD COLUMN pantone_system       VARCHAR(8)   NULL,
        ADD COLUMN dev_hex              VARCHAR(7)   NULL,
        ADD COLUMN swatch_media_id      INT          NULL,
        ADD COLUMN lab_dip_round        INT          NULL,
        ADD COLUMN lab_dip_submitted_at DATE         NULL,
        ADD COLUMN lab_dip_decided_at   DATE         NULL,
        ADD COLUMN lab_dip_decided_by   VARCHAR(255) NULL,
        ADD COLUMN lab_dip_reject_reason TEXT        NULL',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

-- swatch_media_id FK (guarded), mirrors fk_tech_card_colorway_swatch (ON DELETE SET NULL).
SET @need_fk := (SELECT COUNT(*) = 0 FROM information_schema.TABLE_CONSTRAINTS
    WHERE CONSTRAINT_SCHEMA = DATABASE() AND TABLE_NAME = 'product'
      AND CONSTRAINT_NAME = 'fk_product_swatch_media');
SET @sql := IF(@need_fk,
    'ALTER TABLE product ADD CONSTRAINT fk_product_swatch_media
        FOREIGN KEY (swatch_media_id) REFERENCES media(id) ON DELETE SET NULL',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

-- ------------------------------------------------------------------------------------------------
-- 2) product.sku becomes nullable (NULL = unminted draft); the UNIQUE index stays (MySQL allows
-- multiple NULLs). thumbnail_id becomes nullable (a draft has no media yet; the admin read LEFT
-- JOINs media). Guarded on current nullability so a re-run is a no-op (avoids a needless rebuild).
-- ------------------------------------------------------------------------------------------------
SET @sku_notnull := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'product' AND COLUMN_NAME = 'sku' AND IS_NULLABLE = 'NO');
SET @sql := IF(@sku_notnull, 'ALTER TABLE product MODIFY COLUMN sku VARCHAR(64) NULL', 'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

SET @thumb_notnull := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'product' AND COLUMN_NAME = 'thumbnail_id' AND IS_NULLABLE = 'NO');
SET @sql := IF(@thumb_notnull, 'ALTER TABLE product MODIFY COLUMN thumbnail_id INT NULL', 'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

-- ------------------------------------------------------------------------------------------------
-- 3) A temporary back-reference column so a bulk INSERT ... SELECT of draft rows can correlate each
-- new product.id to the dev colourway it came from (for the merge_map ledger) and stay idempotent on
-- re-run. Dropped in 0152 once the ledger is complete.
-- ------------------------------------------------------------------------------------------------
SET @need_src := (SELECT COUNT(*) = 0 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'product' AND COLUMN_NAME = 'merge_src_colorway_id');
SET @sql := IF(@need_src, 'ALTER TABLE product ADD COLUMN merge_src_colorway_id INT NULL', 'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

-- ------------------------------------------------------------------------------------------------
-- 4) Persistent audit ledger (R1): legacy_id -> product_id, whether the product was linked or
-- freshly created, and a provenance checksum of the source colourway.
-- ------------------------------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS migration_colorway_merge_map (
    legacy_id       INT NOT NULL PRIMARY KEY,           -- tech_card_colorway.id
    product_id      INT NOT NULL,
    merge_kind      VARCHAR(16) NOT NULL,               -- 'linked' | 'draft_created'
    source_checksum CHAR(8) NOT NULL DEFAULT '',        -- CRC32 (hex) of the source PLM fields
    merged_at       TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE KEY uniq_merge_map_product (product_id)
);

-- ------------------------------------------------------------------------------------------------
-- 5) LINKED colourways (product_id IS NOT NULL): record provenance, then copy PLM onto the product.
-- color_code is NOT touched — product.color_code is already authoritative (0145), product wins (R1).
-- ------------------------------------------------------------------------------------------------
INSERT INTO migration_colorway_merge_map (legacy_id, product_id, merge_kind, source_checksum)
SELECT cw.id, cw.product_id, 'linked',
       LPAD(HEX(CRC32(CONCAT_WS('|', cw.id, COALESCE(cw.code,''), COALESCE(cw.name,''),
             COALESCE(cw.pantone,''), COALESCE(cw.hex,''), COALESCE(cw.lab_dip_status,'')))), 8, '0')
FROM tech_card_colorway cw
WHERE cw.product_id IS NOT NULL
  AND NOT EXISTS (SELECT 1 FROM migration_colorway_merge_map m WHERE m.legacy_id = cw.id);

UPDATE product p
JOIN tech_card_colorway cw ON cw.product_id = p.id
SET p.dev_code             = cw.code,
    p.dev_name             = cw.name,
    p.lab_dip_status       = cw.lab_dip_status,
    p.dev_comment          = cw.comment,
    p.display_order        = cw.display_order,
    p.pantone              = cw.pantone,
    p.pantone_system       = cw.pantone_system,
    p.dev_hex              = cw.hex,
    p.swatch_media_id      = cw.swatch_media_id,
    p.lab_dip_round        = cw.lab_dip_round,
    p.lab_dip_submitted_at = cw.lab_dip_submitted_at,
    p.lab_dip_decided_at   = cw.lab_dip_decided_at,
    p.lab_dip_decided_by   = cw.lab_dip_decided_by,
    p.lab_dip_reject_reason = cw.lab_dip_reject_reason;

-- ------------------------------------------------------------------------------------------------
-- 6) DEV colourways (product_id IS NULL): insert a DRAFT product (lifecycle_status = 1, sku NULL,
-- style_id = card, colour resolved from the dictionary, no media required), carrying its source id
-- so the ledger row can be recorded. Idempotent: skip colourways that already have a draft product.
-- ------------------------------------------------------------------------------------------------
INSERT INTO product
    (sku, style_id, preorder, color, color_code, color_hex, country_of_origin,
     thumbnail_id, secondary_thumbnail_id, sale_percentage, lifecycle_status, min_tier,
     dev_code, dev_name, lab_dip_status, dev_comment, display_order, pantone, pantone_system, dev_hex,
     swatch_media_id, lab_dip_round, lab_dip_submitted_at, lab_dip_decided_at, lab_dip_decided_by,
     lab_dip_reject_reason, merge_src_colorway_id)
SELECT
    NULL, cw.tech_card_id, NULL,
    (SELECT c.name FROM color c WHERE c.code = cw.color_code), cw.color_code, NULL, '',
    cw.swatch_media_id, NULL, 0, 1, 0,
    cw.code, cw.name, cw.lab_dip_status, cw.comment, cw.display_order, cw.pantone, cw.pantone_system, cw.hex,
    cw.swatch_media_id, cw.lab_dip_round, cw.lab_dip_submitted_at, cw.lab_dip_decided_at, cw.lab_dip_decided_by,
    cw.lab_dip_reject_reason, cw.id
FROM tech_card_colorway cw
WHERE cw.product_id IS NULL
  AND NOT EXISTS (SELECT 1 FROM product p WHERE p.merge_src_colorway_id = cw.id);

INSERT INTO migration_colorway_merge_map (legacy_id, product_id, merge_kind, source_checksum)
SELECT p.merge_src_colorway_id, p.id, 'draft_created',
       LPAD(HEX(CRC32(CONCAT_WS('|', cw.id, COALESCE(cw.code,''), COALESCE(cw.name,''),
             COALESCE(cw.pantone,''), COALESCE(cw.hex,''), COALESCE(cw.lab_dip_status,'')))), 8, '0')
FROM product p
JOIN tech_card_colorway cw ON cw.id = p.merge_src_colorway_id
WHERE p.merge_src_colorway_id IS NOT NULL
  AND NOT EXISTS (SELECT 1 FROM migration_colorway_merge_map m WHERE m.legacy_id = p.merge_src_colorway_id);

-- ------------------------------------------------------------------------------------------------
-- 7) Repoint every child that references tech_card_colorway(id) at product(id). The FK name is
-- resolved from information_schema (some are auto-named ibfk), and each block is a no-op once the FK
-- already points at product. Order: remap values with FK checks off, re-enable, then a single ALTER
-- drops the old FK and adds the new one — MySQL commits the value-remap together with that ALTER, so
-- a retry either sees the old FK (values still legacy, safe to redo) or the new FK (already done).
-- Live children (3): tech_card_colorway_usage (CASCADE), tech_card_piece_material (CASCADE),
-- sample (SET NULL). NOTE: tech_card_bom_colorway was dropped for good in 0079 (Up) — block 7b below
-- is kept only as a defensive information_schema-guarded no-op (its @old_fk resolves to NULL when the
-- table is absent), consistent with the rest of the chain; it repoints nothing on a clean apply.
-- ------------------------------------------------------------------------------------------------

-- 7a) tech_card_colorway_usage — the material recipe (costing). ON DELETE CASCADE.
SET @old_fk := (SELECT CONSTRAINT_NAME FROM information_schema.KEY_COLUMN_USAGE
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_colorway_usage'
      AND COLUMN_NAME = 'colorway_id' AND REFERENCED_TABLE_NAME = 'tech_card_colorway' LIMIT 1);
SET FOREIGN_KEY_CHECKS = 0;
SET @sql := IF(@old_fk IS NOT NULL,
    'UPDATE tech_card_colorway_usage c JOIN migration_colorway_merge_map m ON c.colorway_id = m.legacy_id SET c.colorway_id = m.product_id',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;
SET FOREIGN_KEY_CHECKS = 1;
SET @sql := IF(@old_fk IS NOT NULL,
    CONCAT('ALTER TABLE tech_card_colorway_usage DROP FOREIGN KEY ', @old_fk,
           ', ADD CONSTRAINT fk_tccu_colorway_product FOREIGN KEY (colorway_id) REFERENCES product(id) ON DELETE CASCADE'),
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

-- 7b) tech_card_bom_colorway — dropped for good in 0079 (Up). Guarded no-op: @old_fk is NULL when the
-- table is absent, so nothing runs on a clean chain. Kept defensively for any DB still carrying it.
SET @old_fk := (SELECT CONSTRAINT_NAME FROM information_schema.KEY_COLUMN_USAGE
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_bom_colorway'
      AND COLUMN_NAME = 'colorway_id' AND REFERENCED_TABLE_NAME = 'tech_card_colorway' LIMIT 1);
SET FOREIGN_KEY_CHECKS = 0;
SET @sql := IF(@old_fk IS NOT NULL,
    'UPDATE tech_card_bom_colorway c JOIN migration_colorway_merge_map m ON c.colorway_id = m.legacy_id SET c.colorway_id = m.product_id',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;
SET FOREIGN_KEY_CHECKS = 1;
SET @sql := IF(@old_fk IS NOT NULL,
    CONCAT('ALTER TABLE tech_card_bom_colorway DROP FOREIGN KEY ', @old_fk,
           ', ADD CONSTRAINT fk_tcbc_colorway_product FOREIGN KEY (colorway_id) REFERENCES product(id) ON DELETE CASCADE'),
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

-- 7c) tech_card_piece_material — per-piece fabric mapping (fk_tcpm_colorway). CASCADE.
SET @old_fk := (SELECT CONSTRAINT_NAME FROM information_schema.KEY_COLUMN_USAGE
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_piece_material'
      AND COLUMN_NAME = 'colorway_id' AND REFERENCED_TABLE_NAME = 'tech_card_colorway' LIMIT 1);
SET FOREIGN_KEY_CHECKS = 0;
SET @sql := IF(@old_fk IS NOT NULL,
    'UPDATE tech_card_piece_material c JOIN migration_colorway_merge_map m ON c.colorway_id = m.legacy_id SET c.colorway_id = m.product_id',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;
SET FOREIGN_KEY_CHECKS = 1;
SET @sql := IF(@old_fk IS NOT NULL,
    CONCAT('ALTER TABLE tech_card_piece_material DROP FOREIGN KEY ', @old_fk,
           ', ADD CONSTRAINT fk_tcpm_colorway_product FOREIGN KEY (colorway_id) REFERENCES product(id) ON DELETE CASCADE'),
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

-- 7d) sample — a physical sample optionally tied to a colourway (fk_sample_colorway). ON DELETE SET NULL.
SET @old_fk := (SELECT CONSTRAINT_NAME FROM information_schema.KEY_COLUMN_USAGE
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'sample'
      AND COLUMN_NAME = 'colorway_id' AND REFERENCED_TABLE_NAME = 'tech_card_colorway' LIMIT 1);
SET FOREIGN_KEY_CHECKS = 0;
SET @sql := IF(@old_fk IS NOT NULL,
    'UPDATE sample c JOIN migration_colorway_merge_map m ON c.colorway_id = m.legacy_id SET c.colorway_id = m.product_id',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;
SET FOREIGN_KEY_CHECKS = 1;
SET @sql := IF(@old_fk IS NOT NULL,
    CONCAT('ALTER TABLE sample DROP FOREIGN KEY ', @old_fk,
           ', ADD CONSTRAINT fk_sample_colorway_product FOREIGN KEY (colorway_id) REFERENCES product(id) ON DELETE SET NULL'),
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

-- 7e) Fail-fast: no OTHER table may still reference tech_card_colorway(id). If an unexpected child
-- exists, STOP (the report survives) rather than orphan it when 0152 drops the parent.
DELETE FROM migration_0151_merge_conflict WHERE conflict_type = 'unhandled_child_fk';
INSERT INTO migration_0151_merge_conflict (conflict_type, tech_card_id, colorway_id)
SELECT DISTINCT 'unhandled_child_fk', 0, 0
FROM information_schema.KEY_COLUMN_USAGE
WHERE CONSTRAINT_SCHEMA = DATABASE() AND REFERENCED_TABLE_NAME = 'tech_card_colorway'
  AND TABLE_NAME NOT IN ('tech_card_colorway_usage','tech_card_bom_colorway','tech_card_piece_material','sample');
UPDATE migration_0151_merge_guard SET conflict_count = (SELECT COUNT(*) FROM migration_0151_merge_conflict) WHERE singleton = 1;

-- ------------------------------------------------------------------------------------------------
-- 8) The post-merge catalog invariant: UNIQUE(style_id, color_code) on product. The old
-- uniq_colorway_card_color on tech_card_colorway is dropped (its guarantee now lives on product).
-- ------------------------------------------------------------------------------------------------
SET @need_uniq := (SELECT COUNT(*) = 0 FROM information_schema.STATISTICS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'product' AND INDEX_NAME = 'uniq_product_style_color');
SET @sql := IF(@need_uniq,
    'ALTER TABLE product ADD CONSTRAINT uniq_product_style_color UNIQUE (style_id, color_code)',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

SET @drop_old_uniq := (SELECT COUNT(*) FROM information_schema.STATISTICS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_colorway' AND INDEX_NAME = 'uniq_colorway_card_color');
SET @sql := IF(@drop_old_uniq > 0, 'ALTER TABLE tech_card_colorway DROP INDEX uniq_colorway_card_color', 'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

-- +migrate Down
-- One-way big-bang merge (R1/R10): rollback is restore-from-backup, not a Down. Intentionally empty.
