-- +migrate Up

-- ListObjectsPaged orders the media library by created_at with id as its deterministic
-- tie-breaker. The date scrubber turns month counts into offsets in that same ordering, so
-- increasingly old pages must not require a full-table filesort before applying the offset.
-- One ascending composite index serves both the ASC and DESC forms by forward/reverse scan.
SET @need := (SELECT COUNT(*) = 0 FROM information_schema.STATISTICS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'media'
      AND INDEX_NAME = 'idx_media_created_at_id');
SET @sql := IF(@need,
    'ALTER TABLE media ADD INDEX idx_media_created_at_id (created_at, id)',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

-- +migrate Down

SET @have := (SELECT COUNT(*) FROM information_schema.STATISTICS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'media'
      AND INDEX_NAME = 'idx_media_created_at_id');
SET @sql := IF(@have > 0,
    'ALTER TABLE media DROP INDEX idx_media_created_at_id',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;
