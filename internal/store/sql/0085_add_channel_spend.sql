-- +migrate Up
-- Description: Operator-entered marketing spend per channel per day, joined to the GA4
--   campaign-attribution revenue to compute ROAS on the dashboard. This is source-of-truth
--   data (a small manual form), NOT a re-syncable bq_* cache. ROAS uses base-currency spend
--   only (the shop has no live FX), so spend feeding ROAS should be entered in base currency.
-- Affected tables: channel_spend (new)
-- Type: additive (non-breaking)

CREATE TABLE channel_spend (
    id INT PRIMARY KEY AUTO_INCREMENT,
    date DATE NOT NULL,
    utm_source VARCHAR(255) NOT NULL DEFAULT '',
    utm_medium VARCHAR(255) NOT NULL DEFAULT '',
    utm_campaign VARCHAR(255) NOT NULL DEFAULT '',
    amount DECIMAL(14, 2) NOT NULL CHECK (amount >= 0),
    currency VARCHAR(10) NOT NULL,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP NOT NULL,
    updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP NOT NULL,
    UNIQUE KEY uq_channel_spend (date, utm_source, utm_medium, utm_campaign, currency)
);

-- +migrate Down

DROP TABLE IF EXISTS channel_spend;
