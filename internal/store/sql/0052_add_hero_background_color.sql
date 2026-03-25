-- +migrate Up
-- Description: Singleton row for storefront hero section background color (CSS color string).

CREATE TABLE hero_background_color (
    id INT NOT NULL PRIMARY KEY,
    color VARCHAR(128) NOT NULL DEFAULT ''
) COMMENT 'Singleton hero background color; id must be 1';

INSERT IGNORE INTO hero_background_color (id, color) VALUES (1, '');

-- +migrate Down

DROP TABLE hero_background_color;
