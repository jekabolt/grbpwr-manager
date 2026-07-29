-- +migrate Up
-- Ф4 campaign engagement attribution. Every ALTER is independently guarded
-- because MySQL DDL auto-commits and sql-migrate replays the file after a
-- partial failure. PREPARE / EXECUTE / DEALLOCATE stay on separate lines
-- because the managed beta/prod DSN does not enable multiStatements.

SET @need_col := (SELECT COUNT(*) = 0 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'email_campaign_recipient' AND COLUMN_NAME = 'delivered_at');
SET @sql := IF(@need_col,
    'ALTER TABLE email_campaign_recipient ADD COLUMN delivered_at DATETIME(6) NULL',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

SET @need_col := (SELECT COUNT(*) = 0 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'email_campaign_recipient' AND COLUMN_NAME = 'first_opened_at');
SET @sql := IF(@need_col,
    'ALTER TABLE email_campaign_recipient ADD COLUMN first_opened_at DATETIME(6) NULL',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

SET @need_col := (SELECT COUNT(*) = 0 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'email_campaign_recipient' AND COLUMN_NAME = 'last_opened_at');
SET @sql := IF(@need_col,
    'ALTER TABLE email_campaign_recipient ADD COLUMN last_opened_at DATETIME(6) NULL',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

SET @need_col := (SELECT COUNT(*) = 0 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'email_campaign_recipient' AND COLUMN_NAME = 'open_count');
SET @sql := IF(@need_col,
    'ALTER TABLE email_campaign_recipient ADD COLUMN open_count INT UNSIGNED NOT NULL DEFAULT 0',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

SET @need_col := (SELECT COUNT(*) = 0 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'email_campaign_recipient' AND COLUMN_NAME = 'first_clicked_at');
SET @sql := IF(@need_col,
    'ALTER TABLE email_campaign_recipient ADD COLUMN first_clicked_at DATETIME(6) NULL',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

SET @need_col := (SELECT COUNT(*) = 0 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'email_campaign_recipient' AND COLUMN_NAME = 'last_clicked_at');
SET @sql := IF(@need_col,
    'ALTER TABLE email_campaign_recipient ADD COLUMN last_clicked_at DATETIME(6) NULL',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

SET @need_col := (SELECT COUNT(*) = 0 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'email_campaign_recipient' AND COLUMN_NAME = 'click_count');
SET @sql := IF(@need_col,
    'ALTER TABLE email_campaign_recipient ADD COLUMN click_count INT UNSIGNED NOT NULL DEFAULT 0',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

SET @need_col := (SELECT COUNT(*) = 0 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'email_campaign_recipient' AND COLUMN_NAME = 'bounced_at');
SET @sql := IF(@need_col,
    'ALTER TABLE email_campaign_recipient ADD COLUMN bounced_at DATETIME(6) NULL',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

SET @need_col := (SELECT COUNT(*) = 0 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'email_campaign_recipient' AND COLUMN_NAME = 'complained_at');
SET @sql := IF(@need_col,
    'ALTER TABLE email_campaign_recipient ADD COLUMN complained_at DATETIME(6) NULL',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

SET @need_col := (SELECT COUNT(*) = 0 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'email_campaign_recipient' AND COLUMN_NAME = 'unsubscribed_at');
SET @sql := IF(@need_col,
    'ALTER TABLE email_campaign_recipient ADD COLUMN unsubscribed_at DATETIME(6) NULL',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

SET @need_col := (SELECT COUNT(*) = 0 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'email_campaign_recipient' AND COLUMN_NAME = 'hard_failed_at');
SET @sql := IF(@need_col,
    'ALTER TABLE email_campaign_recipient ADD COLUMN hard_failed_at DATETIME(6) NULL',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

-- +migrate Down

SET @have_col := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'email_campaign_recipient' AND COLUMN_NAME = 'hard_failed_at');
SET @sql := IF(@have_col, 'ALTER TABLE email_campaign_recipient DROP COLUMN hard_failed_at', 'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

SET @have_col := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'email_campaign_recipient' AND COLUMN_NAME = 'unsubscribed_at');
SET @sql := IF(@have_col, 'ALTER TABLE email_campaign_recipient DROP COLUMN unsubscribed_at', 'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

SET @have_col := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'email_campaign_recipient' AND COLUMN_NAME = 'complained_at');
SET @sql := IF(@have_col, 'ALTER TABLE email_campaign_recipient DROP COLUMN complained_at', 'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

SET @have_col := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'email_campaign_recipient' AND COLUMN_NAME = 'bounced_at');
SET @sql := IF(@have_col, 'ALTER TABLE email_campaign_recipient DROP COLUMN bounced_at', 'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

SET @have_col := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'email_campaign_recipient' AND COLUMN_NAME = 'click_count');
SET @sql := IF(@have_col, 'ALTER TABLE email_campaign_recipient DROP COLUMN click_count', 'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

SET @have_col := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'email_campaign_recipient' AND COLUMN_NAME = 'last_clicked_at');
SET @sql := IF(@have_col, 'ALTER TABLE email_campaign_recipient DROP COLUMN last_clicked_at', 'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

SET @have_col := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'email_campaign_recipient' AND COLUMN_NAME = 'first_clicked_at');
SET @sql := IF(@have_col, 'ALTER TABLE email_campaign_recipient DROP COLUMN first_clicked_at', 'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

SET @have_col := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'email_campaign_recipient' AND COLUMN_NAME = 'open_count');
SET @sql := IF(@have_col, 'ALTER TABLE email_campaign_recipient DROP COLUMN open_count', 'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

SET @have_col := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'email_campaign_recipient' AND COLUMN_NAME = 'last_opened_at');
SET @sql := IF(@have_col, 'ALTER TABLE email_campaign_recipient DROP COLUMN last_opened_at', 'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

SET @have_col := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'email_campaign_recipient' AND COLUMN_NAME = 'first_opened_at');
SET @sql := IF(@have_col, 'ALTER TABLE email_campaign_recipient DROP COLUMN first_opened_at', 'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

SET @have_col := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'email_campaign_recipient' AND COLUMN_NAME = 'delivered_at');
SET @sql := IF(@have_col, 'ALTER TABLE email_campaign_recipient DROP COLUMN delivered_at', 'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;
