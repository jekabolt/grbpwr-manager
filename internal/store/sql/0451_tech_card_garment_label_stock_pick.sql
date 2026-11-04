-- +migrate Up

-- A garment label says how it was chosen: picked from stock or made for this style.
--
-- label_source: '' = a row older than this, stock = a material from stock used as it is, make = made
-- for this style. A row with a source owns its BOM line: the save keeps that line's per-garment count
-- and puts it in every colourway's recipe, because a BOM line no recipe row names is neither costed
-- nor planned (entity/countable.go). The material is reached through the BOM line, the label holds
-- no material id of its own.
-- label_kind: the label's type when label_key does not say it; '' = the key is the type.
-- made_by, print_method: how a made label is produced. Closed lists live on the client.
-- length_cm: per garment length of goods sold by the metre; 0 = counted in qty_per_garment.
--
-- ADDITIVE ONLY. Every column has a DEFAULT, so an older binary that names none still inserts. Every
-- existing row reads '' and 0: no recipe row is written for it and no signature goes stale, since the
-- section digest adds these only when they are set.
-- Rollback to an older binary: its full-replace save drops these values, the BOM line and the recipe
-- rows stay.
--
-- ALGORITHM=INSTANT IS EXPLICIT: if MySQL could not do it instantly the ALTER fails loudly instead of
-- copying the table under the five-minute migration ceiling.
--
-- IDEMPOTENCY: DDL auto-commits, so each column has its own gate.

SET @col := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_garment_label' AND COLUMN_NAME = 'label_source');
SET @ddl := IF(@col = 0,
    'ALTER TABLE tech_card_garment_label
        ADD COLUMN label_source VARCHAR(16) NOT NULL DEFAULT '''' COMMENT ''stock, make; empty = older row'',
        ALGORITHM=INSTANT',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @col := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_garment_label' AND COLUMN_NAME = 'label_kind');
SET @ddl := IF(@col = 0,
    'ALTER TABLE tech_card_garment_label
        ADD COLUMN label_kind VARCHAR(64) NOT NULL DEFAULT '''' COMMENT ''type of the label; empty = label_key is the type'',
        ALGORITHM=INSTANT',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @col := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_garment_label' AND COLUMN_NAME = 'made_by');
SET @ddl := IF(@col = 0,
    'ALTER TABLE tech_card_garment_label
        ADD COLUMN made_by VARCHAR(32) NOT NULL DEFAULT '''' COMMENT ''how a made label is produced'',
        ALGORITHM=INSTANT',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @col := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_garment_label' AND COLUMN_NAME = 'print_method');
SET @ddl := IF(@col = 0,
    'ALTER TABLE tech_card_garment_label
        ADD COLUMN print_method VARCHAR(32) NOT NULL DEFAULT '''' COMMENT ''print of a printed or heat transfer label'',
        ALGORITHM=INSTANT',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @col := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_garment_label' AND COLUMN_NAME = 'length_cm');
SET @ddl := IF(@col = 0,
    'ALTER TABLE tech_card_garment_label
        ADD COLUMN length_cm DECIMAL(8,2) NOT NULL DEFAULT 0 COMMENT ''cm per garment of goods sold by the metre; 0 = counted in qty_per_garment'',
        ALGORITHM=INSTANT',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- migrationlint:allow-retroactive the CHECK covers label_type, added just above with DEFAULT '', so no existing row can fail it
-- A label in stock keeps its type on the material itself, read by the label picker's filter and never
-- derived from the name. '' = no type, which is the only value outside the label section. Stock,
-- reserves, price and the BOM key on material.id, so none of them changes. The CHECK is a separate
-- ALTER: MySQL cannot add a CHECK instantly, and the table holds a few dozen rows.
SET @col := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'material' AND COLUMN_NAME = 'label_type');
SET @ddl := IF(@col = 0,
    'ALTER TABLE material
        ADD COLUMN label_type VARCHAR(16) NOT NULL DEFAULT '''' COMMENT ''type of a label in stock; empty outside the label section'',
        ALGORITHM=INSTANT',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @chk := (SELECT COUNT(*) FROM information_schema.TABLE_CONSTRAINTS
    WHERE CONSTRAINT_SCHEMA = DATABASE() AND TABLE_NAME = 'material' AND CONSTRAINT_NAME = 'chk_material_label_type');
SET @ddl := IF(@chk = 0,
    'ALTER TABLE material
        ADD CONSTRAINT chk_material_label_type CHECK (label_type = '''' OR (section = ''label'' AND label_type REGEXP ''^(brand|size|flag|patch|tape|loop|hangtag|barcode|composition)$''))',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- +migrate Down

SET @chk := (SELECT COUNT(*) FROM information_schema.TABLE_CONSTRAINTS
    WHERE CONSTRAINT_SCHEMA = DATABASE() AND TABLE_NAME = 'material' AND CONSTRAINT_NAME = 'chk_material_label_type');
SET @ddl := IF(@chk = 1,
    'ALTER TABLE material DROP CONSTRAINT chk_material_label_type',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @col := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'material' AND COLUMN_NAME = 'label_type');
SET @ddl := IF(@col = 1,
    'ALTER TABLE material DROP COLUMN label_type',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @col := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_garment_label' AND COLUMN_NAME = 'length_cm');
SET @ddl := IF(@col = 1,
    'ALTER TABLE tech_card_garment_label DROP COLUMN length_cm',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @col := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_garment_label' AND COLUMN_NAME = 'print_method');
SET @ddl := IF(@col = 1,
    'ALTER TABLE tech_card_garment_label DROP COLUMN print_method',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @col := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_garment_label' AND COLUMN_NAME = 'made_by');
SET @ddl := IF(@col = 1,
    'ALTER TABLE tech_card_garment_label DROP COLUMN made_by',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @col := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_garment_label' AND COLUMN_NAME = 'label_kind');
SET @ddl := IF(@col = 1,
    'ALTER TABLE tech_card_garment_label DROP COLUMN label_kind',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @col := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_garment_label' AND COLUMN_NAME = 'label_source');
SET @ddl := IF(@col = 1,
    'ALTER TABLE tech_card_garment_label DROP COLUMN label_source',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;
