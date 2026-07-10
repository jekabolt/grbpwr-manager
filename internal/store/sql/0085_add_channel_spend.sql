-- +migrate Up
-- Description: Operator-entered marketing spend per channel per day, joined to the GA4
--   campaign-attribution revenue to compute ROAS on the dashboard. This is source-of-truth
--   data (a small manual form), NOT a re-syncable bq_* cache. ROAS uses base-currency spend
--   only (the shop has no live FX), so spend feeding ROAS should be entered in base currency.
-- Affected tables: channel_spend (new)
-- Type: additive (non-breaking)

-- utm_* are capped at 191 chars: they are all part of uq_channel_spend, and under utf8mb4
-- (4 bytes/char) three VARCHAR(255) columns overflow InnoDB's 3072-byte index-key limit
-- (Error 1071). 191 is the utf8mb4 index-safe length and is far longer than any real UTM
-- value entered in the operator form. The GA4 tables keep VARCHAR(255) (no such composite key).
CREATE TABLE channel_spend (
    id INT PRIMARY KEY AUTO_INCREMENT,
    date DATE NOT NULL,
    utm_source VARCHAR(191) NOT NULL DEFAULT '',
    utm_medium VARCHAR(191) NOT NULL DEFAULT '',
    utm_campaign VARCHAR(191) NOT NULL DEFAULT '',
    amount DECIMAL(14, 2) NOT NULL CHECK (amount >= 0),
    currency VARCHAR(10) NOT NULL,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP NOT NULL,
    updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP NOT NULL,
    UNIQUE KEY uq_channel_spend (date, utm_source, utm_medium, utm_campaign, currency)
);

-- +migrate Down

DROP TABLE IF EXISTS channel_spend;
