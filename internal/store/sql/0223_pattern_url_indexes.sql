-- +migrate Up
-- Orphan detection for pattern objects (audit #61) filters both pattern tables by url at the end of
-- every card/fitting write transaction. Without an index that is a full scan under SERIALIZABLE,
-- inviting lock waits between concurrent saves. Prefix length keeps the key within limits for the
-- utf8 TEXT/VARCHAR url columns.
SET @has_idx := (SELECT COUNT(*) > 0 FROM information_schema.STATISTICS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_size_pattern' AND INDEX_NAME = 'idx_tcsp_url');
SET @sql := IF(@has_idx, 'SELECT 1', 'ALTER TABLE tech_card_size_pattern ADD INDEX idx_tcsp_url (url(191))');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

SET @has_idx := (SELECT COUNT(*) > 0 FROM information_schema.STATISTICS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'fitting_pattern' AND INDEX_NAME = 'idx_fp_url');
SET @sql := IF(@has_idx, 'SELECT 1', 'ALTER TABLE fitting_pattern ADD INDEX idx_fp_url (url(191))');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

-- +migrate Down
SELECT 1;
