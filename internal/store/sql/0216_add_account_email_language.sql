-- +migrate Up
-- Explicit, sticky per-account email language preference (ISO-639-1). Distinct from
-- default_language, which the storefront auto-derives from the active site locale; email_language
-- is set only by a deliberate user choice. NULL = not explicitly chosen -> the mailer uses the
-- event/purchase locale, then default_language, then EN.
--
-- Idempotent via the information_schema guard (managed MySQL rejects `ADD COLUMN IF NOT EXISTS` —
-- MariaDB-only), PREPARE/EXECUTE/DEALLOCATE one-per-line so a re-run after a mid-file failure is a
-- no-op.
SET @sql = IF(
    (SELECT COUNT(*) FROM information_schema.columns WHERE table_schema = DATABASE() AND table_name = 'storefront_account' AND column_name = 'email_language') > 0,
    'SELECT 1',
    'ALTER TABLE storefront_account ADD COLUMN email_language VARCHAR(8) NULL COMMENT ''Explicit sticky email-language preference (ISO-639-1). NULL = not chosen'''
);
PREPARE stmt FROM @sql;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- +migrate Down
SET @sql = IF(
    (SELECT COUNT(*) FROM information_schema.columns WHERE table_schema = DATABASE() AND table_name = 'storefront_account' AND column_name = 'email_language') > 0,
    'ALTER TABLE storefront_account DROP COLUMN email_language',
    'SELECT 1'
);
PREPARE stmt FROM @sql;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;
