-- +migrate Up
-- Email addresses and provider subjects are identity keys, not human-language text. Make every
-- control-plane column that stores or compares one byte-exact. MODIFY is naturally idempotent and
-- keeps the existing width and NULL-ness; utf8mb4 (rather than ASCII) lets legacy rows migrate.
--
-- uq_platform_user_email stays valid: values distinct under utf8mb4_0900_ai_ci are also distinct
-- under utf8mb4_bin, so changing to the more discriminating equality cannot create a duplicate.
-- uq_platform_identity_subject(provider, subject) is safe for the same reason. The other three
-- columns have no email/subject unique index. ANSI_QUOTES-safe: this file uses no quoted literals.

ALTER TABLE platform_user
  MODIFY email VARCHAR(254) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NULL;

ALTER TABLE team_invite
  MODIFY email VARCHAR(254) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL;

ALTER TABLE platform_identity
  MODIFY subject VARCHAR(255) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
  MODIFY email VARCHAR(254) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NULL;

ALTER TABLE platform_email
  MODIFY to_email VARCHAR(254) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL;

-- +migrate Down
-- Identity equality is never weakened on rollback.
