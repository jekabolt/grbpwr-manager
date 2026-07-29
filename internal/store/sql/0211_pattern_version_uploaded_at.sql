-- +migrate Up

-- Gives a cut-pattern PDF a durable revision and a durable upload time (PLM UI gap 5).
--
-- Until now a pattern row carried only (size_id, url, filename, size_bytes). The admin therefore
-- guessed the revision by regexing the FILENAME the factory happened to send ("GR2601-XS-v3.pdf"),
-- and there was no upload time at all: the table's created_at exists, but tech_card_size_pattern is a
-- full-replace child of UpdateTechCard (DELETE + re-INSERT on every card save), so created_at means
-- "last time anyone saved this card", not "when this PDF arrived". It was never read back for exactly
-- that reason.
--
--   version     -- Rev.N of the sheet, per (tech_card, size). Server-assigned MAX+1 for a url it has
--                  not seen on that card before; preserved for a url it has. A client may pin a
--                  specific number (the factory's own numbering) by sending it.
--   uploaded_at -- when the PDF was first attached to this card. Preserved across the full-replace
--                  by matching on url, which is unique per uploaded object (bucket.GetMediaName
--                  embeds a timestamp + random suffix). Server-owned: ignored on write.
--
-- Both are NULLable / zero-defaulted so every pre-existing row stays valid: an old pattern reads as
-- version 0 ("unversioned") with no upload time, which is the truth about it.
--
-- Idempotent: each ADD COLUMN is guarded by an information_schema check (MySQL 8 has no
-- ADD COLUMN IF NOT EXISTS), so a rerun after a mid-file failure is a no-op.

SET @col_exists := (
    SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'tech_card_size_pattern'
      AND COLUMN_NAME = 'version'
);
SET @ddl := IF(@col_exists = 0,
    'ALTER TABLE tech_card_size_pattern ADD COLUMN version INT NOT NULL DEFAULT 0 COMMENT ''Rev.N of this sheet within (tech_card_id, size_id); 0 = unversioned legacy row''',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @col_exists := (
    SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'tech_card_size_pattern'
      AND COLUMN_NAME = 'uploaded_at'
);
SET @ddl := IF(@col_exists = 0,
    'ALTER TABLE tech_card_size_pattern ADD COLUMN uploaded_at TIMESTAMP NULL COMMENT ''when this PDF was first attached; carried across the card full-replace by url''',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- Seed the existing rows so the first save after this migration does not renumber them from 1 and
-- make every historical sheet look brand new. created_at is the best available approximation of the
-- upload time (it is the last save, but it is a real, ordered fact rather than NULL), and the version
-- is assigned in display_order within each (card, size) group.
UPDATE tech_card_size_pattern SET uploaded_at = created_at WHERE uploaded_at IS NULL;

UPDATE tech_card_size_pattern p
JOIN (
    SELECT id, ROW_NUMBER() OVER (PARTITION BY tech_card_id, size_id ORDER BY display_order, id) AS rn
    FROM tech_card_size_pattern
) r ON r.id = p.id
SET p.version = r.rn
WHERE p.version = 0;

-- +migrate Down
SET @col_exists := (
    SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'tech_card_size_pattern'
      AND COLUMN_NAME = 'version'
);
SET @ddl := IF(@col_exists = 1, 'ALTER TABLE tech_card_size_pattern DROP COLUMN version', 'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @col_exists := (
    SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'tech_card_size_pattern'
      AND COLUMN_NAME = 'uploaded_at'
);
SET @ddl := IF(@col_exists = 1, 'ALTER TABLE tech_card_size_pattern DROP COLUMN uploaded_at', 'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;
