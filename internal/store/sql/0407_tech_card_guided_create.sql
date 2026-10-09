-- TECH-CARD ONBOARDING — GUIDED CREATE (2026-10-07, tmp/plans/techcard-onboarding S4-server).
--
-- Two columns on tech_card:
--
-- guided — the card was born through the guided studio flow (CREATE NEW → four fields → mood). The
-- studio shows guided faces and the list shows a `setup` pill while it is set; ExitTechCardGuide
-- clears it. DEFAULT 0: every card that exists today, and every clone / import / legacy create, is
-- not guided and renders exactly as before.
--
-- create_request_id — the client_request_id of the CreateTechCard call that made the row. A retry of
-- the same create (lost response, double effect) finds the row by this key and gets the same id
-- instead of a second card. NULL = created without a key (everything before 0407, clone, import);
-- NULLs are not unique-constrained, so they never collide. utf8mb4_bin: byte comparison as in Go
-- (precedent 0370); the store trims the key before reading and writing it. VARCHAR(64) = UUID with
-- room; the handler rejects longer keys before the transaction.
--
-- Neither column is in the shared header column list (clone/import/update): only AddTechCard names
-- them, and UPDATE never touches them — guided is cleared only by ExitTechCardGuide, which does not
-- bump lock_version.
--
-- NO CHECK (ADD CONSTRAINT … CHECK runs as ALGORITHM=COPY and re-validates the whole history inside
-- the deploy's migration window). ADD COLUMN at the end is INSTANT; ADD UNIQUE KEY is INPLACE. Each
-- under its own information_schema gate (MySQL has no ADD COLUMN IF NOT EXISTS); a failure between
-- them leaves a state the next run finishes.
--
-- ROLLBACK-SAFE: the previous binary never names either column (explicit column lists on every
-- write; `SELECT *` reads run on the sqlx Unsafe handle, which ignores a column with no struct field).
--
-- PREPARE / EXECUTE / DEALLOCATE each on its own line (prod runs without multiStatements).

-- +migrate Up

-- 1. guided.
SET @tc_guided := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card'
      AND COLUMN_NAME = 'guided');
SET @ddl := IF(@tc_guided = 0,
    'ALTER TABLE tech_card
        ADD COLUMN guided TINYINT(1) NOT NULL DEFAULT 0 COMMENT ''created through the guided studio flow; cleared by ExitTechCardGuide (no lock_version bump)''',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- 2. create_request_id.
SET @tc_create_request_id := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card'
      AND COLUMN_NAME = 'create_request_id');
SET @ddl := IF(@tc_create_request_id = 0,
    'ALTER TABLE tech_card
        ADD COLUMN create_request_id VARCHAR(64) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NULL COMMENT ''client_request_id of the CreateTechCard call that made the row; a retry under the same key returns this row. NULL = created without a key''',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- 3. Unique key on create_request_id (NULLs do not collide).
SET @tc_create_request_id_idx := (SELECT COUNT(*) FROM information_schema.STATISTICS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card'
      AND INDEX_NAME = 'uq_tech_card_create_request_id');
SET @ddl := IF(@tc_create_request_id_idx = 0,
    'ALTER TABLE tech_card
        ADD UNIQUE KEY uq_tech_card_create_request_id (create_request_id)',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- +migrate Down
--
-- Reverse order: the key goes before its column.

SET @tc_create_request_id_idx_down := (SELECT COUNT(*) FROM information_schema.STATISTICS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card'
      AND INDEX_NAME = 'uq_tech_card_create_request_id');
SET @ddl_down := IF(@tc_create_request_id_idx_down > 0,
    'ALTER TABLE tech_card DROP INDEX uq_tech_card_create_request_id',
    'SELECT 1');
PREPARE stmt FROM @ddl_down;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @tc_create_request_id_down := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card'
      AND COLUMN_NAME = 'create_request_id');
SET @ddl_down := IF(@tc_create_request_id_down = 1,
    'ALTER TABLE tech_card DROP COLUMN create_request_id',
    'SELECT 1');
PREPARE stmt FROM @ddl_down;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @tc_guided_down := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card'
      AND COLUMN_NAME = 'guided');
SET @ddl_down := IF(@tc_guided_down = 1, 'ALTER TABLE tech_card DROP COLUMN guided', 'SELECT 1');
PREPARE stmt FROM @ddl_down;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;
