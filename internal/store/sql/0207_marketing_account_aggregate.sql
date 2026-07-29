-- +migrate Up
CREATE TABLE IF NOT EXISTS marketing_account_aggregate (
    account_id      INT PRIMARY KEY,
    total_spend_eur DECIMAL(12,2) NOT NULL DEFAULT 0.00,
    order_count     INT NOT NULL DEFAULT 0,
    first_order_at  DATETIME NULL,
    last_order_at   DATETIME NULL,
    refreshed_at   TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    CONSTRAINT fk_maa_account FOREIGN KEY (account_id) REFERENCES storefront_account(id) ON DELETE CASCADE,
    INDEX idx_maa_spend (total_spend_eur),
    INDEX idx_maa_last_order (last_order_at)
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4 COLLATE = utf8mb4_unicode_ci COMMENT 'Precomputed per-account order aggregates for email segmentation (refresh-worker owned)';

-- +migrate Down
DROP TABLE IF EXISTS marketing_account_aggregate;
