-- +migrate Up
-- PLM-rework WS3 / S15: type material attributes via class-table-inheritance. A flat `material`
-- carried fabric-only columns (fabric_width, fabric_weight_gsm) that are meaningless on a thread or
-- a buckle. Give the material a `material_class` discriminant + four typed 1:1 side-tables
-- (fabric/hardware/thread/packaging), an `other_attrs` JSON escape-hatch for the unclassifiable
-- 'other' class, and username audit stamps. Long-tail types map to the nearest of the four at
-- backfill (leather/interfacing -> fabric; metal fittings/buckles -> hardware); they get no
-- side-table of their own. The flat fabric_* columns move into material_fabric_attr here; the base
-- columns are dropped later (M3) behind a reconciliation guard, NOT in this additive migration.
--
-- Additive + crash-idempotent: MySQL 8 has no ADD COLUMN IF NOT EXISTS, so the base add is guarded
-- on information_schema (one atomic ALTER — all columns + the named CHECK, or none); side-tables use
-- IF-NOT-EXISTS guards; backfills are re-runnable. A mid-file failure re-runs from the top.

-- --- base: discriminant + escape-hatch + audit (guarded on material_class) ---
SET @need := (SELECT COUNT(*) = 0 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'material' AND COLUMN_NAME = 'material_class');
SET @sql := IF(@need,
    'ALTER TABLE material
        ADD COLUMN material_class VARCHAR(16) NOT NULL DEFAULT ''other'',
        ADD COLUMN other_attrs JSON NULL,
        ADD COLUMN created_by VARCHAR(255) NOT NULL DEFAULT '''',
        ADD COLUMN updated_by VARCHAR(255) NOT NULL DEFAULT '''',
        ADD CONSTRAINT chk_material_class CHECK (material_class REGEXP ''^(fabric|hardware|thread|packaging|other)$'')',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

-- --- backfill material_class from the existing free-text section (only rows still at the default,
--     so a crash-retry re-derives the same values without clobbering) ---
UPDATE material SET material_class = CASE
        WHEN section IN ('fabric', 'lining', 'interlining', 'insulation') THEN 'fabric'
        WHEN section IN ('hardware', 'trim', 'decoration') THEN 'hardware'
        WHEN section = 'thread' THEN 'thread'
        WHEN section IN ('label', 'packaging') THEN 'packaging'
        ELSE 'other'
    END
    WHERE material_class = 'other';

-- --- four typed side-tables (1:1 on material_class; material_id PK = FK CASCADE) ---
CREATE TABLE IF NOT EXISTS material_fabric_attr (
    material_id INT PRIMARY KEY,
    width_cm DECIMAL(7, 2) NULL CHECK (width_cm IS NULL OR width_cm >= 0),
    weight_gsm DECIMAL(7, 2) NULL CHECK (weight_gsm IS NULL OR weight_gsm >= 0),
    fabric_direction VARCHAR(16) NULL
        CHECK (fabric_direction IS NULL OR fabric_direction REGEXP '^(lengthwise|crosswise|any)$'),
    shrinkage_pct DECIMAL(5, 2) NULL,
    roll_length_m DECIMAL(10, 2) NULL,
    FOREIGN KEY (material_id) REFERENCES material(id) ON DELETE CASCADE
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4 COMMENT 'Typed attributes for fabric-class materials (CTI)';

CREATE TABLE IF NOT EXISTS material_hardware_attr (
    material_id INT PRIMARY KEY,
    diameter_mm DECIMAL(7, 2) NULL CHECK (diameter_mm IS NULL OR diameter_mm >= 0),
    dimensions VARCHAR(64) NULL,
    finish VARCHAR(64) NULL,
    base_material VARCHAR(64) NULL,
    weight_g DECIMAL(8, 3) NULL CHECK (weight_g IS NULL OR weight_g >= 0),
    FOREIGN KEY (material_id) REFERENCES material(id) ON DELETE CASCADE
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4 COMMENT 'Typed attributes for hardware-class materials (CTI)';

CREATE TABLE IF NOT EXISTS material_thread_attr (
    material_id INT PRIMARY KEY,
    ticket_tex VARCHAR(16) NULL,
    length_per_cone_m DECIMAL(10, 2) NULL CHECK (length_per_cone_m IS NULL OR length_per_cone_m >= 0),
    needle_reco VARCHAR(32) NULL,
    FOREIGN KEY (material_id) REFERENCES material(id) ON DELETE CASCADE
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4 COMMENT 'Typed attributes for thread-class materials (CTI); fibre composition lives in material_composition';

CREATE TABLE IF NOT EXISTS material_packaging_attr (
    material_id INT PRIMARY KEY,
    substrate VARCHAR(64) NULL,
    dimensions VARCHAR(64) NULL,
    gsm DECIMAL(7, 2) NULL CHECK (gsm IS NULL OR gsm >= 0),
    print_method VARCHAR(32) NULL,
    FOREIGN KEY (material_id) REFERENCES material(id) ON DELETE CASCADE
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4 COMMENT 'Typed attributes for packaging-class materials (CTI)';

-- --- backfill the fabric side-table from the flat fabric_* columns (idempotent upsert) ---
INSERT INTO material_fabric_attr (material_id, width_cm, weight_gsm)
SELECT id, fabric_width, fabric_weight_gsm
    FROM material
    WHERE material_class = 'fabric' AND (fabric_width IS NOT NULL OR fabric_weight_gsm IS NOT NULL)
ON DUPLICATE KEY UPDATE width_cm = VALUES(width_cm), weight_gsm = VALUES(weight_gsm);

-- +migrate Down
DROP TABLE IF EXISTS material_packaging_attr;
DROP TABLE IF EXISTS material_thread_attr;
DROP TABLE IF EXISTS material_hardware_attr;
DROP TABLE IF EXISTS material_fabric_attr;
ALTER TABLE material
    DROP CONSTRAINT chk_material_class,
    DROP COLUMN updated_by,
    DROP COLUMN created_by,
    DROP COLUMN other_attrs,
    DROP COLUMN material_class;
