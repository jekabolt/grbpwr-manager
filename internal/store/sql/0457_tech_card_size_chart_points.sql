-- +migrate Up

-- A real point-of-measure chart over the existing size x measurement grid. Existing cells are
-- finished-garment centimetres; source records whether the number was typed or copied from pattern
-- geometry. The point row owns presentation order, tolerance, shop visibility and pattern provenance.
--
-- The new source CHECK is safe for existing rows because the added column is NOT NULL DEFAULT
-- 'manual', so every pre-existing row satisfies it before the constraint is added.
-- migrationlint:allow-retroactive source is backfilled by its manual default before the named CHECK

SET @tcsm_source_col := (
    SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'tech_card_size_measurement'
      AND COLUMN_NAME = 'source'
);
SET @ddl := IF(@tcsm_source_col = 0,
    'ALTER TABLE tech_card_size_measurement ADD COLUMN source VARCHAR(8) NOT NULL DEFAULT ''manual''',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @tcsm_source_check := (
    SELECT COUNT(*) FROM information_schema.TABLE_CONSTRAINTS
    WHERE CONSTRAINT_SCHEMA = DATABASE()
      AND TABLE_NAME = 'tech_card_size_measurement'
      AND CONSTRAINT_NAME = 'chk_tcsm_source'
);
SET @ddl := IF(@tcsm_source_check = 0,
    'ALTER TABLE tech_card_size_measurement ADD CONSTRAINT chk_tcsm_source CHECK (source IN (''manual'', ''pattern''))',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @shrinkage_col := (
    SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'tech_card'
      AND COLUMN_NAME = 'size_chart_shrinkage_pct'
);
SET @ddl := IF(@shrinkage_col = 0,
    'ALTER TABLE tech_card ADD COLUMN size_chart_shrinkage_pct DECIMAL(5,2) NULL',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

CREATE TABLE IF NOT EXISTS tech_card_size_chart_point (
    id INT PRIMARY KEY AUTO_INCREMENT,
    tech_card_id INT NOT NULL,
    measurement_name_id INT NOT NULL,
    pom_code VARCHAR(32) NOT NULL DEFAULT '',
    position SMALLINT NOT NULL,
    tol_minus_cm DECIMAL(5,2) NOT NULL DEFAULT 1.00,
    tol_plus_cm DECIMAL(5,2) NOT NULL DEFAULT 1.00,
    in_shop TINYINT(1) NOT NULL DEFAULT 1,
    basis_pieces JSON NOT NULL,
    source_fingerprint CHAR(64) NOT NULL DEFAULT '',
    CONSTRAINT uniq_tcscp_card_name UNIQUE (tech_card_id, measurement_name_id),
    CONSTRAINT fk_tcscp_card FOREIGN KEY (tech_card_id) REFERENCES tech_card(id) ON DELETE CASCADE,
    CONSTRAINT fk_tcscp_name FOREIGN KEY (measurement_name_id) REFERENCES measurement_name(id) ON DELETE RESTRICT,
    INDEX idx_tcscp_card (tech_card_id)
) ENGINE=InnoDB COMMENT 'Point-of-measure metadata for the style size chart';

INSERT IGNORE INTO measurement_name (name) VALUES
    ('length-cb'),
    ('shoulder-length'),
    ('neck-width'),
    ('neck-drop-front'),
    ('neck-drop-back'),
    ('armhole'),
    ('underarm'),
    ('bicep'),
    ('sleeve-opening'),
    ('collar-length'),
    ('stand-height'),
    ('collar-point'),
    ('back-rise'),
    ('thigh'),
    ('knee');

INSERT IGNORE INTO tech_card_size_chart_point
    (tech_card_id, measurement_name_id, pom_code, position, tol_minus_cm, tol_plus_cm,
     in_shop, basis_pieces, source_fingerprint)
SELECT q.tech_card_id, q.measurement_name_id, '',
       ROW_NUMBER() OVER (PARTITION BY q.tech_card_id ORDER BY q.measurement_name_id),
       1.00, 1.00, 1, JSON_ARRAY(), ''
FROM (
    SELECT tech_card_id, measurement_name_id FROM tech_card_size_measurement
    UNION
    SELECT tech_card_id, measurement_name_id FROM tech_card_grade_rule
) q;

-- +migrate Down

DROP TABLE IF EXISTS tech_card_size_chart_point;

SET @tcsm_source_check_down := (
    SELECT COUNT(*) FROM information_schema.TABLE_CONSTRAINTS
    WHERE CONSTRAINT_SCHEMA = DATABASE()
      AND TABLE_NAME = 'tech_card_size_measurement'
      AND CONSTRAINT_NAME = 'chk_tcsm_source'
);
SET @ddl := IF(@tcsm_source_check_down = 1,
    'ALTER TABLE tech_card_size_measurement DROP CHECK chk_tcsm_source',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @tcsm_source_col_down := (
    SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'tech_card_size_measurement'
      AND COLUMN_NAME = 'source'
);
SET @ddl := IF(@tcsm_source_col_down = 1,
    'ALTER TABLE tech_card_size_measurement DROP COLUMN source',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @shrinkage_col_down := (
    SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'tech_card'
      AND COLUMN_NAME = 'size_chart_shrinkage_pct'
);
SET @ddl := IF(@shrinkage_col_down = 1,
    'ALTER TABLE tech_card DROP COLUMN size_chart_shrinkage_pct',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;
