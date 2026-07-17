-- +migrate Up
-- PLM-rework WS7 (§2.8 / Q3 / S21): the ASSEMBLY bill — what auxiliary items (labels/tags) physically go
-- on/into a garment style — distinct from PACKAGING (WS2 packaging_recipe, on the SHIPMENT). Assembly is
-- on the garment and its component's material is consumed in production (existing BOM/production path), so
-- there is NO new cost machinery here and nothing crosses the sales/warehouse streams.
--
-- style_assembly: one row = one auxiliary component of a garment style. component_tech_card_id points at
-- an AUXILIARY tech card (purpose=auxiliary; carries aux_subtype and an output_material_id that resolves to
-- the warehouse material the garment BOM consumes). size_id is an optional garment-size scope (size labels
-- differ per size); NULL = every size. Write path is full-replace per style (mirrors WS2
-- UpsertPackagingRecipe), which also dedups — hence a plain INDEX, not a NULL-fragile UNIQUE.
--
-- ON DELETE: CASCADE from the garment style (its bill dies with it); RESTRICT from the component style and
-- size (can't delete an auxiliary card / size still referenced by an assembly). Idempotent via
-- CREATE TABLE IF NOT EXISTS (FKs/CHECK land with the table).

CREATE TABLE IF NOT EXISTS style_assembly (
  id INT PRIMARY KEY AUTO_INCREMENT,
  style_id INT NOT NULL COMMENT 'garment style (tech_card, purpose=sellable)',
  component_tech_card_id INT NOT NULL COMMENT 'auxiliary item (tech_card, purpose=auxiliary; carries aux_subtype)',
  size_id INT NULL COMMENT 'optional garment-size scope; NULL = all sizes',
  qty DECIMAL(12, 3) NOT NULL DEFAULT 1,
  print_note TEXT NULL COMMENT 'what to print (artwork ref / text)',
  position_note TEXT NULL COMMENT 'where/how it is attached (placement)',
  active BOOLEAN NOT NULL DEFAULT TRUE,
  lock_version INT NOT NULL DEFAULT 0,
  created_by VARCHAR(255) NOT NULL DEFAULT '',
  updated_by VARCHAR(255) NOT NULL DEFAULT '',
  created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  CONSTRAINT chk_style_assembly_qty CHECK (qty > 0),
  INDEX idx_style_assembly_style (style_id),
  CONSTRAINT fk_style_assembly_style FOREIGN KEY (style_id) REFERENCES tech_card(id) ON DELETE CASCADE,
  CONSTRAINT fk_style_assembly_component FOREIGN KEY (component_tech_card_id) REFERENCES tech_card(id) ON DELETE RESTRICT,
  CONSTRAINT fk_style_assembly_size FOREIGN KEY (size_id) REFERENCES size(id) ON DELETE RESTRICT
) COMMENT 'On-garment assembly bill: auxiliary components (labels/tags) per style (WS7, §2.8)';

-- §2.8 S21 unification bridge: link the free-text garment label SPEC (tech_card_label) to the physical
-- label MATERIAL's BOM line (tech_card_bom_item). This is the additive half of the 3-concept unification;
-- the destructive collapse (label_type→aux_subtype, drop redundant free-text) is a later guarded M3.
-- ON DELETE SET NULL: dropping a BOM line unlinks the label spec rather than deleting the spec.
-- Guarded/idempotent (MySQL 8 has no ADD COLUMN / ADD CONSTRAINT IF NOT EXISTS).
SET @need_col := (SELECT COUNT(*) = 0 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_label' AND COLUMN_NAME = 'bom_item_id');
SET @sql := IF(@need_col,
    'ALTER TABLE tech_card_label ADD COLUMN bom_item_id INT NULL COMMENT ''FK tech_card_bom_item: physical label material in the BOM (§2.8)''',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

SET @need_fk := (SELECT COUNT(*) = 0 FROM information_schema.TABLE_CONSTRAINTS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_label' AND CONSTRAINT_NAME = 'fk_tech_card_label_bom_item');
SET @sql := IF(@need_fk,
    'ALTER TABLE tech_card_label ADD CONSTRAINT fk_tech_card_label_bom_item FOREIGN KEY (bom_item_id) REFERENCES tech_card_bom_item(id) ON DELETE SET NULL',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

-- +migrate Down
DROP TABLE IF EXISTS style_assembly;
-- (leaves tech_card_label.bom_item_id; a Down is not exercised in prod automigrate)
SELECT 1;
