-- +migrate Up
-- PR6 R1 — Colourway domain merge (step 2 of 2: DROP the merged-away parent behind a re-checked
-- guard). Mirrors 0142 (drop size_measurement after 0141) and 0140 (drop product style cols after
-- 0139): a destructive contract that first re-proves the expand/backfill/repoint step (0151) left no
-- orphan and no unresolved conflict, so a partial or tampered 0151 can never silently strand a
-- costing/sample reference when tech_card_colorway disappears.
--
-- Crash-idempotent: the guard reports are rebuilt deterministically; DROP INDEX/COLUMN/TABLE are all
-- guarded (IF EXISTS / information_schema), so a retried apply is a no-op once complete.

-- 1) Re-assert 0151's merge preflight is still clean (the named CHECK rejects any non-zero count).
INSERT INTO migration_0151_merge_guard (singleton, conflict_count)
SELECT 1, COUNT(*) FROM migration_0151_merge_conflict
ON DUPLICATE KEY UPDATE conflict_count = VALUES(conflict_count);

-- 2) Post-condition: every child that was repointed must now resolve to a real product. A CASCADE
-- child (usage / bom_colorway / piece_material) with a non-NULL colorway_id outside product(id), or a
-- SET-NULL child (sample) with a dangling colorway_id, is an orphan — STOP before the parent is gone.
CREATE TABLE IF NOT EXISTS migration_0152_orphan_conflict (
    child_table VARCHAR(64) NOT NULL,
    colorway_id INT NOT NULL,
    PRIMARY KEY (child_table, colorway_id)
);
DELETE FROM migration_0152_orphan_conflict;

INSERT INTO migration_0152_orphan_conflict (child_table, colorway_id)
SELECT 'tech_card_colorway_usage', c.colorway_id FROM tech_card_colorway_usage c
WHERE c.colorway_id IS NOT NULL AND NOT EXISTS (SELECT 1 FROM product p WHERE p.id = c.colorway_id)
GROUP BY c.colorway_id;
INSERT INTO migration_0152_orphan_conflict (child_table, colorway_id)
SELECT 'tech_card_bom_colorway', c.colorway_id FROM tech_card_bom_colorway c
WHERE c.colorway_id IS NOT NULL AND NOT EXISTS (SELECT 1 FROM product p WHERE p.id = c.colorway_id)
GROUP BY c.colorway_id;
INSERT INTO migration_0152_orphan_conflict (child_table, colorway_id)
SELECT 'tech_card_piece_material', c.colorway_id FROM tech_card_piece_material c
WHERE c.colorway_id IS NOT NULL AND NOT EXISTS (SELECT 1 FROM product p WHERE p.id = c.colorway_id)
GROUP BY c.colorway_id;
INSERT INTO migration_0152_orphan_conflict (child_table, colorway_id)
SELECT 'sample', c.colorway_id FROM sample c
WHERE c.colorway_id IS NOT NULL AND NOT EXISTS (SELECT 1 FROM product p WHERE p.id = c.colorway_id)
GROUP BY c.colorway_id;

CREATE TABLE IF NOT EXISTS migration_0152_orphan_guard (
    singleton    TINYINT NOT NULL PRIMARY KEY,
    orphan_count INT     NOT NULL,
    CONSTRAINT migration_0152_no_orphan_children CHECK (orphan_count = 0)
);
DELETE FROM migration_0152_orphan_guard;
INSERT INTO migration_0152_orphan_guard (singleton, orphan_count)
SELECT 1, COUNT(*) FROM migration_0152_orphan_conflict;

-- 3) Drop the temporary correlation column added by 0151 (ledger is complete). Guarded.
SET @has_src := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'product' AND COLUMN_NAME = 'merge_src_colorway_id');
SET @sql := IF(@has_src > 0, 'ALTER TABLE product DROP COLUMN merge_src_colorway_id', 'SELECT 1');
PREPARE s FROM @sql; EXECUTE s; DEALLOCATE PREPARE s;

-- 4) Drop the merged-away parent. Its own FKs (tech_card_id, product_id, swatch, color_code) go with
-- it; all children were repointed at product in 0151.
DROP TABLE IF EXISTS tech_card_colorway;

-- +migrate Down
-- One-way big-bang merge (R1/R10): rollback is restore-from-backup, not a Down. Intentionally empty.
