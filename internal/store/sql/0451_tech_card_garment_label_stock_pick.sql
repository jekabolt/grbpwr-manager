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

-- +migrate Down

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
