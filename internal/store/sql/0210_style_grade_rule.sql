-- +migrate Up

-- Persists the GRADE RULE behind a style's size chart (PLM UI gap 4).
--
-- The chart (tech_card_size_measurement) stores the fully expanded sizes x measurements grid. That is
-- what the factory reads, and it stays the source of truth. But it is not how a grader AUTHORS the
-- grid: 5 sizes x 8 measurements is 40 numbers, 35 of which are derived from the other 5 by
--
--     value(size) = base + step x (position(size) - position(base))
--
-- Until now the base size and the per-measurement step existed only in the admin's browser session:
-- the RPC received the expanded grid and the rule evaporated, so reopening a card showed 40 hand-typed
-- looking numbers with no rule attached. This migration gives the rule a home.
--
-- TWO PIECES, deliberately in different places:
--   * tech_card.grade_base_size_id -- one base per style, so a column on the style row. NULL = no rule
--     authored (the client then falls back to its own middle-of-the-range default, as today).
--   * tech_card_grade_rule -- one step per measurement, so a child table keyed by measurement_name_id.
--
-- NO per-cell "overridden" flag, on purpose. Whether a cell follows the rule is DERIVABLE: compare the
-- stored value against base + step x delta. A cell that differs was overtyped; a cell that matches is
-- derived. Storing the flag as well would create a second source of truth that the OTHER writer of
-- this table (internal/store/product replaceStyleChart, the colourway measurement editor) would
-- silently desynchronise on every save. Derivation cannot desynchronise.
--
-- ON DELETE CASCADE on the card: the rule is part of the style's chart, and a deleted style takes it.
-- ON DELETE RESTRICT on the size (grade_base_size_id) and on measurement_name: mirrors
-- fk_tcsm_size_restrict / fk_tcsm_name from 0141+0149 -- a dictionary row a style still references
-- must not vanish underneath it.
--
-- Idempotent: the column add is guarded by an information_schema check (MySQL 8 has no
-- ADD COLUMN IF NOT EXISTS), and the table is created only when absent. Re-running is a no-op.

CREATE TABLE IF NOT EXISTS tech_card_grade_rule (
    id                  INT PRIMARY KEY AUTO_INCREMENT,
    tech_card_id        INT NOT NULL COMMENT 'FK tech_card(id): the style the rule belongs to',
    measurement_name_id INT NOT NULL COMMENT 'FK measurement_name(id): which measurement this step grades',
    step                DECIMAL(10, 2) NOT NULL COMMENT 'increment per size position; may be negative',
    CONSTRAINT fk_tcgr_card FOREIGN KEY (tech_card_id) REFERENCES tech_card(id) ON DELETE CASCADE,
    CONSTRAINT fk_tcgr_name FOREIGN KEY (measurement_name_id) REFERENCES measurement_name(id) ON DELETE RESTRICT,
    CONSTRAINT uniq_tcgr_card_name UNIQUE (tech_card_id, measurement_name_id)
) ENGINE = InnoDB COMMENT 'Per-measurement grade step of a style size chart (authoring rule behind tech_card_size_measurement)';

SET @col_exists := (
    SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'tech_card'
      AND COLUMN_NAME = 'grade_base_size_id'
);
SET @ddl := IF(@col_exists = 0,
    'ALTER TABLE tech_card ADD COLUMN grade_base_size_id INT NULL COMMENT ''FK size(id): size the grade rule radiates from; NULL = no rule authored''',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @fk_exists := (
    SELECT COUNT(*) FROM information_schema.TABLE_CONSTRAINTS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'tech_card'
      AND CONSTRAINT_NAME = 'fk_tech_card_grade_base_size'
);
SET @ddl := IF(@fk_exists = 0,
    'ALTER TABLE tech_card ADD CONSTRAINT fk_tech_card_grade_base_size FOREIGN KEY (grade_base_size_id) REFERENCES size(id) ON DELETE RESTRICT',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- +migrate Down
DROP TABLE IF EXISTS tech_card_grade_rule;

SET @fk_exists := (
    SELECT COUNT(*) FROM information_schema.TABLE_CONSTRAINTS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'tech_card'
      AND CONSTRAINT_NAME = 'fk_tech_card_grade_base_size'
);
SET @ddl := IF(@fk_exists = 1,
    'ALTER TABLE tech_card DROP FOREIGN KEY fk_tech_card_grade_base_size',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @col_exists := (
    SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'tech_card'
      AND COLUMN_NAME = 'grade_base_size_id'
);
SET @ddl := IF(@col_exists = 1,
    'ALTER TABLE tech_card DROP COLUMN grade_base_size_id',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;
