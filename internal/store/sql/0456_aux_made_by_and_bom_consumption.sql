-- +migrate Up

-- Norms for an AUXILIARY tech card (purpose = 'auxiliary'), and who makes it.
--
-- An aux card produces a warehouse material and has no colourways, while measured norms lived only
-- on colourway recipe rows (tech_card_colorway_usage.consumption). So an aux card had no measured
-- norm anywhere: its run material plan was empty and its costing priced materials at zero.
--
-- 1. tech_card.aux_made_by: 'us' (we make it from the BOM) or 'supplier' (bought finished: the
--    supplier's price is the cost and nothing is reserved or issued). NULL on sellable cards; NULL on
--    an aux card reads as 'us' (an older binary that creates an aux card writes no value).
--    Backfill of existing aux cards: 'us' when the card has a BOM line with a material, else
--    'supplier'. Only NULL rows are touched, so a rerun changes nothing.
--    A value-set CHECK only (named, guarded). No purpose gate: an older binary that flips a card to
--    sellable does not clear the column, and a gate would make that save fail.
--
-- 2. tech_card_bom_item.consumption_per_unit: the measured norm of an aux card's BOM line, per one
--    output unit, net of wastage. Counted sections keep qty_per_garment + spare_qty. NULL = no norm
--    (unpriced / a plan blocker, never zero). No backfill and no CHECK: validated in Go (>= 0, at
--    most 3 decimals), like est_usage (0365). Not named consumption / quantity: those were dropped
--    from this table in 0079.
--
-- ALGORITHM=INSTANT IS EXPLICIT on both columns: if MySQL could not do it instantly the ALTER fails
-- loudly instead of copying the table. The CHECK on tech_card is a COPY ALTER of a small table.
--
-- IDEMPOTENCY: DDL auto-commits, so every step has its own gate.

SET @col := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card' AND COLUMN_NAME = 'aux_made_by');
SET @ddl := IF(@col = 0,
    'ALTER TABLE tech_card
        ADD COLUMN aux_made_by VARCHAR(16) NULL COMMENT ''us|supplier; auxiliary cards only, NULL reads as us'',
        ALGORITHM=INSTANT',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- migrationlint:allow-retroactive the CHECK covers aux_made_by, added NULL just above; the backfill below writes only 'us' or 'supplier' before the CHECK lands, so no existing row can fail it
UPDATE tech_card
SET aux_made_by = CASE
    WHEN EXISTS (
        SELECT 1 FROM tech_card_bom_item bi
        WHERE bi.tech_card_id = tech_card.id AND bi.material_id IS NOT NULL
    ) THEN 'us'
    ELSE 'supplier'
END
WHERE purpose = 'auxiliary' AND aux_made_by IS NULL;

SET @chk := (SELECT COUNT(*) FROM information_schema.TABLE_CONSTRAINTS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card' AND CONSTRAINT_NAME = 'chk_tech_card_aux_made_by');
SET @ddl := IF(@chk = 0,
    'ALTER TABLE tech_card ADD CONSTRAINT chk_tech_card_aux_made_by CHECK (aux_made_by IS NULL OR aux_made_by REGEXP ''^(us|supplier)$'')',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @col := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_bom_item' AND COLUMN_NAME = 'consumption_per_unit');
SET @ddl := IF(@col = 0,
    'ALTER TABLE tech_card_bom_item
        ADD COLUMN consumption_per_unit DECIMAL(10,3) NULL COMMENT ''aux cards: measured norm per output unit, net of wastage; NULL = no norm'',
        ALGORITHM=INSTANT',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- +migrate Down

SET @col_back := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_bom_item' AND COLUMN_NAME = 'consumption_per_unit');
SET @ddl := IF(@col_back = 1,
    'ALTER TABLE tech_card_bom_item DROP COLUMN consumption_per_unit',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @chk_back := (SELECT COUNT(*) FROM information_schema.TABLE_CONSTRAINTS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card' AND CONSTRAINT_NAME = 'chk_tech_card_aux_made_by');
SET @ddl := IF(@chk_back = 1,
    'ALTER TABLE tech_card DROP CHECK chk_tech_card_aux_made_by',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @col_back := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card' AND COLUMN_NAME = 'aux_made_by');
SET @ddl := IF(@col_back = 1,
    'ALTER TABLE tech_card DROP COLUMN aux_made_by',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;
