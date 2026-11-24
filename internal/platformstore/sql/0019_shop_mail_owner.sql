-- +migrate Up
-- Team-shop mail ownership (X18-L7): which team sent each shop letter Resend accepted, by Resend's
-- email id, so a signed bounce or complaint event reaches that team's own suppression list (the
-- letters live in the team databases; the event names only Resend's id). Idempotent and
-- ANSI_QUOTES-safe: IF NOT EXISTS, no string literals. Online: one new table, nothing on a live
-- table. No FK, as every control-plane table: a team wipe deletes its rows (shopTables).
CREATE TABLE IF NOT EXISTS shop_mail_owner (
  provider_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  team_id BIGINT NOT NULL,
  mail_id BIGINT NOT NULL,
  created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  PRIMARY KEY (provider_id),
  KEY idx_shop_mail_owner_team (team_id),
  KEY idx_shop_mail_owner_created (created_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

-- +migrate Down
-- Never dropped: ownership rows outlive a binary rollback (as 0001-0018).
