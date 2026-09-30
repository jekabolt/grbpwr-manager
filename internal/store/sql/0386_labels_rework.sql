-- Labels rework (30.09), I-01 — the composition label record, garment labels and packaging items.
--
-- OWNER, 30.09 (tmp/plans/labels-rework/00-OWNER-SPEC.md R-01..R-09): the composition label
-- (составник) becomes its own always-present block whose every line is editable in place; the
-- other labels and the packaging items work like construction aspects — a known list plus custom
-- names — and each carries a mockup, placement, attachment, folding and the rest.
--
-- SEVEN NEW TABLES, NOTHING ELSE MOVES:
--   - tech_card_care_label           1:1 with the card: logo override, care prose / back caption /
--                                    address overrides, QR preset + template. No row = everything
--                                    derived (brand mark, dictionary prose, storefront QR, constants).
--   - tech_card_care_label_colorway  per-colourway overrides (colour name on the ribbon).
--   - tech_card_care_label_fiber     per-colourway composition override rows; an absent set means
--                                    «derived from the BOM». Rows, not JSON: the FK to fiber keeps the
--                                    translations reachable and the digest projection canonical.
--   - tech_card_garment_label (+ _media)   the labels other than the composition label.
--   - tech_card_packaging_item (+ _media)  polybag, tissue, insert card, … (the carton facts stay on
--                                          tech_card_packaging, where shipping reads them).
--
-- OLD LABELS ARE NOT CONVERTED (owner decision D-06, «на старые лейблы похуй»). The tables are
-- created EMPTY: no copy from tech_card_label, none from tech_card_packaging.polybag / bag_sticker /
-- inserts. Those legacy objects stay in the schema untouched — the pre-wave binary still names them
-- in its INSERTs, so the drop rides a SECOND commit after prod runs this binary (I-19,
-- README-pending-drops.md).
--
-- KEYS ARE FREEFORM VARCHAR(64), NO CHECK (the lesson of 0070's label_type regex): known-ness is the
-- client's constant, exactly as detail_key of the construction aspects. The two CHECKs below guard
-- plain numeric ranges on tables born empty, so they are not retroactive and cannot halt a deploy.
--
-- CHARSET-only table options (DEFAULT CHARSET = utf8mb4, no COLLATE), because fiber_code is a string
-- FK to fiber(code), which 0167 declared the same way — a foreign key between VARCHARs needs the same
-- collation on both sides.
--
-- ONE STATEMENT PER CREATE, each IF NOT EXISTS: MySQL DDL auto-commits, so a mid-file failure re-runs
-- the whole file on the next boot, and every statement here is then a no-op.
--
-- THE OLD BINARY ON THE NEW SCHEMA IS SAFE: it never reads or writes these tables.

-- +migrate Up

CREATE TABLE IF NOT EXISTS tech_card_care_label (
    tech_card_id INT NOT NULL PRIMARY KEY,
    logo_media_id INT NULL COMMENT 'FK media(id): SVG logo override; NULL = the brand mark',
    care_prose_lines TEXT NULL COMMENT 'newline-separated EN care prose override; NULL = dictionary short_prose',
    qr_preset VARCHAR(16) NOT NULL DEFAULT 'storefront' COMMENT 'storefront | custom | fixed (closed in Go)',
    qr_template VARCHAR(512) NULL COMMENT 'QR template (custom) or URL (fixed)',
    back_caption_lines TEXT NULL COMMENT 'newline-separated override of the back caption; NULL = default',
    address_lines TEXT NULL COMMENT 'newline-separated override of the company address; NULL = constant',
    CONSTRAINT fk_tc_care_label_card FOREIGN KEY (tech_card_id) REFERENCES tech_card(id) ON DELETE CASCADE,
    CONSTRAINT fk_tc_care_label_logo FOREIGN KEY (logo_media_id) REFERENCES media(id)
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4 COMMENT 'Composition (care) label overrides, 1:1 with the tech card; no row = all derived';

CREATE TABLE IF NOT EXISTS tech_card_care_label_colorway (
    id INT NOT NULL PRIMARY KEY AUTO_INCREMENT,
    tech_card_id INT NOT NULL,
    colorway_id INT NOT NULL COMMENT 'FK product(id): a colourway of this style',
    colour_name VARCHAR(64) NULL COMMENT 'override of the colour name printed on the ribbon; NULL = derived',
    display_order INT NOT NULL DEFAULT 0,
    CONSTRAINT uniq_tc_care_label_colorway UNIQUE (tech_card_id, colorway_id),
    CONSTRAINT fk_tc_care_label_colorway_label FOREIGN KEY (tech_card_id) REFERENCES tech_card_care_label(tech_card_id) ON DELETE CASCADE,
    CONSTRAINT fk_tc_care_label_colorway_product FOREIGN KEY (colorway_id) REFERENCES product(id) ON DELETE CASCADE
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4 COMMENT 'Per-colourway overrides of the composition label';

CREATE TABLE IF NOT EXISTS tech_card_care_label_fiber (
    id INT NOT NULL PRIMARY KEY AUTO_INCREMENT,
    care_label_colorway_id INT NOT NULL,
    label_part VARCHAR(16) NOT NULL COMMENT 'TechCardBomLabelPart token (shell, body_lining, …; closed in Go)',
    fiber_code VARCHAR(8) NOT NULL,
    pct TINYINT UNSIGNED NOT NULL,
    display_order INT NOT NULL DEFAULT 0,
    INDEX idx_tc_care_label_fiber_colorway (care_label_colorway_id),
    CONSTRAINT chk_tc_care_label_fiber_pct CHECK (pct BETWEEN 1 AND 100),
    CONSTRAINT fk_tc_care_label_fiber_colorway FOREIGN KEY (care_label_colorway_id) REFERENCES tech_card_care_label_colorway(id) ON DELETE CASCADE,
    CONSTRAINT fk_tc_care_label_fiber_fiber FOREIGN KEY (fiber_code) REFERENCES fiber(code) ON DELETE RESTRICT
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4 COMMENT 'Composition override of one colourway, per label part; absent set = derived from the BOM';

CREATE TABLE IF NOT EXISTS tech_card_garment_label (
    id INT NOT NULL PRIMARY KEY AUTO_INCREMENT,
    tech_card_id INT NOT NULL,
    label_key VARCHAR(64) NOT NULL COMMENT 'known key (brand/size/flag/hangtag/barcode/special) or a custom name; freeform',
    placement VARCHAR(255) NULL,
    attachment VARCHAR(255) NULL,
    folding VARCHAR(255) NULL,
    size VARCHAR(64) NULL,
    qty_per_garment INT NOT NULL DEFAULT 1,
    bom_item_id INT NULL COMMENT 'FK tech_card_bom_item: the BOM line the label is bought as',
    note TEXT NULL,
    display_order INT NOT NULL DEFAULT 0,
    INDEX idx_tc_garment_label_card (tech_card_id),
    CONSTRAINT chk_tc_garment_label_qty CHECK (qty_per_garment >= 1),
    CONSTRAINT fk_tc_garment_label_card FOREIGN KEY (tech_card_id) REFERENCES tech_card(id) ON DELETE CASCADE,
    CONSTRAINT fk_tc_garment_label_bom_item FOREIGN KEY (bom_item_id) REFERENCES tech_card_bom_item(id) ON DELETE SET NULL
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4 COMMENT 'Garment labels other than the composition label (labels rework, 0386)';

CREATE TABLE IF NOT EXISTS tech_card_garment_label_media (
    id INT NOT NULL PRIMARY KEY AUTO_INCREMENT,
    label_id INT NOT NULL,
    media_id INT NOT NULL COMMENT 'FK media(id): the mockup',
    display_order INT NOT NULL DEFAULT 0,
    INDEX idx_tc_garment_label_media_label (label_id),
    CONSTRAINT fk_tc_garment_label_media_label FOREIGN KEY (label_id) REFERENCES tech_card_garment_label(id) ON DELETE CASCADE,
    CONSTRAINT fk_tc_garment_label_media_media FOREIGN KEY (media_id) REFERENCES media(id)
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4 COMMENT 'Mockups of a garment label';

CREATE TABLE IF NOT EXISTS tech_card_packaging_item (
    id INT NOT NULL PRIMARY KEY AUTO_INCREMENT,
    tech_card_id INT NOT NULL,
    item_key VARCHAR(64) NOT NULL COMMENT 'known key (polybag/tissue/sticker/…) or a custom name; freeform',
    item_usage VARCHAR(255) NULL COMMENT 'where / how the item is used (USAGE is a reserved word)',
    packing VARCHAR(255) NULL COMMENT 'folding / packing instruction',
    size VARCHAR(64) NULL,
    qty_per_garment INT NOT NULL DEFAULT 1,
    bom_item_id INT NULL COMMENT 'FK tech_card_bom_item: the BOM line the item is bought as',
    note TEXT NULL,
    display_order INT NOT NULL DEFAULT 0,
    INDEX idx_tc_packaging_item_card (tech_card_id),
    CONSTRAINT chk_tc_packaging_item_qty CHECK (qty_per_garment >= 1),
    CONSTRAINT fk_tc_packaging_item_card FOREIGN KEY (tech_card_id) REFERENCES tech_card(id) ON DELETE CASCADE,
    CONSTRAINT fk_tc_packaging_item_bom_item FOREIGN KEY (bom_item_id) REFERENCES tech_card_bom_item(id) ON DELETE SET NULL
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4 COMMENT 'Packaging items of a style (labels rework, 0386); carton facts stay on tech_card_packaging';

CREATE TABLE IF NOT EXISTS tech_card_packaging_item_media (
    id INT NOT NULL PRIMARY KEY AUTO_INCREMENT,
    item_id INT NOT NULL,
    media_id INT NOT NULL COMMENT 'FK media(id): the mockup',
    display_order INT NOT NULL DEFAULT 0,
    INDEX idx_tc_packaging_item_media_item (item_id),
    CONSTRAINT fk_tc_packaging_item_media_item FOREIGN KEY (item_id) REFERENCES tech_card_packaging_item(id) ON DELETE CASCADE,
    CONSTRAINT fk_tc_packaging_item_media_media FOREIGN KEY (media_id) REFERENCES media(id)
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4 COMMENT 'Mockups of a packaging item';

-- +migrate Down
--
-- Rolls back only together with the code: the tech card save writes these tables. Children first.

DROP TABLE IF EXISTS tech_card_packaging_item_media;

DROP TABLE IF EXISTS tech_card_packaging_item;

DROP TABLE IF EXISTS tech_card_garment_label_media;

DROP TABLE IF EXISTS tech_card_garment_label;

DROP TABLE IF EXISTS tech_card_care_label_fiber;

DROP TABLE IF EXISTS tech_card_care_label_colorway;

DROP TABLE IF EXISTS tech_card_care_label;
