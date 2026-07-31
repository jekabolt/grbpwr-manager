-- +migrate Up
-- Slots (BOM = роль): a colourway pins the CONCRETE warehouse article it takes for a BOM line,
-- while the BOM line itself stays the role ("основная молния") with a default article
-- (bom_item.material_id). NULL pin = inherit the slot default, so this column changes nothing
-- until a colourway explicitly diverges (black jacket → antique-brass zip, bone → silver).
-- No backfill on purpose: copying today's defaults into pins would freeze them — a later change
-- of the slot default must keep propagating to colourways that never diverged.
--
-- Crash-idempotent: guarded on information_schema (see 0159 for the idiom).
SET @need := (SELECT COUNT(*) = 0 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_colorway_usage' AND COLUMN_NAME = 'material_id');
SET @sql := IF(@need,
    'ALTER TABLE tech_card_colorway_usage ADD COLUMN material_id INT NULL COMMENT ''per-colourway pinned catalog article for this slot; NULL = inherit bom_item.material_id''',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

-- RESTRICT, not SET NULL: silently unpinning would flip the colourway back to the slot default —
-- a different physical article on a signed spec. Materials are archived, not deleted, in the
-- normal flow; a hard delete of a pinned article must be refused loudly.
SET @need := (SELECT COUNT(*) = 0 FROM information_schema.TABLE_CONSTRAINTS
    WHERE TABLE_SCHEMA = DATABASE() AND CONSTRAINT_NAME = 'fk_usage_material');
SET @sql := IF(@need,
    'ALTER TABLE tech_card_colorway_usage ADD CONSTRAINT fk_usage_material FOREIGN KEY (material_id) REFERENCES material(id) ON DELETE RESTRICT',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

-- +migrate Down
ALTER TABLE tech_card_colorway_usage DROP FOREIGN KEY fk_usage_material, DROP COLUMN material_id;
