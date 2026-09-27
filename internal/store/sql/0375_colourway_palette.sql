-- T45 (27.09) — a colourway's colour is an ordered palette plus a per-language name.
-- Owner's decisions (06-COLOURWAYS-RESEARCH.md §7), items 3, 5 and 6.
-- ---
-- product_colour — the palette. One row per colour, position 0 = the main colour, 1…8 colours
-- per colourway (enforced on write, in Go). Each colour is a Pantone code OR a free label, with a
-- hex for the screen preview. The widths are the ones product already uses for the same facts
-- (pantone VARCHAR(64), pantone_system VARCHAR(8), dev_hex 7 characters), so the main colour mirrors
-- into product.pantone / pantone_system / dev_hex without truncation. NULL, not the empty string,
-- for a fact nobody stated.
-- ---
-- product_colour_name_i18n — the colourway's name per storefront language, keyed by the house
-- translation key language_id (as product_translation is). A language without a row reads the
-- operator's name, product.dev_name.
-- ---
-- ZERO PALETTE ROWS IS A LEGAL, PERMANENT STATE — the legacy single-colour colourway. Nothing is
-- backfilled here: every existing colourway keeps reading its colour from pantone / dev_hex /
-- the dictionary family exactly as before.
-- ---
-- Both tables cascade with the product: a palette and a translation mean nothing without their
-- colourway, and DeleteColorwayByID must not grow a new blocker for them. No CHECK constraints
-- (the palette rules are write-path validation). Idempotent — IF NOT EXISTS / IF EXISTS — so a
-- re-run after a mid-file failure is a no-op.

-- +migrate Up

CREATE TABLE IF NOT EXISTS product_colour (
    product_id INT NOT NULL,
    position TINYINT UNSIGNED NOT NULL COMMENT '0 = the main colour',
    label VARCHAR(64) NULL COMMENT 'free words; required when pantone is NULL',
    hex CHAR(7) NULL COMMENT 'screen preview #RRGGBB',
    pantone VARCHAR(64) NULL COMMENT 'Pantone code as typed, free text',
    pantone_system VARCHAR(8) NULL COMMENT 'TCX, TPG, C and the like, only with pantone',
    PRIMARY KEY (product_id, position),
    CONSTRAINT fk_product_colour_product FOREIGN KEY (product_id) REFERENCES product(id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT 'Colourway palette (T45): ordered colours, position 0 is the main one';

CREATE TABLE IF NOT EXISTS product_colour_name_i18n (
    product_id INT NOT NULL,
    language_id INT NOT NULL,
    name VARCHAR(128) NOT NULL,
    PRIMARY KEY (product_id, language_id),
    KEY idx_product_colour_name_i18n_language (language_id),
    CONSTRAINT fk_product_colour_name_i18n_product FOREIGN KEY (product_id) REFERENCES product(id) ON DELETE CASCADE,
    CONSTRAINT fk_product_colour_name_i18n_language FOREIGN KEY (language_id) REFERENCES language(id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT 'Colourway name per storefront language (T45); missing = the operator name';

-- +migrate Down

DROP TABLE IF EXISTS product_colour_name_i18n;
DROP TABLE IF EXISTS product_colour;
