-- +migrate Up
-- Newsletter signup locale (ISO-639-1), so the welcome / promo mail matches the site the
-- user subscribed from when they have no explicit account preference yet. Nullable +
-- idempotent.
ALTER TABLE subscriber ADD COLUMN IF NOT EXISTS language VARCHAR(8) NULL;

-- +migrate Down
ALTER TABLE subscriber DROP COLUMN IF EXISTS language;
