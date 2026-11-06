-- +migrate Up

-- Two fields of the labels and packaging rework.
--
-- 1. The finished pack has several photos, in display order, the first being the cover. The cover stays
-- where it always was, in tech_card_packaging.packed_media_id; the new table holds the FURTHER photos
-- only (display_order 1..n). So every existing row is already a one-photo list and needs no backfill,
-- and no media id is held twice by one card. The rows hang off the packaging row (CASCADE): a save
-- replaces that row and writes them again, and an older binary's save, which knows only the cover,
-- drops them along with the row it replaces — its card then reads as the one photo it wrote.
-- media_id refuses a media delete like every other media carrier and is registered in
-- store/content/media_usage.go.
--
-- 2. The composition label prints vertical (the ribbon) or horizontal (a wide ribbon).
-- care_orientation: '' = vertical, horizontal. NOT NULL DEFAULT '', so an older binary that names no
-- such column still inserts, and every existing row reads vertical.
--
-- ADDITIVE ONLY: a new table and one defaulted column. The section digest adds either only when it is
-- set past the old value (a second photo, horizontal), so no existing signature goes stale.
--
-- ALGORITHM=INSTANT IS EXPLICIT on the column: if MySQL could not do it instantly the ALTER fails
-- loudly instead of copying the table.
--
-- IDEMPOTENCY: DDL auto-commits, so the table is created only if absent and the column has its own gate.

CREATE TABLE IF NOT EXISTS tech_card_packaging_media (
    id INT NOT NULL PRIMARY KEY AUTO_INCREMENT,
    tech_card_id INT NOT NULL,
    media_id INT NOT NULL COMMENT 'FK media(id): a further photo of the finished pack; the cover is tech_card_packaging.packed_media_id',
    display_order INT NOT NULL DEFAULT 1,
    INDEX idx_tc_packaging_media_card (tech_card_id),
    CONSTRAINT fk_tc_packaging_media_packaging FOREIGN KEY (tech_card_id) REFERENCES tech_card_packaging(tech_card_id) ON DELETE CASCADE,
    CONSTRAINT fk_tc_packaging_media_media FOREIGN KEY (media_id) REFERENCES media(id)
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4 COMMENT 'Further photos of the finished pack, after the cover (0452)';

SET @col := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_care_label' AND COLUMN_NAME = 'care_orientation');
SET @ddl := IF(@col = 0,
    'ALTER TABLE tech_card_care_label
        ADD COLUMN care_orientation VARCHAR(16) NOT NULL DEFAULT '''' COMMENT ''horizontal; empty = vertical'',
        ALGORITHM=INSTANT',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- +migrate Down

DROP TABLE IF EXISTS tech_card_packaging_media;

SET @col_back := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_care_label' AND COLUMN_NAME = 'care_orientation');
SET @ddl := IF(@col_back = 1,
    'ALTER TABLE tech_card_care_label DROP COLUMN care_orientation',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;
