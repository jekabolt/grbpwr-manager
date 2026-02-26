-- +migrate Up
-- Migration to convert archive main_media_id from single to multiple
-- This creates a new junction table for main media items

-- Create the archive_main_media junction table
CREATE TABLE archive_main_media (
    id INT PRIMARY KEY AUTO_INCREMENT,
    archive_id INT NOT NULL,
    media_id INT NOT NULL,
    display_order INT NOT NULL DEFAULT 0,
    FOREIGN KEY (archive_id) REFERENCES archive(id) ON DELETE CASCADE,
    FOREIGN KEY (media_id) REFERENCES media(id),
    UNIQUE KEY unique_archive_media (archive_id, media_id)
) COMMENT 'Junction table for archive main media (multiple media per archive)';

-- Migrate existing main_media_id data to the new table
INSERT INTO archive_main_media (archive_id, media_id, display_order)
SELECT id, main_media_id, 0
FROM archive
WHERE main_media_id IS NOT NULL;

-- Drop the old main_media_id column
ALTER TABLE archive DROP FOREIGN KEY archive_ibfk_1;
ALTER TABLE archive DROP COLUMN main_media_id;

-- Create index for better query performance
CREATE INDEX idx_archive_main_media_archive_id ON archive_main_media(archive_id);
CREATE INDEX idx_archive_main_media_media_id ON archive_main_media(media_id);

-- +migrate Down
ALTER TABLE archive ADD COLUMN main_media_id INT NULL;

UPDATE archive a
INNER JOIN (
    SELECT archive_id, SUBSTRING_INDEX(GROUP_CONCAT(media_id ORDER BY display_order, id), ',', 1) AS media_id
    FROM archive_main_media
    GROUP BY archive_id
) x ON a.id = x.archive_id
SET a.main_media_id = x.media_id;

ALTER TABLE archive ADD FOREIGN KEY (main_media_id) REFERENCES media(id);

DROP TABLE archive_main_media;
