-- +migrate Up

-- opex_entry is a lightweight fixed-cost (OPEX) journal so the dashboard can show an
-- operating result under the contribution margin — making clear that contribution is NOT
-- profit (task 22). One amount per month per category, in the base currency (EUR). This is a
-- management order-of-magnitude aid, deliberately NOT double-entry accounting.
CREATE TABLE opex_entry (
    id         INT AUTO_INCREMENT PRIMARY KEY,
    month      DATE NOT NULL,                        -- first day of the month the cost belongs to
    category   VARCHAR(32) NOT NULL,                 -- salaries|rent|software|marketing_other|production_content|other (validated in dto)
    amount     DECIMAL(12, 2) NOT NULL CHECK (amount >= 0), -- base currency (EUR)
    note       VARCHAR(255) NULL,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP NOT NULL,
    updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP NOT NULL,
    UNIQUE KEY uq_opex_month_category (month, category)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- +migrate Down

DROP TABLE IF EXISTS opex_entry;
