-- +migrate Up

-- Problem 029: make archive.code a real, enforced public identity. 0136 added the column, backfilled
-- from id and made it UNIQUE, but left it NULLABLE and generated the runtime code with an INSERT-then-
-- UPDATE (NULL window) — so a direct/import/partial write could persist an archive with no code, and a
-- read-time fallback fabricated a plausible-but-dead URL that the code resolver would 404 on. This
-- migration closes the gap: a dedicated allocation sequence hands every new archive a code BEFORE the
-- insert (no NULL window, no MAX(id)+1), all remaining NULL/empty codes are backfilled, the format is
-- pinned by a CHECK, and the column becomes NOT NULL.
--
-- Idempotent: persisted preflight/guard tables expose conflicts before archive data is mutated; seq
-- table uses CREATE IF NOT EXISTS; the AUTO_INCREMENT floor, CHECK and NOT NULL changes are guarded.
-- 0136 is not touched. Code immutability is enforced by the application update surface; managed
-- MySQL deploy users cannot safely CREATE TRIGGER with binary logging enabled (Error 1419).

-- 1) Fail-fast report. Direct/import writes are the threat model, so do not silently rewrite malformed
-- non-empty codes or collide an id-derived backfill with a manually persisted code.
CREATE TABLE IF NOT EXISTS migration_0148_archive_code_conflict (
    conflict_type         VARCHAR(32) NOT NULL,
    archive_id            INT NOT NULL,
    conflicting_archive_id INT NOT NULL DEFAULT 0,
    PRIMARY KEY (conflict_type, archive_id, conflicting_archive_id)
);

DELETE FROM migration_0148_archive_code_conflict;

INSERT INTO migration_0148_archive_code_conflict (conflict_type, archive_id)
SELECT 'invalid_code', id
FROM archive
WHERE code IS NOT NULL AND code <> ''
  AND NOT REGEXP_LIKE(code, '^AR[0-9A-Z]{1,10}$', 'c');

INSERT INTO migration_0148_archive_code_conflict (conflict_type, archive_id, conflicting_archive_id)
SELECT 'backfill_collision', missing.id, existing.id
FROM archive missing
JOIN archive existing
  ON existing.id <> missing.id
 AND existing.code = CONCAT('AR', LPAD(UPPER(CONV(missing.id, 10, 36)), 4, '0'))
WHERE missing.code IS NULL OR missing.code = '';

INSERT INTO migration_0148_archive_code_conflict (conflict_type, archive_id, conflicting_archive_id)
SELECT 'duplicate_code', a.id, b.id
FROM archive a
JOIN archive b ON b.id > a.id AND b.code = a.code
WHERE a.code IS NOT NULL AND a.code <> '';

INSERT INTO migration_0148_archive_code_conflict (conflict_type, archive_id)
SELECT 'missing_unique_index', 0
WHERE NOT EXISTS (
    SELECT 1 FROM information_schema.STATISTICS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'archive'
      AND INDEX_NAME = 'uniq_archive_code' AND NON_UNIQUE = 0
);

INSERT INTO migration_0148_archive_code_conflict (conflict_type, archive_id)
SELECT 'sequence_out_of_range', id
FROM archive
WHERE code IS NOT NULL AND code <> ''
  AND CAST(CONV(SUBSTRING(code, 3), 36, 10) AS UNSIGNED) >= 2147483647;

CREATE TABLE IF NOT EXISTS migration_0148_archive_code_guard (
    singleton      TINYINT NOT NULL PRIMARY KEY,
    conflict_count INT NOT NULL,
    CONSTRAINT migration_0148_no_archive_code_conflicts CHECK (conflict_count = 0)
);

DELETE FROM migration_0148_archive_code_guard;
INSERT INTO migration_0148_archive_code_guard (singleton, conflict_count)
SELECT 1, COUNT(*) FROM migration_0148_archive_code_conflict;

-- 2) Allocation sequence for archive codes. Same AUTO_INCREMENT-table pattern as model_no_seq (0132):
-- INSERT INTO archive_code_seq () VALUES () mints the next number via LAST_INSERT_ID, concurrency-safe.
CREATE TABLE IF NOT EXISTS archive_code_seq (
    id         INT PRIMARY KEY AUTO_INCREMENT,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- Always raise (never lower) the floor above archive PKs, every already persisted base36 code and
-- previous sequence rows. This closes collisions with valid non-id-derived imported codes and heals
-- a partially created sequence whose counter is behind the archive table.
SET @seed := (
    SELECT GREATEST(
        COALESCE((SELECT MAX(id) FROM archive), 0),
        COALESCE((SELECT MAX(CAST(CONV(SUBSTRING(code, 3), 36, 10) AS UNSIGNED))
                  FROM archive WHERE code IS NOT NULL AND code <> ''), 0),
        COALESCE((SELECT MAX(id) FROM archive_code_seq), 0)
    ) + 1
);
SET @sql := CONCAT('ALTER TABLE archive_code_seq AUTO_INCREMENT = ', @seed);
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
