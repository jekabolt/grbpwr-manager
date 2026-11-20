-- +migrate Up

-- Recently touched tech cards per account (home «recent techcards»). One row per (account, card):
-- GetTechCard and every tech-card write by that account upsert touched_at; the store keeps the
-- latest entity.RecentTechCardsKeep rows per account and trims the rest on each touch.
--
-- account_id is the TENANT admins.id: each team has its own database and its own admins table, so
-- a row never crosses teams. Both FKs cascade: a deleted card or a deleted account takes its rows.
-- The (account_id, touched_at) index serves the list and the trim.
--
-- Idempotent (AGENTS.md): CREATE TABLE IF NOT EXISTS only; no string literals, so ANSI_QUOTES is moot.
CREATE TABLE IF NOT EXISTS tech_card_recent (
  account_id INT NOT NULL,
  tech_card_id INT NOT NULL,
  touched_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
  PRIMARY KEY (account_id, tech_card_id),
  INDEX idx_tech_card_recent_account_touched (account_id, touched_at),
  INDEX idx_tech_card_recent_card (tech_card_id),
  CONSTRAINT fk_tech_card_recent_account FOREIGN KEY (account_id) REFERENCES admins(id) ON DELETE CASCADE,
  CONSTRAINT fk_tech_card_recent_card FOREIGN KEY (tech_card_id) REFERENCES tech_card(id) ON DELETE CASCADE
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4;

-- +migrate Down

DROP TABLE IF EXISTS tech_card_recent;
