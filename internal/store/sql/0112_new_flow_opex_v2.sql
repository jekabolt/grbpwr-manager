-- +migrate Up

-- new-flow NF-08: OPEX v2 — a line-item journal with multi-currency and recurring templates. Until
-- now opex_entry held ONE aggregated base-EUR amount per month × category — no line items («Adobe —
-- 60», «зарплата швеи — 1200»), no currency, no repetition. opex_line is the line journal (each with
-- its own currency, folded to base on write); opex_recurring holds salaries/subscriptions that a
-- worker materializes into monthly lines. opex_entry is kept and backfilled — read switches to
-- opex_line atomically with this deploy; its drop is a later migration.
--
-- Idempotent: CREATE TABLE IF NOT EXISTS with inline named indexes/CHECKs + an INSERT ... WHERE NOT
-- EXISTS backfill, so a mid-file failure re-runs cleanly.

CREATE TABLE IF NOT EXISTS opex_recurring (
    id INT AUTO_INCREMENT PRIMARY KEY,
    label VARCHAR(255) NOT NULL,          -- «Adobe CC», «зарплата — швея Мария», «аренда студии»
    category VARCHAR(32) NOT NULL,        -- closed set validated in dto (no DB CHECK, so it extends without a migration)
    amount DECIMAL(12, 2) NOT NULL,       -- in `currency`
    currency CHAR(3) NOT NULL,
    active_from DATE NOT NULL,            -- normalised to the 1st of the month
    active_to DATE NULL,                  -- NULL = open-ended; else the 1st of the last active month
    note VARCHAR(255) NULL,
    archived BOOLEAN NOT NULL DEFAULT FALSE,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    CONSTRAINT chk_opex_rec_amount CHECK (amount >= 0),
    INDEX idx_opex_rec_active (archived, active_from)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS opex_line (
    id INT AUTO_INCREMENT PRIMARY KEY,
    month DATE NOT NULL,                  -- 1st of the month the cost belongs to
    category VARCHAR(32) NOT NULL,
    label VARCHAR(255) NOT NULL DEFAULT '',
    amount DECIMAL(12, 2) NOT NULL,       -- in `currency`
    currency CHAR(3) NOT NULL,
    amount_base DECIMAL(12, 2) NULL,      -- NULL = no FX rate for the month (uncosted; excluded + caveat)
    recurring_id INT NULL,                -- set when materialised from a template
    note VARCHAR(255) NULL,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    CONSTRAINT fk_opex_line_recurring FOREIGN KEY (recurring_id) REFERENCES opex_recurring(id) ON DELETE SET NULL,
    CONSTRAINT chk_opex_line_amount CHECK (amount >= 0),
    -- manual lines are distinguished by label; a materialised line's label is its template label,
    -- so (month, category, label) is unique and re-materialisation is an idempotent upsert.
    UNIQUE KEY uniq_opex_line (month, category, label),
    INDEX idx_opex_line_month (month)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- carry the existing aggregates forward as base-EUR lines labelled '(aggregate)'.
INSERT INTO opex_line (month, category, label, amount, currency, amount_base, note)
SELECT e.month, e.category, '(aggregate)', e.amount, 'EUR', e.amount, e.note
FROM opex_entry e
WHERE NOT EXISTS (SELECT 1 FROM opex_line l
                  WHERE l.month = e.month AND l.category = e.category AND l.label = '(aggregate)');

-- +migrate Down

DROP TABLE IF EXISTS opex_line;
DROP TABLE IF EXISTS opex_recurring;
SELECT 1;
