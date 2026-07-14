-- +migrate Up

-- new-flow NF-04 follow-up: a sample gets its own media (photos of the finished sample, outside a
-- fitting session — B-6) and an optional link to the pattern iteration it was cut from (B-3/gap-03).
-- sample_media mirrors fitting_media (full-replace on write); pattern_url/pattern_note are a
-- lightweight snapshot/free-text on the sample row.
--
-- Idempotent: CREATE TABLE IF NOT EXISTS + guarded ADD COLUMN via information_schema (MySQL 8 has no
-- ADD COLUMN IF NOT EXISTS), so a mid-file failure re-runs cleanly from the top.

CREATE TABLE IF NOT EXISTS sample_media (
    id INT AUTO_INCREMENT PRIMARY KEY,
    sample_id INT NOT NULL,
    media_id INT NOT NULL,
    display_order INT NOT NULL DEFAULT 0,
    CONSTRAINT fk_sample_media_sample FOREIGN KEY (sample_id) REFERENCES sample(id) ON DELETE CASCADE,
    CONSTRAINT fk_sample_media_media FOREIGN KEY (media_id) REFERENCES media(id),
    INDEX idx_sample_media_sample (sample_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

SET @need_cols := (SELECT COUNT(*) = 0 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'sample' AND COLUMN_NAME = 'pattern_url');
SET @sql := IF(@need_cols,
    'ALTER TABLE sample
        ADD COLUMN pattern_url VARCHAR(512) NULL,
        ADD COLUMN pattern_note VARCHAR(255) NULL',
    'SELECT 1');
PREPARE s FROM @sql; EXECUTE s; DEALLOCATE PREPARE s;

-- +migrate Down

DROP TABLE IF EXISTS sample_media;
-- (leaves the pattern_* columns; Down is not exercised in prod automigrate)
SELECT 1;
