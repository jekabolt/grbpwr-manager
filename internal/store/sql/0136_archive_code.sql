-- +migrate Up

-- SKU redesign task 32: give the archive/timeline its own stable, immutable public code and take
-- archive.id off the public surface. Until now the timeline URL embedded the raw id
-- (/timeline/{title}/{tag}/{id}) and resolution parsed the id back out of the slug tail — brittle,
-- and it leaks the primary key. `code` is a short, URL-safe, immutable token assigned once at
-- creation; the public URL becomes /timeline/{pretty}-{code} and the frontend resolves by code.
--
-- Format: 'AR' + base36(id) upper-cased, left-padded with '0' to width 4 (e.g. id 12 -> AR000C).
-- Backfilling from id is a one-time deterministic seed — after this the code is frozen and never
-- tracks id again. New rows get the identical code shape from entity.ArchiveCodeFromID at insert.
--
-- Idempotent: guarded ADD COLUMN / backfill / UNIQUE via information_schema (multi-line
-- PREPARE/EXECUTE/DEALLOCATE — a single line trips 1064 on the managed DSN, see 0124). The column is
-- added NULL, backfilled, then the UNIQUE index is added; the backfill only fills still-NULL rows so a
-- re-run is a no-op. NOT NULL is enforced by the app (every insert sets code), not the schema, so a
-- mid-file re-run never hits a NULL-violation on a half-applied column.

-- 1) Column (nullable; filled by backfill then by the app going forward).
SET @need_col := (SELECT COUNT(*) = 0 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'archive' AND COLUMN_NAME = 'code');
SET @sql := IF(@need_col,
    'ALTER TABLE archive ADD COLUMN code VARCHAR(12) NULL DEFAULT NULL',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

-- 2) Backfill deterministically from id. CONV(id,10,36) yields upper-case base36; LPAD to 4 keeps
-- short ids readable. Only still-NULL rows, so a re-run is a no-op.
UPDATE archive
    SET code = CONCAT('AR', LPAD(UPPER(CONV(id, 10, 36)), 4, '0'))
    WHERE code IS NULL;

-- 3) UNIQUE index (named explicitly so it is stable across history).
SET @need_idx := (SELECT COUNT(*) = 0 FROM information_schema.STATISTICS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'archive' AND INDEX_NAME = 'uniq_archive_code');
SET @sql := IF(@need_idx,
    'ALTER TABLE archive ADD UNIQUE INDEX uniq_archive_code (code)',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;
