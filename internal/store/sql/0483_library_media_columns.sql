-- +migrate Up

-- Library metadata on media (media-files wave, D3/D4). The media row, its URLs and its 28 FK / 5 JSON
-- holders stay exactly as they are; these columns only feed the unified library list.
--
-- WHY lib_: SELECT m.* joins (techcard/enrich.go runs tcm.kind, …, m.* under sqlx Unsafe) would let a
-- media column named kind or folder_id silently overwrite the joined table's column. Every new media
-- column carries the lib_ prefix so no m.* join can collide.
--
-- WHY VIRTUAL lib_kind: MySQL computes it from the URLs, so it cannot drift, needs no backfill, is right
-- under an old binary during rollout, is an INSTANT add, and can still be indexed (the index
-- materializes it). The order is video, model, vector, image; video is decided on thumbnail exactly as
-- mediaThumbnailIsVideo does (video.go writes the same URL to every size; a model has a .glb full_size
-- and a raster preview thumbnail). entity.MediaKindOf is the Go twin and must stay identical.
--
-- WHY NO FK on lib_folder_id: with foreign_key_checks=1 adding a FK to an existing large table forces
-- ALGORITHM=COPY, and it would be a retroactive constraint. The library store enforces integrity:
-- folder delete refuses while any row points at the folder; moves take the folder FOR SHARE.
--
-- Every column is nullable with no default, so an older binary keeps inserting. lib_file_name states
-- utf8mb4 explicitly because prod media may still be legacy utf8mb3.
--
-- ALGORITHM=INSTANT IS EXPLICIT: if MySQL cannot add a column instantly (row-version cap of 64) the
-- ALTER fails loudly instead of copying the table. Indexes are INPLACE, LOCK=NONE: the table stays
-- writable while they build.
--
-- IDEMPOTENCY: DDL auto-commits, so each column and each index has its own gate.

SET @col := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'media' AND COLUMN_NAME = 'lib_kind');
SET @ddl := IF(@col = 0,
    'ALTER TABLE media
        ADD COLUMN lib_kind VARCHAR(8) CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci GENERATED ALWAYS AS (CASE WHEN LOWER(SUBSTRING_INDEX(thumbnail,''.'',-1)) IN (''mp4'',''webm'') THEN ''video'' WHEN LOWER(SUBSTRING_INDEX(full_size,''.'',-1)) = ''glb'' THEN ''model'' WHEN LOWER(SUBSTRING_INDEX(full_size,''.'',-1)) = ''svg'' THEN ''vector'' ELSE ''image'' END) VIRTUAL,
        ALGORITHM=INSTANT',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @col := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'media' AND COLUMN_NAME = 'lib_file_name');
SET @ddl := IF(@col = 0,
    'ALTER TABLE media
        ADD COLUMN lib_file_name VARCHAR(255) CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci NULL,
        ALGORITHM=INSTANT',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @col := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'media' AND COLUMN_NAME = 'lib_size_bytes');
SET @ddl := IF(@col = 0,
    'ALTER TABLE media
        ADD COLUMN lib_size_bytes BIGINT NULL,
        ALGORITHM=INSTANT',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @col := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'media' AND COLUMN_NAME = 'lib_uploaded_by_id');
SET @ddl := IF(@col = 0,
    'ALTER TABLE media
        ADD COLUMN lib_uploaded_by_id INT NULL,
        ALGORITHM=INSTANT',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @col := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'media' AND COLUMN_NAME = 'lib_folder_id');
SET @ddl := IF(@col = 0,
    'ALTER TABLE media
        ADD COLUMN lib_folder_id INT NULL,
        ALGORITHM=INSTANT',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @col := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'media' AND COLUMN_NAME = 'lib_source_sha256');
SET @ddl := IF(@col = 0,
    'ALTER TABLE media
        ADD COLUMN lib_source_sha256 CHAR(64) CHARACTER SET ascii NULL,
        ALGORITHM=INSTANT',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @idx := (SELECT COUNT(*) FROM information_schema.STATISTICS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'media' AND INDEX_NAME = 'idx_media_lib_folder');
SET @ddl := IF(@idx = 0,
    'ALTER TABLE media
        ADD INDEX idx_media_lib_folder (lib_folder_id, created_at, id),
        ALGORITHM=INPLACE, LOCK=NONE',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @idx := (SELECT COUNT(*) FROM information_schema.STATISTICS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'media' AND INDEX_NAME = 'idx_media_lib_kind');
SET @ddl := IF(@idx = 0,
    'ALTER TABLE media
        ADD INDEX idx_media_lib_kind (lib_kind, created_at, id),
        ALGORITHM=INPLACE, LOCK=NONE',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @idx := (SELECT COUNT(*) FROM information_schema.STATISTICS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'media' AND INDEX_NAME = 'idx_media_lib_source_sha');
SET @ddl := IF(@idx = 0,
    'ALTER TABLE media
        ADD INDEX idx_media_lib_source_sha (lib_source_sha256),
        ALGORITHM=INPLACE, LOCK=NONE',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- +migrate Down

SET @idx := (SELECT COUNT(*) FROM information_schema.STATISTICS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'media' AND INDEX_NAME = 'idx_media_lib_source_sha');
SET @ddl := IF(@idx > 0,
    'ALTER TABLE media DROP INDEX idx_media_lib_source_sha, ALGORITHM=INPLACE, LOCK=NONE',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @idx := (SELECT COUNT(*) FROM information_schema.STATISTICS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'media' AND INDEX_NAME = 'idx_media_lib_kind');
SET @ddl := IF(@idx > 0,
    'ALTER TABLE media DROP INDEX idx_media_lib_kind, ALGORITHM=INPLACE, LOCK=NONE',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @idx := (SELECT COUNT(*) FROM information_schema.STATISTICS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'media' AND INDEX_NAME = 'idx_media_lib_folder');
SET @ddl := IF(@idx > 0,
    'ALTER TABLE media DROP INDEX idx_media_lib_folder, ALGORITHM=INPLACE, LOCK=NONE',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @col := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'media' AND COLUMN_NAME = 'lib_source_sha256');
SET @ddl := IF(@col = 1,
    'ALTER TABLE media DROP COLUMN lib_source_sha256',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @col := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'media' AND COLUMN_NAME = 'lib_folder_id');
SET @ddl := IF(@col = 1,
    'ALTER TABLE media DROP COLUMN lib_folder_id',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @col := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'media' AND COLUMN_NAME = 'lib_uploaded_by_id');
SET @ddl := IF(@col = 1,
    'ALTER TABLE media DROP COLUMN lib_uploaded_by_id',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @col := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'media' AND COLUMN_NAME = 'lib_size_bytes');
SET @ddl := IF(@col = 1,
    'ALTER TABLE media DROP COLUMN lib_size_bytes',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @col := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'media' AND COLUMN_NAME = 'lib_file_name');
SET @ddl := IF(@col = 1,
    'ALTER TABLE media DROP COLUMN lib_file_name',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @col := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'media' AND COLUMN_NAME = 'lib_kind');
SET @ddl := IF(@col = 1,
    'ALTER TABLE media DROP COLUMN lib_kind',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;
