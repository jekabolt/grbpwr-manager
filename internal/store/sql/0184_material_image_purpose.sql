-- +migrate Up
-- Catalog-hardening: give a catalog material an optional image (#39) and a purpose/mark that
-- distinguishes sample vs production materials (#40, so the admin can mark and filter).
--
-- The warehouse `code` column already exists (0106) and is intentionally NOT made globally UNIQUE
-- here: it must stay unique only among NON-ARCHIVED rows (an archived material may keep its code, and
-- a new material may reuse a freed one), a range the app holds inside a SERIALIZABLE transaction
-- (checkMaterialCodeFree). A plain UNIQUE(code) would both break that reuse and risk failing this
-- migration on any pre-existing duplicate (archived+active) prod row. Auto-generated codes (#68)
-- derive from the row's own auto-increment id, so they are unique by construction and need no schema
-- change; the existing idx_material_code stays a plain lookup index.
--
-- Additive + crash-idempotent: MySQL 8 has no ADD COLUMN IF NOT EXISTS, so both columns + the named
-- CHECK + the image FK are added in ONE atomic ALTER guarded on information_schema (all-or-none, so a
-- retried partial apply re-runs the whole ALTER); the backfill is re-runnable. ANSI_QUOTES is ON on
-- the managed cluster, so every string literal is single-quoted (doubled inside the dynamic SQL).

-- --- material: image_id (FK media, ON DELETE SET NULL) + purpose (guarded on image_id) ---
SET @need := (SELECT COUNT(*) = 0 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'material' AND COLUMN_NAME = 'image_id');
SET @sql := IF(@need,
    'ALTER TABLE material
        ADD COLUMN image_id INT NULL COMMENT ''optional catalog image; FK media(id) (#39)'',
        ADD COLUMN purpose VARCHAR(16) NOT NULL DEFAULT ''both''
            COMMENT ''sample | production | both (#40)'',
        ADD CONSTRAINT chk_material_purpose CHECK (purpose REGEXP ''^(sample|production|both)$''),
        ADD CONSTRAINT fk_material_image FOREIGN KEY (image_id) REFERENCES media(id) ON DELETE SET NULL',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

-- --- backfill purpose for existing rows (idempotent; a no-op once every row is a valid value).
--     The ADD COLUMN DEFAULT already stamps existing rows 'both'; this makes the backfill explicit
--     and self-heals any pre-existing out-of-range value before the CHECK would matter on re-run. ---
UPDATE material SET purpose = 'both'
    WHERE purpose NOT REGEXP '^(sample|production|both)$';

-- +migrate Down
ALTER TABLE material
    DROP FOREIGN KEY fk_material_image,
    DROP CONSTRAINT chk_material_purpose,
    DROP COLUMN purpose,
    DROP COLUMN image_id;
