-- +migrate Up
-- X11: each team's file storage in use and its cap. used_bytes is counted at upload / delete and
-- recounted daily from the bucket; cap_bytes NULL = the default (TEAM_STORAGE_CAP_GB), 0 = no cap,
-- > 0 = this team's cap. A new small table: additive, online, idempotent, ANSI_QUOTES-safe.
-- Team #1 is counted but starts with no cap (cap_bytes = 0) until the owner sets one; a re-run
-- never overwrites a cap the operator set.

CREATE TABLE IF NOT EXISTS team_storage (
  team_id BIGINT NOT NULL,
  used_bytes BIGINT NOT NULL DEFAULT 0,
  cap_bytes BIGINT NULL,
  recounted_at DATETIME(3) NULL,
  updated_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  PRIMARY KEY (team_id)
);

INSERT IGNORE INTO team_storage (team_id) SELECT id FROM team;

INSERT INTO team_storage (team_id, cap_bytes) VALUES (1, 0)
  ON DUPLICATE KEY UPDATE cap_bytes = COALESCE(cap_bytes, 0);

-- +migrate Down
-- Never dropped: a rollback binary ignores the table (as 0001-0013).
