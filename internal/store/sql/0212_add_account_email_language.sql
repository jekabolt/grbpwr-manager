-- +migrate Up
-- Explicit, sticky per-account email language preference (ISO-639-1). Distinct from
-- default_language, which the storefront auto-derives from the active site locale;
-- email_language is set only by a deliberate user choice. NULL = not explicitly chosen ->
-- the mailer uses the event/purchase locale, then default_language, then EN. Nullable +
-- idempotent.
ALTER TABLE storefront_account ADD COLUMN IF NOT EXISTS email_language VARCHAR(8) NULL;

-- +migrate Down
ALTER TABLE storefront_account DROP COLUMN IF EXISTS email_language;
