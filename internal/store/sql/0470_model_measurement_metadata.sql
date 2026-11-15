-- Optional acquisition provenance for each sparse body measurement. Existing
-- rows remain NULL/legacy. The three columns are an all-or-none V1 tuple; they
-- do not assert that the number or its landmark protocol is accurate.
--
-- Each ALTER is independently guarded so a retry after a partially committed
-- MySQL DDL sequence completes the missing columns instead of failing.

-- +migrate Up

SET @has_col = (SELECT COUNT(*) FROM information_schema.COLUMNS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'model_measurement' AND COLUMN_NAME = 'metadata_schema_version');
SET @ddl = IF(@has_col = 0, 'ALTER TABLE model_measurement ADD COLUMN metadata_schema_version SMALLINT UNSIGNED NULL COMMENT ''1 = BodyMeasurementMetadataV1; NULL = legacy/no provenance'', ALGORITHM=INSTANT', 'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @has_col = (SELECT COUNT(*) FROM information_schema.COLUMNS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'model_measurement' AND COLUMN_NAME = 'measurement_source');
SET @ddl = IF(@has_col = 0, 'ALTER TABLE model_measurement ADD COLUMN measurement_source VARCHAR(24) COLLATE utf8mb4_bin NULL COMMENT ''manual_tape|three_d_scan|imported|derived; NULL = legacy/no provenance'', ALGORITHM=INSTANT', 'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @has_col = (SELECT COUNT(*) FROM information_schema.COLUMNS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'model_measurement' AND COLUMN_NAME = 'measurement_protocol_id');
SET @ddl = IF(@has_col = 0, 'ALTER TABLE model_measurement ADD COLUMN measurement_protocol_id VARCHAR(128) COLLATE utf8mb4_bin NULL COMMENT ''caller-owned stable acquisition protocol id; NULL = legacy/no provenance'', ALGORITHM=INSTANT', 'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- migrationlint:allow-retroactive all three columns are added nullable above and no statement writes them, so every existing row satisfies the all-NULL branch
SET @has_chk = (SELECT COUNT(*) FROM information_schema.TABLE_CONSTRAINTS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'model_measurement' AND CONSTRAINT_NAME = 'chk_model_measurement_metadata_v1');
SET @ddl = IF(@has_chk = 0, 'ALTER TABLE model_measurement ADD CONSTRAINT chk_model_measurement_metadata_v1 CHECK ((metadata_schema_version IS NULL AND measurement_source IS NULL AND measurement_protocol_id IS NULL) OR (metadata_schema_version IS NOT NULL AND measurement_source IS NOT NULL AND measurement_protocol_id IS NOT NULL AND metadata_schema_version = 1 AND measurement_source IN (''manual_tape'', ''three_d_scan'', ''imported'', ''derived'') AND CHAR_LENGTH(measurement_protocol_id) BETWEEN 1 AND 128 AND measurement_protocol_id = TRIM(measurement_protocol_id)))', 'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- +migrate Down

SET @has_chk = (SELECT COUNT(*) FROM information_schema.TABLE_CONSTRAINTS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'model_measurement' AND CONSTRAINT_NAME = 'chk_model_measurement_metadata_v1');
SET @ddl = IF(@has_chk > 0, 'ALTER TABLE model_measurement DROP CHECK chk_model_measurement_metadata_v1', 'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @has_col = (SELECT COUNT(*) FROM information_schema.COLUMNS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'model_measurement' AND COLUMN_NAME = 'measurement_protocol_id');
SET @ddl = IF(@has_col > 0, 'ALTER TABLE model_measurement DROP COLUMN measurement_protocol_id', 'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @has_col = (SELECT COUNT(*) FROM information_schema.COLUMNS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'model_measurement' AND COLUMN_NAME = 'measurement_source');
SET @ddl = IF(@has_col > 0, 'ALTER TABLE model_measurement DROP COLUMN measurement_source', 'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @has_col = (SELECT COUNT(*) FROM information_schema.COLUMNS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'model_measurement' AND COLUMN_NAME = 'metadata_schema_version');
SET @ddl = IF(@has_col > 0, 'ALTER TABLE model_measurement DROP COLUMN metadata_schema_version', 'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;
