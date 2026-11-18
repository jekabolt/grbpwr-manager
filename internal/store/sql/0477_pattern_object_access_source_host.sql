-- Where a pattern object lives, recorded on its access row (teams wave, lane B8).
--
-- A team database copied from another installation keeps pattern urls on that installation's file
-- host (BUCKET_S3_READONLY_HOSTS). The tokenized read path /api/p/{token} resolved a row by object
-- key alone, so it could not tell a copied object from one of ours with the same key, and it had to
-- guess the host. source_host makes the host part of the identity:
--   ''      the object is in this server's own bucket (every existing row: rows written before this
--           column exist only on servers without read-only hosts, so '' is correct for all of them);
--   a host  the object is a public object on that read-only host, linked there with no bucket probe.
-- No token minted before this migration can name a read-only object: the read-only path
-- (BUCKET_S3_READONLY_HOSTS) never ran on any server before it, so '' is the truth for every existing
-- row and no pre-0477 token needs invalidating.
--
-- NOT NULL DEFAULT '' rather than NULL on purpose: the unique key below must collapse concurrent
-- ensures of the same object into one row, and MySQL lets any number of NULLs through a UNIQUE index.
-- The ADD COLUMN fills '' into every existing row, so the new unique key cannot meet a duplicate:
-- object_key alone was already unique.
--
-- One ALTER (column + key swap) so there is no moment without a unique key; MySQL 8 DDL is atomic,
-- and the guard on the column makes a retry a no-op. Safe under ANSI_QUOTES: single-quoted literals
-- only, bare identifiers. ascii_bin: hosts are stored lower-case and compared byte for byte, and an
-- ascii column keeps the composite key far under the 3072-byte index limit.

-- +migrate Up

-- migrationlint:allow-retroactive the column is added in this same statement with DEFAULT '' for every row and object_key is already unique, so (object_key, source_host) has no duplicates to reject
SET @has_col = (SELECT COUNT(*) FROM information_schema.COLUMNS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'pattern_object_access' AND COLUMN_NAME = 'source_host');
SET @ddl = IF(@has_col = 0, 'ALTER TABLE pattern_object_access ADD COLUMN source_host VARCHAR(255) CHARACTER SET ascii COLLATE ascii_bin NOT NULL DEFAULT '''' COMMENT ''empty = own bucket; else the read-only host of the object'' AFTER object_key, DROP INDEX uq_pattern_object_access_key, ADD CONSTRAINT uq_pattern_object_access_key_host UNIQUE (object_key, source_host)', 'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- +migrate Down

-- Down never deletes a row: a read-only row carries its own revocation epoch and expiry policy, and a
-- DELETE followed by a failing ALTER could not be rolled back (DDL commits implicitly). While any row
-- has a non-empty source_host, Down fails on purpose — the error names the missing table
-- refuse_0477_down__read_only_source_host_rows_exist (SIGNAL cannot be PREPAREd); remove or move those
-- rows deliberately, then run Down again. With none, it restores the key-only unique key and drops the
-- column. Idempotent: the column guard makes a finished Down a no-op.
SET @has_col = (SELECT COUNT(*) FROM information_schema.COLUMNS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'pattern_object_access' AND COLUMN_NAME = 'source_host');
SET @ro_rows = 0;
SET @ddl = IF(@has_col > 0, 'SELECT COUNT(*) INTO @ro_rows FROM pattern_object_access WHERE source_host <> ''''', 'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;
SET @ddl = IF(@ro_rows > 0, 'SELECT 1 FROM refuse_0477_down__read_only_source_host_rows_exist', 'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;
SET @ddl = IF(@has_col > 0, 'ALTER TABLE pattern_object_access DROP INDEX uq_pattern_object_access_key_host, ADD CONSTRAINT uq_pattern_object_access_key UNIQUE (object_key), DROP COLUMN source_host', 'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;
