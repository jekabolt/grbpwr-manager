-- +migrate Up

-- The picture next to the style size chart and the free lines the user draws on it. One picture per
-- card: an icon from the client library (icon_key), one of the card's sketches or an uploaded image
-- (media_id). Coordinates are normalised to the picture box, 0..1. Lines predrawn by an icon are not
-- stored; a free line names its point by measurement_name_id, NULL until the user picks it. Both
-- tables are new, so the CHECKs cannot reject an existing row.

CREATE TABLE IF NOT EXISTS tech_card_size_chart_picture (
    tech_card_id INT NOT NULL PRIMARY KEY,
    source ENUM('icon', 'sketch', 'upload') NOT NULL,
    icon_key VARCHAR(64) NOT NULL DEFAULT '',
    media_id INT NULL,
    CONSTRAINT fk_tcscpic_card FOREIGN KEY (tech_card_id) REFERENCES tech_card(id) ON DELETE CASCADE,
    CONSTRAINT fk_tcscpic_media FOREIGN KEY (media_id) REFERENCES media(id) ON DELETE SET NULL,
    INDEX idx_tcscpic_media (media_id)
) ENGINE=InnoDB COMMENT 'Picture next to the style size chart';

CREATE TABLE IF NOT EXISTS tech_card_size_chart_line (
    id INT PRIMARY KEY AUTO_INCREMENT,
    tech_card_id INT NOT NULL,
    measurement_name_id INT NULL,
    x1 DECIMAL(6,5) NOT NULL,
    y1 DECIMAL(6,5) NOT NULL,
    x2 DECIMAL(6,5) NOT NULL,
    y2 DECIMAL(6,5) NOT NULL,
    position INT NOT NULL,
    CONSTRAINT fk_tcscl_card FOREIGN KEY (tech_card_id) REFERENCES tech_card(id) ON DELETE CASCADE,
    CONSTRAINT fk_tcscl_name FOREIGN KEY (measurement_name_id) REFERENCES measurement_name(id) ON DELETE RESTRICT,
    CONSTRAINT chk_tcscl_coords CHECK (
        x1 BETWEEN 0 AND 1 AND y1 BETWEEN 0 AND 1 AND x2 BETWEEN 0 AND 1 AND y2 BETWEEN 0 AND 1
    ),
    INDEX idx_tcscl_card (tech_card_id, position),
    INDEX idx_tcscl_name (measurement_name_id)
) ENGINE=InnoDB COMMENT 'Free lines drawn on the size chart picture';

-- Points the icon library draws that the dictionary did not have yet (16 seed + 15 from 0457 cover
-- the rest; the icon line front-rise maps to rise on the client).
INSERT IGNORE INTO measurement_name (name) VALUES
    ('brim'),
    ('flap-width'),
    ('foot-length'),
    ('handle-drop'),
    ('head'),
    ('heel-height'),
    ('hood-height'),
    ('peak'),
    ('shaft-height'),
    ('shaft-width'),
    ('strap-drop'),
    ('strap-length'),
    ('underbust');

-- +migrate Down

DROP TABLE IF EXISTS tech_card_size_chart_line;
DROP TABLE IF EXISTS tech_card_size_chart_picture;
