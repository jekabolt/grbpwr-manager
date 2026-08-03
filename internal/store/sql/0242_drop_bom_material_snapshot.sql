-- +migrate Up

-- production-costing Phase 3 — the CONTRACT half of Phase 2's expand/contract (adversarial review
-- #7): tech_card_bom_item.material_snapshot was written on every BOM save and read by NOTHING — the
-- line's own columns carry the identity, the admin client stripped the field from payloads, and the
-- tech-pack renders from the resolved line. Phase 2 removed the write path and reserved proto field
-- 26; the DROP had to wait one deploy because the then-serving binary still SELECTed the column in
-- enrichMaterials — dropping at boot would have errored the old container for the deploy window.
-- On BETA the Phase 2 binary (zero references) is live before this runs. On PROD it is NOT (master
-- sits pre-Phase-2 at the time of writing): merging beta→master in one go would break the old
-- container's BOM saves and tech-card reads for the deploy window. Either two-stage the prod merge
-- (Phase 2 first, then this) or accept a short admin-PLM-only window — the storefront never touches
-- these objects. Same caveat on 0243.
SET @have_col := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_bom_item' AND COLUMN_NAME = 'material_snapshot');
SET @sql := IF(@have_col > 0, 'ALTER TABLE tech_card_bom_item DROP COLUMN material_snapshot', 'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

-- +migrate Down

SELECT 1;
