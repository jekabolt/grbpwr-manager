-- +migrate Up

-- Team-shop customer mail (teams X18-L7). The outbox row learns what it is and how long it may live,
-- so a team-shop lane can drain only its own transactional letters and never a stale one.
--
-- kind            what rendered the row. shop_signin / shop_order are written only by the team-shop
--                 mailer. NULL is every row written before this file (and every storefront row, whose
--                 lane does not filter by kind). The team-shop lane quarantines anything else.
-- expires_at      UTC instant after which the letter is useless (a sign-in code past its challenge TTL).
-- claim_token     the worker that holds the row's lease (next_retry_at is the lease end). Release and
--                 bookkeeping only touch rows that still carry the caller's token.
-- quarantined_at  the row will never be sent (stale, wrong kind, created before its shop opened). Unlike
--                 MarkSendDead it does not depend on the attempt cap, so raising the cap cannot revive it.
--
-- Every column is nullable with no default, so an older binary keeps inserting and its worker keeps
-- draining (it never reads these columns). ALGORITHM=INSTANT is explicit so a copy fails loudly.
-- IDEMPOTENCY - DDL auto-commits, so each column has its own gate.

SET @col := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'send_email_request' AND COLUMN_NAME = 'kind');
SET @ddl := IF(@col = 0,
    'ALTER TABLE send_email_request ADD COLUMN kind VARCHAR(16) CHARACTER SET ascii NULL, ALGORITHM=INSTANT',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @col := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'send_email_request' AND COLUMN_NAME = 'expires_at');
SET @ddl := IF(@col = 0,
    'ALTER TABLE send_email_request ADD COLUMN expires_at DATETIME NULL, ALGORITHM=INSTANT',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @col := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'send_email_request' AND COLUMN_NAME = 'claim_token');
SET @ddl := IF(@col = 0,
    'ALTER TABLE send_email_request ADD COLUMN claim_token CHAR(36) CHARACTER SET ascii NULL, ALGORITHM=INSTANT',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @col := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'send_email_request' AND COLUMN_NAME = 'quarantined_at');
SET @ddl := IF(@col = 0,
    'ALTER TABLE send_email_request ADD COLUMN quarantined_at DATETIME NULL, ALGORITHM=INSTANT',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- +migrate Down

SET @col := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'send_email_request' AND COLUMN_NAME = 'quarantined_at');
SET @ddl := IF(@col = 1,
    'ALTER TABLE send_email_request DROP COLUMN quarantined_at',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @col := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'send_email_request' AND COLUMN_NAME = 'claim_token');
SET @ddl := IF(@col = 1,
    'ALTER TABLE send_email_request DROP COLUMN claim_token',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @col := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'send_email_request' AND COLUMN_NAME = 'expires_at');
SET @ddl := IF(@col = 1,
    'ALTER TABLE send_email_request DROP COLUMN expires_at',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @col := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'send_email_request' AND COLUMN_NAME = 'kind');
SET @ddl := IF(@col = 1,
    'ALTER TABLE send_email_request DROP COLUMN kind',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;
