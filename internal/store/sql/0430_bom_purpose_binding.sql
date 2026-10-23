-- +migrate Up

-- Bias binding / edging is a distinct roll-goods purpose. All three purpose columns use the same
-- closed vocabulary: the BOM line owns it, while pattern sheets and piece aliases bind to it.
-- Each replacement only widens the existing CHECK by appending `binding`; every row accepted by
-- the old constraint is therefore accepted by the new one, so no existing data can block deploy.
--
-- MySQL auto-commits DDL. Each DROP + ADD is one atomic ALTER and is guarded by the absence of the
-- new token, so a retry after a later step fails is safe. The constraints have stable explicit
-- names; no positional auto-generated CHECK name is used.

SET @bom_purpose_needs_binding := (SELECT COUNT(*)
    FROM information_schema.TABLE_CONSTRAINTS tc
    JOIN information_schema.CHECK_CONSTRAINTS cc
      ON cc.CONSTRAINT_SCHEMA = tc.CONSTRAINT_SCHEMA AND cc.CONSTRAINT_NAME = tc.CONSTRAINT_NAME
    WHERE tc.TABLE_SCHEMA = DATABASE() AND tc.TABLE_NAME = 'tech_card_bom_item'
      AND tc.CONSTRAINT_NAME = 'chk_bom_item_purpose'
      AND cc.CHECK_CLAUSE NOT LIKE '%binding%');
SET @ddl := IF(@bom_purpose_needs_binding = 1,
    'ALTER TABLE tech_card_bom_item
        DROP CHECK chk_bom_item_purpose,
        ADD CONSTRAINT chk_bom_item_purpose CHECK (purpose IS NULL OR (purpose REGEXP ''^(main|lining|pocketing|interfacing|insulation|contrast|mesh|other|binding)$'' AND STRCMP(CAST(purpose AS BINARY), CAST(LOWER(purpose) AS BINARY)) = 0))',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @pattern_purpose_needs_binding := (SELECT COUNT(*)
    FROM information_schema.TABLE_CONSTRAINTS tc
    JOIN information_schema.CHECK_CONSTRAINTS cc
      ON cc.CONSTRAINT_SCHEMA = tc.CONSTRAINT_SCHEMA AND cc.CONSTRAINT_NAME = tc.CONSTRAINT_NAME
    WHERE tc.TABLE_SCHEMA = DATABASE() AND tc.TABLE_NAME = 'tech_card_size_pattern'
      AND tc.CONSTRAINT_NAME = 'chk_tcsp_fabric_purpose'
      AND cc.CHECK_CLAUSE NOT LIKE '%binding%');
SET @ddl := IF(@pattern_purpose_needs_binding = 1,
    'ALTER TABLE tech_card_size_pattern
        DROP CHECK chk_tcsp_fabric_purpose,
        ADD CONSTRAINT chk_tcsp_fabric_purpose CHECK (fabric_purpose IS NULL OR (fabric_purpose REGEXP ''^(main|lining|pocketing|interfacing|insulation|contrast|mesh|other|binding)$'' AND STRCMP(CAST(fabric_purpose AS BINARY), CAST(LOWER(fabric_purpose) AS BINARY)) = 0))',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @alias_purpose_needs_binding := (SELECT COUNT(*)
    FROM information_schema.TABLE_CONSTRAINTS tc
    JOIN information_schema.CHECK_CONSTRAINTS cc
      ON cc.CONSTRAINT_SCHEMA = tc.CONSTRAINT_SCHEMA AND cc.CONSTRAINT_NAME = tc.CONSTRAINT_NAME
    WHERE tc.TABLE_SCHEMA = DATABASE() AND tc.TABLE_NAME = 'tech_card_piece_dxf_block'
      AND tc.CONSTRAINT_NAME = 'chk_tcpdb_fabric_purpose'
      AND cc.CHECK_CLAUSE NOT LIKE '%binding%');
SET @ddl := IF(@alias_purpose_needs_binding = 1,
    'ALTER TABLE tech_card_piece_dxf_block
        DROP CHECK chk_tcpdb_fabric_purpose,
        ADD CONSTRAINT chk_tcpdb_fabric_purpose CHECK (fabric_purpose IS NULL OR (fabric_purpose REGEXP ''^(main|lining|pocketing|interfacing|insulation|contrast|mesh|other|binding)$'' AND STRCMP(CAST(fabric_purpose AS BINARY), CAST(LOWER(fabric_purpose) AS BINARY)) = 0))',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- +migrate Down

-- Forward-only vocabulary expansion. Removing `binding` would narrow all three CHECKs and could
-- reject rows written after this migration, leaving a rollback half-applied.
SELECT 1;
