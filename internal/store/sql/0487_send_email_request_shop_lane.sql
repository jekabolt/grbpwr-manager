-- +migrate Up

-- Team-shop customer mail (teams X18-L7). The outbox row learns what it is, how long it may live, who
-- holds it and what the provider was told, so a team-shop lane can drain only its own transactional
-- letters, never a stale one, and never one twice.
--
-- kind               what rendered the row. shop_signin / shop_order are written only by the team-shop
--                    mailer. NULL is every row written before this file (and every storefront row, whose
--                    lane does not filter by kind). The team-shop lane quarantines anything else.
-- expires_at         UTC instant after which the letter is useless (a sign-in code past its challenge TTL).
-- claim_token        the worker that holds the row's lease (next_retry_at is the lease end). Every write a
--                    worker makes to a claimed row (dispatch mark, sent, retry, dead, quarantine, release)
--                    applies only while the row still carries the worker's token.
-- quarantined_at     the row will never be sent (stale, wrong kind, created before its shop opened, an
--                    unknown dispatch outcome past the provider's idempotency window). Unlike MarkSendDead
--                    it does not depend on the attempt cap, so raising the cap cannot revive it.
-- idempotency_key    the provider idempotency key, fixed when the row is written: a config change
--                    between two attempts cannot change it. NULL on rows of an older binary.
-- first_dispatch_at  when a worker first offered the row to the provider (written before the call).
-- dispatch_count     how many times workers offered it (a dispatch mark always changes the row, so the
--                    driver's changed-rows count proves the lease was still held).
-- provider_email_id  the provider's id of the accepted letter: delivery events (bounce, complaint) find
--                    their row and recipient by it.
--
-- Every column but dispatch_count is nullable with no default; dispatch_count is NOT NULL DEFAULT 0. An
-- older binary keeps inserting and its worker keeps draining (it never reads these columns).
-- ALGORITHM=INSTANT is explicit on every ADD and DROP, so a server that would copy or lock the outbox
-- refuses the statement instead (MySQL 8.0.29+ drops a column instantly). No LOCK clause: MySQL refuses
-- LOCK=NONE next to INSTANT (error 1221), and INSTANT never takes more than the metadata lock anyway.
-- Down is safe only once every binary that reads these columns is stopped.
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

SET @col := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'send_email_request' AND COLUMN_NAME = 'idempotency_key');
SET @ddl := IF(@col = 0,
    'ALTER TABLE send_email_request ADD COLUMN idempotency_key VARCHAR(64) CHARACTER SET ascii NULL, ALGORITHM=INSTANT',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @col := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'send_email_request' AND COLUMN_NAME = 'first_dispatch_at');
SET @ddl := IF(@col = 0,
    'ALTER TABLE send_email_request ADD COLUMN first_dispatch_at DATETIME NULL, ALGORITHM=INSTANT',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @col := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'send_email_request' AND COLUMN_NAME = 'dispatch_count');
SET @ddl := IF(@col = 0,
    'ALTER TABLE send_email_request ADD COLUMN dispatch_count INT NOT NULL DEFAULT 0, ALGORITHM=INSTANT',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @col := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'send_email_request' AND COLUMN_NAME = 'provider_email_id');
SET @ddl := IF(@col = 0,
    'ALTER TABLE send_email_request ADD COLUMN provider_email_id VARCHAR(64) CHARACTER SET ascii NULL, ALGORITHM=INSTANT',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- +migrate Down

SET @col := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'send_email_request' AND COLUMN_NAME = 'provider_email_id');
SET @ddl := IF(@col = 1,
    'ALTER TABLE send_email_request DROP COLUMN provider_email_id, ALGORITHM=INSTANT',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @col := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'send_email_request' AND COLUMN_NAME = 'dispatch_count');
SET @ddl := IF(@col = 1,
    'ALTER TABLE send_email_request DROP COLUMN dispatch_count, ALGORITHM=INSTANT',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @col := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'send_email_request' AND COLUMN_NAME = 'first_dispatch_at');
SET @ddl := IF(@col = 1,
    'ALTER TABLE send_email_request DROP COLUMN first_dispatch_at, ALGORITHM=INSTANT',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @col := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'send_email_request' AND COLUMN_NAME = 'idempotency_key');
SET @ddl := IF(@col = 1,
    'ALTER TABLE send_email_request DROP COLUMN idempotency_key, ALGORITHM=INSTANT',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @col := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'send_email_request' AND COLUMN_NAME = 'quarantined_at');
SET @ddl := IF(@col = 1,
    'ALTER TABLE send_email_request DROP COLUMN quarantined_at, ALGORITHM=INSTANT',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @col := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'send_email_request' AND COLUMN_NAME = 'claim_token');
SET @ddl := IF(@col = 1,
    'ALTER TABLE send_email_request DROP COLUMN claim_token, ALGORITHM=INSTANT',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @col := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'send_email_request' AND COLUMN_NAME = 'expires_at');
SET @ddl := IF(@col = 1,
    'ALTER TABLE send_email_request DROP COLUMN expires_at, ALGORITHM=INSTANT',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @col := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'send_email_request' AND COLUMN_NAME = 'kind');
SET @ddl := IF(@col = 1,
    'ALTER TABLE send_email_request DROP COLUMN kind, ALGORITHM=INSTANT',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;
