-- +migrate Up

-- A pattern row needs an identity clock distinct from the human-facing pattern version. The latter
-- may be pinned/restored and therefore cannot close ABA for a byte-read authority receipt.
SET @has_saved_row_revision := (SELECT COUNT(*) FROM information_schema.COLUMNS
  WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_size_pattern' AND COLUMN_NAME = 'saved_row_revision');
SET @ddl := IF(@has_saved_row_revision = 0,
  'ALTER TABLE tech_card_size_pattern ADD COLUMN saved_row_revision INT UNSIGNED NOT NULL DEFAULT 1 AFTER version',
  'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- Assembly is split across full-replace operations and independently-written seam rows, so neither
-- tech_card.lock_version nor a section digest is its clock. lineage_id prevents an ABA if a card id
-- is restored/reseeded; revision is bumped by every authoritative assembly writer.
CREATE TABLE IF NOT EXISTS tech_card_assembly_clock (
  tech_card_id INT PRIMARY KEY,
  lineage_id CHAR(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  revision BIGINT UNSIGNED NOT NULL DEFAULT 1,
  updated_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6),
  CONSTRAINT fk_tech_card_assembly_clock_card FOREIGN KEY (tech_card_id) REFERENCES tech_card(id) ON DELETE CASCADE,
  CONSTRAINT uq_tech_card_assembly_clock_lineage UNIQUE (lineage_id),
  CONSTRAINT chk_tech_card_assembly_clock_revision CHECK (revision > 0)
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4 COMMENT 'Monotonic authority clock for operations plus seam rows';

INSERT IGNORE INTO tech_card_assembly_clock (tech_card_id, lineage_id, revision)
SELECT id, LOWER(REPLACE(UUID(), '-', '')), 1 FROM tech_card;

-- +migrate Down

DROP TABLE IF EXISTS tech_card_assembly_clock;

SET @has_saved_row_revision := (SELECT COUNT(*) FROM information_schema.COLUMNS
  WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_size_pattern' AND COLUMN_NAME = 'saved_row_revision');
SET @ddl := IF(@has_saved_row_revision = 1,
  'ALTER TABLE tech_card_size_pattern DROP COLUMN saved_row_revision',
  'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;
