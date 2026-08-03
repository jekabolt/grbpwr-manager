-- +migrate Up

-- production-costing Phase 2 dead-schema drop (plan 13): tech_card_bom_item.material_snapshot was
-- written on every BOM save and read by NOTHING — the line's own columns carry the identity, the
-- admin client stripped the field from payloads, and the tech-pack renders from the resolved line.
-- The write path and the proto field were removed in this same change-set (reserved 26).
SET @have_col := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_bom_item' AND COLUMN_NAME = 'material_snapshot');
SET @sql := IF(@have_col > 0, 'ALTER TABLE tech_card_bom_item DROP COLUMN material_snapshot', 'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

-- +migrate Down

SELECT 1;
