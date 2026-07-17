-- +migrate Up
-- PLM-rework WS3 / S2-S3 (root): BOM lines had a stable PK but every downstream reference was a
-- POSITIONAL index into the submitted array (bom_item_index / piece_index / fusing_bom_item_index),
-- so deleting or reordering a line left those refs dangling ("operation bom_item_index 1 out of
-- range") or silently skipped. Give each BOM line a stable wire identity (line_key) and replace the
-- positional refs with REAL foreign keys (bom_item_id ... ON DELETE RESTRICT) so the database itself
-- refuses to orphan a recipe/piece/operation.
--
-- This lands the additive schema + the backfill/conversion in one file; the store write-path becomes
-- a keyed upsert-diff in the same change (a full-replace that deletes a referenced bom_item would now
-- hit the RESTRICT). The positional columns are kept for the transition and dropped later (M3).
-- Out-of-range legacy refs (the S2/S3 bug itself) convert to NULL — the ref was already broken — so
-- adding the FK cannot fail on prod data (NULL bypasses the constraint); they are the review's audit
-- signal, not a boot-halting STOP.
--
-- Crash-idempotent: guarded on information_schema; backfills are re-runnable (WHERE ... IS NULL).

-- --- tech_card_bom_item: line_key + material_snapshot (guarded on line_key) ---
SET @need := (SELECT COUNT(*) = 0 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_bom_item' AND COLUMN_NAME = 'line_key');
SET @sql := IF(@need,
    'ALTER TABLE tech_card_bom_item ADD COLUMN line_key CHAR(26) NULL, ADD COLUMN material_snapshot JSON NULL',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

-- Backfill a deterministic, unique 26-char line_key for legacy rows (real ULIDs come from clients on
-- new lines). 'LEGACY' + zero-padded id = 26 chars; id is unique so the key is unique.
UPDATE tech_card_bom_item SET line_key = CONCAT('LEGACY', LPAD(id, 20, '0'))
    WHERE line_key IS NULL OR line_key = '';

-- UNIQUE (tech_card_id, line_key): the upsert-diff matches on it, so the invariant must hold now.
SET @need := (SELECT COUNT(*) = 0 FROM information_schema.STATISTICS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_bom_item' AND INDEX_NAME = 'uniq_bom_line_key');
SET @sql := IF(@need,
    'ALTER TABLE tech_card_bom_item ADD CONSTRAINT uniq_bom_line_key UNIQUE (tech_card_id, line_key)',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

-- --- referrer FK columns (nullable, no constraint yet), guarded per table ---
SET @need := (SELECT COUNT(*) = 0 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_operation' AND COLUMN_NAME = 'bom_item_id');
SET @sql := IF(@need, 'ALTER TABLE tech_card_operation ADD COLUMN bom_item_id INT NULL', 'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

SET @need := (SELECT COUNT(*) = 0 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_piece_material' AND COLUMN_NAME = 'bom_item_id');
SET @sql := IF(@need,
    'ALTER TABLE tech_card_piece_material ADD COLUMN bom_item_id INT NULL, ADD COLUMN fusing_bom_item_id INT NULL',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

SET @need := (SELECT COUNT(*) = 0 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_colorway_usage' AND COLUMN_NAME = 'bom_item_id');
SET @sql := IF(@need,
    'ALTER TABLE tech_card_colorway_usage ADD COLUMN bom_item_id INT NULL, ADD COLUMN piece_id INT NULL',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

-- --- convert positional indexes -> real ids (the index-th BOM line of the owning style, ordered by
--     display_order,id; the same 0-based scheme the app used, see 0079). Out-of-range -> stays NULL. ---
UPDATE tech_card_operation op
    JOIN (SELECT id, tech_card_id, ROW_NUMBER() OVER (PARTITION BY tech_card_id ORDER BY display_order, id) - 1 AS idx
          FROM tech_card_bom_item) r ON r.tech_card_id = op.tech_card_id AND r.idx = op.bom_item_index
    SET op.bom_item_id = r.id
    WHERE op.bom_item_index IS NOT NULL AND op.bom_item_id IS NULL;

UPDATE tech_card_piece_material pm
    JOIN tech_card_piece pc ON pc.id = pm.piece_id
    JOIN (SELECT id, tech_card_id, ROW_NUMBER() OVER (PARTITION BY tech_card_id ORDER BY display_order, id) - 1 AS idx
          FROM tech_card_bom_item) r ON r.tech_card_id = pc.tech_card_id AND r.idx = pm.bom_item_index
    SET pm.bom_item_id = r.id
    WHERE pm.bom_item_index IS NOT NULL AND pm.bom_item_id IS NULL;

UPDATE tech_card_piece_material pm
    JOIN tech_card_piece pc ON pc.id = pm.piece_id
    JOIN (SELECT id, tech_card_id, ROW_NUMBER() OVER (PARTITION BY tech_card_id ORDER BY display_order, id) - 1 AS idx
          FROM tech_card_bom_item) r ON r.tech_card_id = pc.tech_card_id AND r.idx = pm.fusing_bom_item_index
    SET pm.fusing_bom_item_id = r.id
    WHERE pm.fusing_bom_item_index IS NOT NULL AND pm.fusing_bom_item_id IS NULL;

UPDATE tech_card_colorway_usage u
    JOIN product p ON p.id = u.colorway_id
    JOIN (SELECT id, tech_card_id, ROW_NUMBER() OVER (PARTITION BY tech_card_id ORDER BY display_order, id) - 1 AS idx
          FROM tech_card_bom_item) r ON r.tech_card_id = p.style_id AND r.idx = u.bom_item_index
    SET u.bom_item_id = r.id
    WHERE u.bom_item_index IS NOT NULL AND u.bom_item_id IS NULL;

UPDATE tech_card_colorway_usage u
    JOIN product p ON p.id = u.colorway_id
    JOIN (SELECT id, tech_card_id, ROW_NUMBER() OVER (PARTITION BY tech_card_id ORDER BY display_order, id) - 1 AS idx
          FROM tech_card_piece) r ON r.tech_card_id = p.style_id AND r.idx = u.piece_index
    SET u.piece_id = r.id
    WHERE u.piece_index IS NOT NULL AND u.piece_id IS NULL;

-- --- FK constraints (ON DELETE RESTRICT), guarded per constraint name. Added AFTER conversion so
--     they validate populated data; the deployed store upsert-diffs BOM, so future writes can't
--     violate them. ---
SET @need := (SELECT COUNT(*) = 0 FROM information_schema.TABLE_CONSTRAINTS
    WHERE TABLE_SCHEMA = DATABASE() AND CONSTRAINT_NAME = 'fk_op_bom');
SET @sql := IF(@need,
    'ALTER TABLE tech_card_operation ADD CONSTRAINT fk_op_bom FOREIGN KEY (bom_item_id) REFERENCES tech_card_bom_item(id) ON DELETE RESTRICT',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

SET @need := (SELECT COUNT(*) = 0 FROM information_schema.TABLE_CONSTRAINTS
    WHERE TABLE_SCHEMA = DATABASE() AND CONSTRAINT_NAME = 'fk_pm_bom');
SET @sql := IF(@need,
    'ALTER TABLE tech_card_piece_material ADD CONSTRAINT fk_pm_bom FOREIGN KEY (bom_item_id) REFERENCES tech_card_bom_item(id) ON DELETE RESTRICT',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

SET @need := (SELECT COUNT(*) = 0 FROM information_schema.TABLE_CONSTRAINTS
    WHERE TABLE_SCHEMA = DATABASE() AND CONSTRAINT_NAME = 'fk_pm_fuse');
SET @sql := IF(@need,
    'ALTER TABLE tech_card_piece_material ADD CONSTRAINT fk_pm_fuse FOREIGN KEY (fusing_bom_item_id) REFERENCES tech_card_bom_item(id) ON DELETE RESTRICT',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

SET @need := (SELECT COUNT(*) = 0 FROM information_schema.TABLE_CONSTRAINTS
    WHERE TABLE_SCHEMA = DATABASE() AND CONSTRAINT_NAME = 'fk_usage_bom');
SET @sql := IF(@need,
    'ALTER TABLE tech_card_colorway_usage ADD CONSTRAINT fk_usage_bom FOREIGN KEY (bom_item_id) REFERENCES tech_card_bom_item(id) ON DELETE RESTRICT',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

-- NOTE: piece_id is backfilled above but its FK to tech_card_piece is deliberately NOT added here.
-- Pieces are full-replaced (delete + reinsert with new ids) by UpdateTechCard, so a RESTRICT FK from
-- the persistent (colourway-owned) usage would block every piece save; the FK lands when WS4 makes
-- pieces keyed/stable. bom_item_id CAN take its FK now because BOM is upsert-diffed (stable ids) in
-- the same change.

-- +migrate Down
ALTER TABLE tech_card_colorway_usage DROP FOREIGN KEY fk_usage_bom,
    DROP COLUMN piece_id, DROP COLUMN bom_item_id;
ALTER TABLE tech_card_piece_material DROP FOREIGN KEY fk_pm_fuse, DROP FOREIGN KEY fk_pm_bom,
    DROP COLUMN fusing_bom_item_id, DROP COLUMN bom_item_id;
ALTER TABLE tech_card_operation DROP FOREIGN KEY fk_op_bom, DROP COLUMN bom_item_id;
ALTER TABLE tech_card_bom_item DROP INDEX uniq_bom_line_key, DROP COLUMN material_snapshot, DROP COLUMN line_key;
