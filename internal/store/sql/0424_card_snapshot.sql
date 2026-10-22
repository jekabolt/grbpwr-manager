-- +migrate Up

-- A requested snapshot freezes the pre-update contract in the same transaction as UpdateTechCard.
-- It is append-only and card-owned. Restore is intentionally a later RPC.
CREATE TABLE IF NOT EXISTS tech_card_snapshot (
  id BIGINT PRIMARY KEY AUTO_INCREMENT,
  tech_card_id INT NOT NULL,
  lock_version INT NOT NULL,
  reason VARCHAR(255) NOT NULL,
  snapshot JSON NOT NULL,
  created_by VARCHAR(255) NULL,
  created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
  CONSTRAINT fk_tech_card_snapshot_card FOREIGN KEY (tech_card_id) REFERENCES tech_card(id) ON DELETE CASCADE,
  INDEX idx_tech_card_snapshot_card_created (tech_card_id, created_at)
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4 COMMENT 'Immutable pre-update snapshots of a tech card';

-- +migrate Down

DROP TABLE IF EXISTS tech_card_snapshot;
