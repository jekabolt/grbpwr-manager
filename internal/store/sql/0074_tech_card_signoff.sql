-- +migrate Up
-- Per-section sign-off (Phase 3.5a-2). The single header approval_state can't say
-- "costing approved by finance, labels still pending compliance". tech_card_signoff
-- records a sign-off per responsible section, so the merchandiser/manager can see
-- who signed which sheet. signed_by is free text (matching the codebase's actor
-- convention: fitting.recorded_by, tech_card_revision.author).

CREATE TABLE tech_card_signoff (
  id INT PRIMARY KEY AUTO_INCREMENT,
  tech_card_id INT NOT NULL,
  section VARCHAR(16) NOT NULL
    COMMENT 'design|construction|pom|materials|colour|labels|packaging|costing'
    CHECK (section REGEXP '^(design|construction|pom|materials|colour|labels|packaging|costing)$'),
  state VARCHAR(8) NOT NULL DEFAULT 'pending' COMMENT 'pending|approved|rejected'
    CHECK (state REGEXP '^(pending|approved|rejected)$'),
  signed_by VARCHAR(255) NULL COMMENT 'who signed off (free text / role name)',
  signed_at TIMESTAMP NULL,
  note TEXT NULL,
  display_order INT NOT NULL DEFAULT 0,
  UNIQUE KEY uniq_tech_card_signoff (tech_card_id, section),
  FOREIGN KEY (tech_card_id) REFERENCES tech_card(id) ON DELETE CASCADE
) COMMENT 'Per-section sign-off for a tech card';

-- +migrate Down
DROP TABLE IF EXISTS tech_card_signoff;
