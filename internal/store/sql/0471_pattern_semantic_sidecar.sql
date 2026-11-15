-- +migrate Up

-- Browser-derived DXF semantics are attached only after the DXF row has been saved and reread.
-- The payload lives in MySQL for the beta path so bytes + both digests commit atomically; putting a
-- second object in S3 would add an uncloseable upload/DB two-phase seam and orphan cleanup.
CREATE TABLE IF NOT EXISTS tech_card_pattern_semantic_sidecar (
  pattern_row_id INT PRIMARY KEY,
  card_lock_version INT NOT NULL,
  saved_row_revision INT UNSIGNED NOT NULL,
  dxf_sha256 CHAR(64) NOT NULL,
  pattern_version INT NOT NULL,
  sidecar_sha256 CHAR(64) NOT NULL,
  media_type VARCHAR(96) NOT NULL,
  byte_length INT UNSIGNED NOT NULL,
  payload MEDIUMBLOB NOT NULL,
  post_save_receipt_sha256 CHAR(64) NOT NULL,
  created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
  created_by VARCHAR(255) NOT NULL,
  CONSTRAINT fk_pattern_semantic_sidecar_row
    FOREIGN KEY (pattern_row_id) REFERENCES tech_card_size_pattern(id) ON DELETE CASCADE,
  CONSTRAINT chk_pattern_semantic_sidecar_version CHECK (pattern_version > 0),
  CONSTRAINT chk_pattern_semantic_sidecar_saved_row_revision CHECK (saved_row_revision > 0),
  CONSTRAINT chk_pattern_semantic_sidecar_card_lock CHECK (card_lock_version >= 0),
  CONSTRAINT chk_pattern_semantic_sidecar_length CHECK (byte_length BETWEEN 1 AND 4194304),
  CONSTRAINT chk_pattern_semantic_sidecar_media_type CHECK (
    media_type = 'application/vnd.grbpwr.pattern-dxf-semantics.v1+json'
  )
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4
  COMMENT 'Canonical browser-derived DXF seam semantics bound to one active pattern revision';

-- +migrate Down

DROP TABLE IF EXISTS tech_card_pattern_semantic_sidecar;

