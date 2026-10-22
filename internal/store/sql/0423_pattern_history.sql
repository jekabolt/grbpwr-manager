-- +migrate Up

-- The active sheet keeps its stable line key while the object URL changes. Its digest is optional
-- and presence-aware in the write contract, so legacy clients preserve it.
SET @pattern_sha_col_exists := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_size_pattern' AND COLUMN_NAME = 'sha256');
SET @ddl := IF(@pattern_sha_col_exists = 0,
    'ALTER TABLE tech_card_size_pattern ADD COLUMN sha256 CHAR(64) NULL COMMENT ''lowercase SHA-256 of the uploaded object bytes'' AFTER size_bytes',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- One row per replacement or removal event. There is deliberately no uniqueness constraint because
-- restoring an older object and replacing it again is another real event. The card owns retention.
CREATE TABLE IF NOT EXISTS tech_card_size_pattern_history (
  id BIGINT PRIMARY KEY AUTO_INCREMENT,
  tech_card_id INT NOT NULL,
  line_key CHAR(26) NULL,
  scope_key VARCHAR(64) NULL,
  size_id INT NULL,
  version INT NOT NULL DEFAULT 0,
  url VARCHAR(1024) NOT NULL,
  filename VARCHAR(255) NULL,
  size_bytes BIGINT NULL,
  sha256 CHAR(64) NULL,
  uploaded_at TIMESTAMP NULL,
  replaced_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
  replaced_by VARCHAR(255) NULL,
  reason VARCHAR(255) NULL,
  CONSTRAINT fk_tc_pattern_history_card FOREIGN KEY (tech_card_id) REFERENCES tech_card(id) ON DELETE CASCADE,
  INDEX idx_tc_pattern_history_card_scope_version (tech_card_id, scope_key, size_id, version),
  INDEX idx_tc_pattern_history_card_line (tech_card_id, line_key)
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4 COMMENT 'Append-only history of replaced and removed tech-card pattern sheets';

-- +migrate Down

DROP TABLE IF EXISTS tech_card_size_pattern_history;

SET @pattern_sha_col_exists := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_size_pattern' AND COLUMN_NAME = 'sha256');
SET @ddl := IF(@pattern_sha_col_exists = 1,
    'ALTER TABLE tech_card_size_pattern DROP COLUMN sha256',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;
