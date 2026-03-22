-- +migrate Up
-- Retry scheduling for queued emails: attempt counter, next retry time, longer error storage.

ALTER TABLE send_email_request
  ADD COLUMN send_attempt_count INT NOT NULL DEFAULT 0 AFTER error_msg,
  ADD COLUMN next_retry_at DATETIME NULL DEFAULT NULL AFTER send_attempt_count;

ALTER TABLE send_email_request
  MODIFY COLUMN error_msg TEXT NULL DEFAULT NULL;

-- Add email_suppression table to track bounced and complained addresses
CREATE TABLE email_suppression (
  id INT AUTO_INCREMENT PRIMARY KEY,
  email VARCHAR(254) NOT NULL,
  reason ENUM('bounce', 'complaint') NOT NULL,
  created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  UNIQUE KEY uq_suppression_email (email)
) COMMENT 'Addresses that must not receive outbound email due to bounces or complaints';

-- Daily aggregated email delivery metrics from Resend webhooks
CREATE TABLE email_daily_metrics (
  id INT AUTO_INCREMENT PRIMARY KEY,
  date DATE NOT NULL,
  emails_sent INT NOT NULL DEFAULT 0,
  emails_delivered INT NOT NULL DEFAULT 0,
  emails_bounced INT NOT NULL DEFAULT 0,
  emails_opened INT NOT NULL DEFAULT 0,
  emails_clicked INT NOT NULL DEFAULT 0,
  created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  UNIQUE KEY uq_email_metrics_date (date)
) COMMENT 'Daily aggregated email delivery metrics from Resend webhooks';

-- +migrate Down
DROP TABLE email_daily_metrics;

DROP TABLE email_suppression;

ALTER TABLE send_email_request
  DROP COLUMN next_retry_at,
  DROP COLUMN send_attempt_count;

ALTER TABLE send_email_request
  MODIFY COLUMN error_msg VARCHAR(255) NULL DEFAULT NULL;
