-- +migrate Up

-- The mannequin editor pose is a resource of its own. It is deliberately not a
-- column of model: UpdateModel is a full replacement used by older clients and
-- must never erase or overwrite a pose edited in another tab.
--
-- payload is not an opaque client blob. The server validates a typed V2 value,
-- emits the canonical JSON itself, and verifies its digest on every read/write.
-- The duplicated schema/space columns make drift visible without trusting JSON.
CREATE TABLE IF NOT EXISTS model_mannequin_pose (
  model_id INT NOT NULL,
  schema_version SMALLINT UNSIGNED NOT NULL,
  pose_space VARCHAR(80) COLLATE utf8mb4_bin NOT NULL,
  payload JSON NOT NULL,
  canonical_sha256 CHAR(64) COLLATE utf8mb4_bin NOT NULL,
  revision BIGINT UNSIGNED NOT NULL DEFAULT 1,
  updated_at TIMESTAMP(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6),
  updated_by VARCHAR(255) NOT NULL,
  PRIMARY KEY (model_id),
  CONSTRAINT fk_model_mannequin_pose_model
    FOREIGN KEY (model_id) REFERENCES model(id) ON DELETE CASCADE,
  CONSTRAINT chk_model_mannequin_pose_schema CHECK (schema_version = 2),
  CONSTRAINT chk_model_mannequin_pose_space CHECK (
    pose_space = 'makehuman-open-reference/direct-pose-v9'
  ),
  CONSTRAINT chk_model_mannequin_pose_hash CHECK (
    canonical_sha256 REGEXP '^[0-9a-f]{64}$'
    AND STRCMP(CAST(canonical_sha256 AS BINARY), CAST(LOWER(canonical_sha256) AS BINARY)) = 0
  ),
  CONSTRAINT chk_model_mannequin_pose_revision CHECK (revision > 0),
  CONSTRAINT chk_model_mannequin_pose_actor CHECK (CHAR_LENGTH(updated_by) BETWEEN 1 AND 255)
) ENGINE=InnoDB COMMENT 'Versioned direct-pose-v9 mannequin pose, one row per fit model';

-- +migrate Down

DROP TABLE IF EXISTS model_mannequin_pose;
