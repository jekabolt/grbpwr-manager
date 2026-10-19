-- Assembly skeleton P2 lane M (2026-10-10): the «draft» mark of an operation becomes a stored fact of
-- the step row (tmp/plans/assembly-from-pattern/03-P2-DESIGN.md §7).
--
-- WHAT IT MEANS. A step the assembly skeleton wrote (OPERATIONS → «build from pattern» → apply) is
-- marked draft = 1: nobody has checked it yet. The first meaningful edit of the row, or a click on its
-- «draft» chip («reviewed»), clears it. Until now the mark lived only in the browser tab and was gone
-- on reload; the team could not see which steps nobody had looked at.
--
-- NOT IN THE OPERATION DIGEST. Checking a step does not change what the card says to the workshop;
-- hashing the mark would re-sign a card every time someone reviews somebody else's step. The
-- construction digest golden (internal/dto) proves the bytes do not move.
--
-- ADDITIVE ONLY: NOT NULL DEFAULT 0 is an INSTANT ADD COLUMN in MySQL 8 — no table copy, no CHECK (a
-- retroactive CHECK halts the deploy), no backfill: every existing step is «not draft», which is
-- true. The old binary does not read the column and the operation rows are full-replaced on save, so a
-- rollback of the binary only drops the marks it saves over.
-- IDEMPOTENT: information_schema guard, one statement per PREPARE (prod has no multiStatements).

-- +migrate Up

SET @col_exists := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_operation' AND COLUMN_NAME = 'draft');
SET @ddl := IF(@col_exists = 0,
    'ALTER TABLE tech_card_operation ADD COLUMN draft TINYINT(1) NOT NULL DEFAULT 0 COMMENT ''written by the assembly skeleton and not reviewed yet; not in the digest''',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- +migrate Down

SET @col_exists := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_operation' AND COLUMN_NAME = 'draft');
SET @ddl := IF(@col_exists = 1, 'ALTER TABLE tech_card_operation DROP COLUMN draft', 'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;
