-- +migrate Up

-- SKU redesign task 02: give each size a short 2-digit numeric ordinal for the size segment of the
-- variant SKU (SS26-00021-BLK-04), ordered by physical size WITHIN a size system. sku_system groups
-- the size families explicitly (apparel / shoe / composite_ta / composite_bo) — a product uses exactly
-- one system, so ordinals only need to be distinct and monotonic within a system; overlap across
-- systems is harmless (different model+colour => different base SKU). Seeded WITH GAPS so future sizes
-- can be inserted between existing ones without renumbering (ordinals are frozen into shipped SKUs).
--
-- Idempotent: guarded ADD COLUMN via information_schema (multi-line PREPARE/EXECUTE/DEALLOCATE — a
-- single line trips 1064 on the managed DSN, see 0124); the seed UPDATEs are keyed by name and set
-- absolute values, so a re-run is a no-op.

-- 1) Columns: sku_ord (2-digit ordinal, rendered LPAD(sku_ord,2,'0')) and sku_system (family).
-- sku_system is nullable (filled by the seed UPDATEs below) — this avoids escaping an empty-string
-- DEFAULT inside the dynamic ALTER; an unseeded/unknown size stays NULL and the generator refuses
-- sku_ord=0 rather than emitting a bogus "-00" segment.
SET @need_cols := (SELECT COUNT(*) = 0 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'size' AND COLUMN_NAME = 'sku_ord');
SET @sql := IF(@need_cols,
    'ALTER TABLE size
        ADD COLUMN sku_ord    SMALLINT    NOT NULL DEFAULT 0,
        ADD COLUMN sku_system VARCHAR(16) NULL',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

-- 2) Seed apparel (letters): gaps of 5, OS lowest.
UPDATE size SET sku_system = 'apparel', sku_ord = CASE name
        WHEN 'os'  THEN 5
        WHEN 'xxs' THEN 10
        WHEN 'xs'  THEN 15
        WHEN 's'   THEN 20
        WHEN 'm'   THEN 25
        WHEN 'l'   THEN 30
        WHEN 'xl'  THEN 35
        WHEN 'xxl' THEN 40
    END
    WHERE name IN ('os','xxs','xs','s','m','l','xl','xxl');

-- 3) Seed shoe (EU 35..48 incl .5): 35=50, each half-step +1, 48=76 (fits 2 digits).
UPDATE size
    SET sku_system = 'shoe',
        sku_ord    = 50 + CAST((CAST(name AS DECIMAL(4,1)) - 35) * 2 AS SIGNED)
    WHERE name REGEXP '^[0-9]+(\\.5)?$';

-- 4) Seed composite tailored (composite_ta): women then men, gaps of 5, distinct within the system.
UPDATE size SET sku_system = 'composite_ta', sku_ord = CASE name
        WHEN 'xxs_32ta_f' THEN 10
        WHEN 'xs_34ta_f'  THEN 15
        WHEN 's_36ta_f'   THEN 20
        WHEN 'm_38ta_f'   THEN 25
        WHEN 'l_40ta_f'   THEN 30
        WHEN 'xl_42ta_f'  THEN 35
        WHEN 'xxl_44ta_f' THEN 40
        WHEN 'xs_44ta_m'  THEN 45
        WHEN 's_46ta_m'   THEN 50
        WHEN 'm_48ta_m'   THEN 55
        WHEN 'l_50ta_m'   THEN 60
        WHEN 'xl_52ta_m'  THEN 65
        WHEN 'xxl_54ta_m' THEN 70
    END
    WHERE name LIKE '%ta\_%';

-- 5) Seed composite bottoms (composite_bo): women then men, gaps of 5, distinct within the system.
UPDATE size SET sku_system = 'composite_bo', sku_ord = CASE name
        WHEN 'xxs_23bo_f' THEN 10
        WHEN 'xs_25bo_f'  THEN 15
        WHEN 's_27bo_f'   THEN 20
        WHEN 'm_29bo_f'   THEN 25
        WHEN 'l_31bo_f'   THEN 30
        WHEN 'xl_33bo_f'  THEN 35
        WHEN 'xxs_26bo_m' THEN 40
        WHEN 'xs_28bo_m'  THEN 45
        WHEN 's_30bo_m'   THEN 50
        WHEN 'm_32bo_m'   THEN 55
        WHEN 'l_34bo_m'   THEN 60
        WHEN 'xl_36bo_m'  THEN 65
        WHEN 'xxl_38bo_m' THEN 70
    END
    WHERE name LIKE '%bo\_%';
