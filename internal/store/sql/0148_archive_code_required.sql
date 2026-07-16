-- +migrate Up

-- Problem 029: make archive.code a real, enforced public identity. 0136 added the column, backfilled
-- from id and made it UNIQUE, but left it NULLABLE and generated the runtime code with an INSERT-then-
-- UPDATE (NULL window) — so a direct/import/partial write could persist an archive with no code, and a
-- read-time fallback fabricated a plausible-but-dead URL that the code resolver would 404 on. This
-- migration closes the gap: a dedicated allocation sequence hands every new archive a code BEFORE the
-- insert (no NULL window, no MAX(id)+1), all remaining NULL/empty codes are backfilled, the format is
-- pinned by a CHECK, and the column becomes NOT NULL.
--
-- Idempotent: seq table via CREATE IF NOT EXISTS; the AUTO_INCREMENT seed, the CHECK add and the
-- NOT NULL modify are all guarded via information_schema (multi-line PREPARE/EXECUTE/DEALLOCATE — a
-- single line trips 1064 on the managed DSN, see 0124). 0136 is not touched.

-- 1) Allocation sequence for archive codes. Same AUTO_INCREMENT-table pattern as model_no_seq (0132):
-- INSERT INTO archive_code_seq () VALUES () mints the next number via LAST_INSERT_ID, concurrency-safe.
CREATE TABLE IF NOT EXISTS archive_code_seq (
    id         INT PRIMARY KEY AUTO_INCREMENT,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- 2) Seed the sequence above the current max archive id, so a runtime-allocated code
-- (AR + base36(seq)) can never collide with 0136's id-derived backfill (AR + base36(id) for id <= max).
-- Only when the seq is still empty (freshly created), so a mid-file re-run never resets it downward.
SET @seq_empty := (SELECT COUNT(*) = 0 FROM archive_code_seq);
SET @seed := (SELECT COALESCE(MAX(id), 0) + 1 FROM archive);
SET @sql := IF(@seq_empty,
    CONCAT('ALTER TABLE archive_code_seq AUTO_INCREMENT = ', @seed),
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

-- 3) Backfill any still-NULL or empty codes deterministically from id (0136 filled NULLs; this also
-- catches empty strings). CONV(id,10,36) upper-cased, LPAD to 4 — byte-identical to entity.ArchiveCodeFromID.
UPDATE archive
    SET code = CONCAT('AR', LPAD(UPPER(CONV(id, 10, 36)), 4, '0'))
    WHERE code IS NULL OR code = '';

-- 4) Pin the format: 'AR' + 1..10 upper-case base36 chars (total length <= 12). Named explicitly and
-- guarded so a re-run does not re-add it.
SET @need_chk := (SELECT COUNT(*) = 0 FROM information_schema.TABLE_CONSTRAINTS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'archive'
      AND CONSTRAINT_TYPE = 'CHECK' AND CONSTRAINT_NAME = 'chk_archive_code_format');
SET @sql := IF(@need_chk,
    'ALTER TABLE archive ADD CONSTRAINT chk_archive_code_format CHECK (REGEXP_LIKE(code, ''^AR[0-9A-Z]{1,10}$'', ''c''))',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

-- 5) Enforce NOT NULL now that every row has a valid code.
SET @is_nullable := (SELECT IS_NULLABLE FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'archive' AND COLUMN_NAME = 'code');
SET @sql := IF(@is_nullable = 'YES',
    'ALTER TABLE archive MODIFY COLUMN code VARCHAR(12) NOT NULL',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;
