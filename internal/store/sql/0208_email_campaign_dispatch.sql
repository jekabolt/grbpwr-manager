-- +migrate Up
-- Ф3 email-campaign dispatcher. Every ALTER is independently guarded because
-- MySQL DDL auto-commits and sql-migrate will replay this file after a partial
-- failure. PREPARE / EXECUTE / DEALLOCATE intentionally stay on separate lines:
-- the managed beta/prod DSN does not enable multiStatements.

SET @need_col := (SELECT COUNT(*) = 0 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'email_campaign' AND COLUMN_NAME = 'audience_predicate');
SET @sql := IF(@need_col,
    'ALTER TABLE email_campaign ADD COLUMN audience_predicate JSON NULL',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

SET @need_col := (SELECT COUNT(*) = 0 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'email_campaign' AND COLUMN_NAME = 'audience_snapshot_at');
SET @sql := IF(@need_col,
    'ALTER TABLE email_campaign ADD COLUMN audience_snapshot_at DATETIME(6) NULL',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

SET @need_col := (SELECT COUNT(*) = 0 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'email_campaign' AND COLUMN_NAME = 'fanout_max_account_id');
SET @sql := IF(@need_col,
    'ALTER TABLE email_campaign ADD COLUMN fanout_max_account_id INT NOT NULL DEFAULT 0',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

SET @need_col := (SELECT COUNT(*) = 0 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'email_campaign' AND COLUMN_NAME = 'fanout_cursor_account_id');
SET @sql := IF(@need_col,
    'ALTER TABLE email_campaign ADD COLUMN fanout_cursor_account_id INT NOT NULL DEFAULT 0',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

SET @need_col := (SELECT COUNT(*) = 0 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'email_campaign' AND COLUMN_NAME = 'audience_materialized_at');
SET @sql := IF(@need_col,
    'ALTER TABLE email_campaign ADD COLUMN audience_materialized_at DATETIME(6) NULL',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

SET @need_col := (SELECT COUNT(*) = 0 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'email_campaign' AND COLUMN_NAME = 'recipient_count');
SET @sql := IF(@need_col,
    'ALTER TABLE email_campaign ADD COLUMN recipient_count INT UNSIGNED NULL',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

SET @need_col := (SELECT COUNT(*) = 0 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'email_campaign' AND COLUMN_NAME = 'dispatch_error');
SET @sql := IF(@need_col,
    'ALTER TABLE email_campaign ADD COLUMN dispatch_error TEXT NULL',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

SET @need_key := (SELECT COUNT(*) = 0 FROM information_schema.STATISTICS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'email_campaign_variant'
      AND INDEX_NAME = 'uq_email_campaign_variant_campaign_id_id');
SET @sql := IF(@need_key,
    'ALTER TABLE email_campaign_variant ADD UNIQUE KEY uq_email_campaign_variant_campaign_id_id (campaign_id, id)',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

CREATE TABLE IF NOT EXISTS email_campaign_recipient (
    id BIGINT UNSIGNED PRIMARY KEY AUTO_INCREMENT,
    campaign_id INT NOT NULL,
    account_id INT NULL,
    email VARCHAR(254) NOT NULL,
    language_id INT NOT NULL,
    variant_id INT NULL,
    cohort ENUM('ab','remainder') NOT NULL DEFAULT 'remainder',
    status ENUM('pending','sent','failed','skipped') NOT NULL DEFAULT 'pending',
    unsubscribe_url VARCHAR(1024) NULL,
    dispatch_batch_id CHAR(36) NULL,
    batch_ordinal TINYINT UNSIGNED NULL,
    resend_idempotency_key VARCHAR(128) NULL,
    claim_token CHAR(36) NULL,
    claim_expires_at DATETIME(6) NULL,
    payload_sha256 BINARY(32) NULL,
    resend_email_id VARCHAR(64) NULL,
    attempt_count SMALLINT UNSIGNED NOT NULL DEFAULT 0,
    next_attempt_at DATETIME(6) NULL DEFAULT CURRENT_TIMESTAMP(6),
    first_provider_attempt_at DATETIME(6) NULL,
    last_attempt_at DATETIME(6) NULL,
    error_code VARCHAR(64) NULL,
    last_error TEXT NULL,
    sent_at DATETIME(6) NULL,
    completed_at DATETIME(6) NULL,
    created_at TIMESTAMP(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    updated_at TIMESTAMP(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6),
    CONSTRAINT fk_ecr_campaign FOREIGN KEY (campaign_id) REFERENCES email_campaign(id) ON DELETE RESTRICT,
    CONSTRAINT fk_ecr_account FOREIGN KEY (account_id) REFERENCES storefront_account(id) ON DELETE SET NULL,
    CONSTRAINT fk_ecr_language FOREIGN KEY (language_id) REFERENCES language(id) ON DELETE RESTRICT,
    CONSTRAINT fk_ecr_campaign_variant FOREIGN KEY (campaign_id, variant_id)
        REFERENCES email_campaign_variant(campaign_id, id) ON DELETE RESTRICT,
    CONSTRAINT chk_ecr_batch_ordinal CHECK (batch_ordinal IS NULL OR batch_ordinal < 100),
    UNIQUE KEY uq_ecr_campaign_email (campaign_id, email),
    UNIQUE KEY uq_ecr_resend_email_id (resend_email_id),
    UNIQUE KEY uq_ecr_batch_ordinal (dispatch_batch_id, batch_ordinal),
    INDEX idx_ecr_ready (campaign_id, status, next_attempt_at, id),
    INDEX idx_ecr_claim_expiry (status, claim_expires_at, dispatch_batch_id),
    INDEX idx_ecr_campaign_cohort (campaign_id, cohort, variant_id, status),
    INDEX idx_ecr_account (account_id),
    INDEX idx_ecr_language (language_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS email_campaign_render_snapshot (
    campaign_id INT NOT NULL,
    variant_id INT NOT NULL,
    language_id INT NOT NULL,
    subject VARCHAR(998) NOT NULL,
    html_template MEDIUMTEXT NOT NULL,
    text_template MEDIUMTEXT NOT NULL,
    from_value VARCHAR(511) NOT NULL,
    reply_to VARCHAR(254) NULL,
    payload_version SMALLINT UNSIGNED NOT NULL DEFAULT 1,
    created_at TIMESTAMP(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    PRIMARY KEY (campaign_id, variant_id, language_id),
    CONSTRAINT fk_ecrs_campaign_variant FOREIGN KEY (campaign_id, variant_id)
        REFERENCES email_campaign_variant(campaign_id, id) ON DELETE RESTRICT,
    CONSTRAINT fk_ecrs_language FOREIGN KEY (language_id) REFERENCES language(id) ON DELETE RESTRICT
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- +migrate Down

DROP TABLE IF EXISTS email_campaign_render_snapshot;
DROP TABLE IF EXISTS email_campaign_recipient;

SET @have_key := (SELECT COUNT(*) FROM information_schema.STATISTICS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'email_campaign_variant'
      AND INDEX_NAME = 'uq_email_campaign_variant_campaign_id_id');
SET @sql := IF(@have_key,
    'ALTER TABLE email_campaign_variant DROP INDEX uq_email_campaign_variant_campaign_id_id',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

SET @have_col := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'email_campaign' AND COLUMN_NAME = 'dispatch_error');
SET @sql := IF(@have_col, 'ALTER TABLE email_campaign DROP COLUMN dispatch_error', 'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

SET @have_col := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'email_campaign' AND COLUMN_NAME = 'recipient_count');
SET @sql := IF(@have_col, 'ALTER TABLE email_campaign DROP COLUMN recipient_count', 'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

SET @have_col := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'email_campaign' AND COLUMN_NAME = 'audience_materialized_at');
SET @sql := IF(@have_col, 'ALTER TABLE email_campaign DROP COLUMN audience_materialized_at', 'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

SET @have_col := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'email_campaign' AND COLUMN_NAME = 'fanout_cursor_account_id');
SET @sql := IF(@have_col, 'ALTER TABLE email_campaign DROP COLUMN fanout_cursor_account_id', 'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

SET @have_col := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'email_campaign' AND COLUMN_NAME = 'fanout_max_account_id');
SET @sql := IF(@have_col, 'ALTER TABLE email_campaign DROP COLUMN fanout_max_account_id', 'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

SET @have_col := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'email_campaign' AND COLUMN_NAME = 'audience_snapshot_at');
SET @sql := IF(@have_col, 'ALTER TABLE email_campaign DROP COLUMN audience_snapshot_at', 'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

SET @have_col := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'email_campaign' AND COLUMN_NAME = 'audience_predicate');
SET @sql := IF(@have_col, 'ALTER TABLE email_campaign DROP COLUMN audience_predicate', 'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;
