-- +migrate Up
-- A team's brand data for its papers: name, legal entity and address, contact email, website (teams W).
-- Idempotent and ANSI_QUOTES-safe: single quotes only, CREATE TABLE IF NOT EXISTS. Online: one new
-- table, nothing on a live table. No FK, as every control-plane table: deletes are explicit (beside
-- every hard delete of team_logo). Text is stored as typed, already normalised by the server; empty
-- is a value (prints nothing). updated_by = the last platform user who saved it, NULL for the seed.

CREATE TABLE IF NOT EXISTS team_brand (
  team_id BIGINT NOT NULL,
  brand_name VARCHAR(64) NOT NULL DEFAULT '',
  legal_entity VARCHAR(96) NOT NULL DEFAULT '',
  legal_address_1 VARCHAR(64) NOT NULL DEFAULT '',
  legal_address_2 VARCHAR(64) NOT NULL DEFAULT '',
  legal_address_3 VARCHAR(64) NOT NULL DEFAULT '',
  contact_email VARCHAR(254) NOT NULL DEFAULT '',
  website VARCHAR(128) NOT NULL DEFAULT '',
  updated_by BIGINT NULL,
  updated_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  PRIMARY KEY (team_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

-- +migrate Down
-- Never dropped: brand data outlives a binary rollback (as 0001-0007).
