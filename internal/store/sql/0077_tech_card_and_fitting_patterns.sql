-- +migrate Up
-- PDF выкройки (cut patterns) for tech cards and fittings.
--
-- A tech card stores the FINAL pattern per size (tech_card_size_pattern); a fitting
-- stores the ITERATION pattern(s) actually tried on (fitting_pattern). Both reference a
-- raw PDF in object storage by url (uploaded via Admin.UploadPattern) — the binary is
-- NOT kept in the `media` table, which is image/video-shaped (full/compressed/thumbnail
-- + dimensions + blurhash) and backs the image library. A size can carry several
-- patterns (pieces split across sheets), so neither table is UNIQUE on (parent, size).

CREATE TABLE tech_card_size_pattern (
  id INT PRIMARY KEY AUTO_INCREMENT,
  tech_card_id INT NOT NULL,
  size_id INT NOT NULL COMMENT 'FK size(id); the graded size this выкройка is for',
  url VARCHAR(1024) NOT NULL COMMENT 'CDN url of the uploaded PDF',
  filename VARCHAR(255) NULL COMMENT 'original filename for display / download',
  size_bytes BIGINT NULL COMMENT 'stored file size in bytes',
  content_type VARCHAR(64) NOT NULL DEFAULT 'application/pdf',
  display_order INT NOT NULL DEFAULT 0,
  created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  INDEX idx_tech_card_size_pattern_card_size (tech_card_id, size_id),
  FOREIGN KEY (tech_card_id) REFERENCES tech_card(id) ON DELETE CASCADE,
  FOREIGN KEY (size_id) REFERENCES size(id)
) COMMENT 'Final PDF cut patterns (выкройки) per tech-card size';

CREATE TABLE fitting_pattern (
  id INT PRIMARY KEY AUTO_INCREMENT,
  fitting_id INT NOT NULL,
  size_id INT NULL COMMENT 'FK size(id); NULL = unset (which size this iteration is for)',
  url VARCHAR(1024) NOT NULL COMMENT 'CDN url of the uploaded PDF',
  filename VARCHAR(255) NULL COMMENT 'original filename for display / download',
  size_bytes BIGINT NULL COMMENT 'stored file size in bytes',
  content_type VARCHAR(64) NOT NULL DEFAULT 'application/pdf',
  display_order INT NOT NULL DEFAULT 0,
  created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  INDEX idx_fitting_pattern_fitting (fitting_id),
  FOREIGN KEY (fitting_id) REFERENCES fitting(id) ON DELETE CASCADE,
  FOREIGN KEY (size_id) REFERENCES size(id)
) COMMENT 'PDF cut-pattern iterations measured in a fitting';

-- +migrate Down
DROP TABLE fitting_pattern;
DROP TABLE tech_card_size_pattern;
