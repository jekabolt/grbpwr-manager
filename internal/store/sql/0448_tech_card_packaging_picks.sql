-- +migrate Up

-- A packaging item can name the thing it IS: a catalog material of class packaging, or an auxiliary
-- tech card (at most one, checked on write). per_order says the item's quantity counts per order
-- instead of per garment. The packaging sheet gains the finished pack's size and photo.
--
-- ADDITIVE ONLY. Every column is born NULL on every row: NULL = no pick, per garment, no size, no
-- photo. No backfill, no CHECK. An empty value adds no pair to the section digest (packagingTails,
-- packagingItemsTail), so no existing signature goes stale.
--
-- THE COLUMNS GO INSTANT, THE FOREIGN KEYS DO NOT. ADD COLUMN names ALGORITHM=INSTANT so a table MySQL
-- could not alter instantly fails loudly. ADD FOREIGN KEY under foreign_key_checks=1 is a table copy,
-- which is why no ALGORITHM is named for it. These two tables hold a handful of rows per card (the
-- beta count on 03.11: tech_card_packaging_item 0, tech_card_packaging 11), so the copy is short, and
-- the startup lock_wait_timeout turns a busy table into a loud failed start, not a long block.
--
-- migrationlint:allow-retroactive every FK column is added NULL by this same file and nothing writes it before the FK lands, so no existing row can be an orphan.
--
-- ON DELETE SET NULL on both picks: a material or a card going away leaves the item as plain words
-- (and moves the packaging digest, which is right). packed_media_id refuses the delete like the other
-- media carriers, and is registered in store/content/media_usage.go.
--
-- PER COLOURWAY: tech_card_packaging_item_colorway names the colourways an item applies to; no rows =
-- all colourways. A colourway is a product row and a card save does not recreate products, but it does
-- recreate the items, so the rows go with the item (CASCADE) and the save writes them again. Deleting a
-- colourway takes its rows (CASCADE, like tech_card_care_label_colorway). A new table holds no old row.
-- No uniqueness on the pick: one material may sit on two items with different colourways and quantities.
--
-- IDEMPOTENCY: DDL auto-commits, so every column and every key has its own information_schema gate, and
-- the new table is created only if it is not there yet.
--
-- ROLLBACK: an older binary names its columns on read and on write, so it survives this schema. Its
-- full replace of the items loses the picks, the same as any older binary and a newer field.

SET @pick_material_col := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_packaging_item'
      AND COLUMN_NAME = 'material_id');
SET @ddl := IF(@pick_material_col = 0,
    'ALTER TABLE tech_card_packaging_item
        ADD COLUMN material_id INT NULL COMMENT ''FK material(id), class packaging: the picked material; NULL = none'',
        ALGORITHM=INSTANT',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @pick_aux_col := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_packaging_item'
      AND COLUMN_NAME = 'aux_tech_card_id');
SET @ddl := IF(@pick_aux_col = 0,
    'ALTER TABLE tech_card_packaging_item
        ADD COLUMN aux_tech_card_id INT NULL COMMENT ''FK tech_card(id), purpose auxiliary: the picked card; NULL = none'',
        ALGORITHM=INSTANT',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @per_order_col := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_packaging_item'
      AND COLUMN_NAME = 'per_order');
SET @ddl := IF(@per_order_col = 0,
    'ALTER TABLE tech_card_packaging_item
        ADD COLUMN per_order TINYINT(1) NULL COMMENT ''1 = qty_per_garment counts per order; NULL or 0 = per garment'',
        ALGORITHM=INSTANT',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @packed_size_col := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_packaging'
      AND COLUMN_NAME = 'packed_size');
SET @ddl := IF(@packed_size_col = 0,
    'ALTER TABLE tech_card_packaging
        ADD COLUMN packed_size VARCHAR(64) NULL COMMENT ''size of the finished pack, free text'',
        ALGORITHM=INSTANT',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @packed_media_col := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_packaging'
      AND COLUMN_NAME = 'packed_media_id');
SET @ddl := IF(@packed_media_col = 0,
    'ALTER TABLE tech_card_packaging
        ADD COLUMN packed_media_id INT NULL COMMENT ''FK media(id): photo of the finished pack; NULL = none'',
        ALGORITHM=INSTANT',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @pick_material_fk := (SELECT COUNT(*) FROM information_schema.TABLE_CONSTRAINTS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_packaging_item'
      AND CONSTRAINT_NAME = 'fk_tc_packaging_item_material');
SET @ddl := IF(@pick_material_fk = 0,
    'ALTER TABLE tech_card_packaging_item
        ADD CONSTRAINT fk_tc_packaging_item_material FOREIGN KEY (material_id) REFERENCES material(id) ON DELETE SET NULL',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @pick_aux_fk := (SELECT COUNT(*) FROM information_schema.TABLE_CONSTRAINTS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_packaging_item'
      AND CONSTRAINT_NAME = 'fk_tc_packaging_item_aux_card');
SET @ddl := IF(@pick_aux_fk = 0,
    'ALTER TABLE tech_card_packaging_item
        ADD CONSTRAINT fk_tc_packaging_item_aux_card FOREIGN KEY (aux_tech_card_id) REFERENCES tech_card(id) ON DELETE SET NULL',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @packed_media_fk := (SELECT COUNT(*) FROM information_schema.TABLE_CONSTRAINTS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_packaging'
      AND CONSTRAINT_NAME = 'fk_tc_packaging_packed_media');
SET @ddl := IF(@packed_media_fk = 0,
    'ALTER TABLE tech_card_packaging
        ADD CONSTRAINT fk_tc_packaging_packed_media FOREIGN KEY (packed_media_id) REFERENCES media(id)',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

CREATE TABLE IF NOT EXISTS tech_card_packaging_item_colorway (
    item_id INT NOT NULL,
    colorway_id INT NOT NULL COMMENT 'FK product(id): a colourway of the item card',
    PRIMARY KEY (item_id, colorway_id),
    INDEX idx_tc_packaging_item_colorway_colorway (colorway_id),
    CONSTRAINT fk_tc_packaging_item_colorway_item FOREIGN KEY (item_id) REFERENCES tech_card_packaging_item(id) ON DELETE CASCADE,
    CONSTRAINT fk_tc_packaging_item_colorway_product FOREIGN KEY (colorway_id) REFERENCES product(id) ON DELETE CASCADE
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4 COMMENT 'Colourways a packaging item applies to; no rows = all (0448)';

-- +migrate Down

DROP TABLE IF EXISTS tech_card_packaging_item_colorway;

SET @packed_media_fk_back := (SELECT COUNT(*) FROM information_schema.TABLE_CONSTRAINTS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_packaging'
      AND CONSTRAINT_NAME = 'fk_tc_packaging_packed_media');
SET @ddl := IF(@packed_media_fk_back = 1,
    'ALTER TABLE tech_card_packaging DROP FOREIGN KEY fk_tc_packaging_packed_media',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @pick_aux_fk_back := (SELECT COUNT(*) FROM information_schema.TABLE_CONSTRAINTS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_packaging_item'
      AND CONSTRAINT_NAME = 'fk_tc_packaging_item_aux_card');
SET @ddl := IF(@pick_aux_fk_back = 1,
    'ALTER TABLE tech_card_packaging_item DROP FOREIGN KEY fk_tc_packaging_item_aux_card',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @pick_material_fk_back := (SELECT COUNT(*) FROM information_schema.TABLE_CONSTRAINTS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_packaging_item'
      AND CONSTRAINT_NAME = 'fk_tc_packaging_item_material');
SET @ddl := IF(@pick_material_fk_back = 1,
    'ALTER TABLE tech_card_packaging_item DROP FOREIGN KEY fk_tc_packaging_item_material',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @packed_cols_back := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_packaging'
      AND COLUMN_NAME = 'packed_size');
SET @ddl := IF(@packed_cols_back = 1,
    'ALTER TABLE tech_card_packaging DROP COLUMN packed_size, DROP COLUMN packed_media_id',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @pick_cols_back := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_packaging_item'
      AND COLUMN_NAME = 'material_id');
SET @ddl := IF(@pick_cols_back = 1,
    'ALTER TABLE tech_card_packaging_item DROP COLUMN material_id, DROP COLUMN aux_tech_card_id, DROP COLUMN per_order',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;
