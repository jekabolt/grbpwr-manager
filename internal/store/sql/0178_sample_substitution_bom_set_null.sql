-- +migrate Up
-- P4-flyover M3 (tmp/plm-rework/04-MAZE-FLYOVER.md): sample_substitution.bom_item_id was
-- ON DELETE RESTRICT (0170_ws6_sample_round_spine.sql), pinning a BOM line against deletion for any
-- dev-time substitution EVER recorded against it — even from a long-closed round. A substitution is a
-- historical dev-time fact (Q2: never COGS) that already snapshots original_material_id, so losing the
-- bom_item_id link does not lose the record's meaning. Align with the same class of dev-time historical
-- reference, fitting_change_request.piece_id (0171_ws6_fitting_audit_and_changereq_s26.sql), which
-- already degrades to NULL rather than blocking.
--
-- This also removes a cascade-ordering hazard (M2 of the same review): deleting a tech card cascades
-- both tech_card_bom_item (CASCADE from tech_card) and sample (CASCADE from tech_card); a substitution
-- row sits at the confluence of sample_id (CASCADE, via sample) and bom_item_id (was RESTRICT, via
-- tech_card_bom_item) — two cascade branches converging on one row with no guaranteed evaluation order,
-- so the delete could non-deterministically 1451 whenever a card's own sample had a substitution on the
-- card's own BOM line. ON DELETE SET NULL removes the RESTRICT branch entirely.
--
-- bom_item_id is already nullable (0170) — only the FK's ON DELETE action changes here.
--
-- Idempotent: guarded on information_schema.REFERENTIAL_CONSTRAINTS.DELETE_RULE, so a mid-file crash or
-- a rerun against an already-migrated schema is a no-op. The FK is dropped and re-added under its
-- existing stable explicit name (fk_subst_bom_item), never an auto-generated one.

SET @cur_rule := (SELECT DELETE_RULE FROM information_schema.REFERENTIAL_CONSTRAINTS
    WHERE CONSTRAINT_SCHEMA = DATABASE() AND TABLE_NAME = 'sample_substitution'
      AND CONSTRAINT_NAME = 'fk_subst_bom_item');
SET @need_drop := (@cur_rule IS NOT NULL AND @cur_rule <> 'SET NULL');
SET @sql := IF(@need_drop,
    'ALTER TABLE sample_substitution DROP FOREIGN KEY fk_subst_bom_item',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

SET @need_add := (SELECT COUNT(*) = 0 FROM information_schema.REFERENTIAL_CONSTRAINTS
    WHERE CONSTRAINT_SCHEMA = DATABASE() AND TABLE_NAME = 'sample_substitution'
      AND CONSTRAINT_NAME = 'fk_subst_bom_item');
SET @sql := IF(@need_add,
    'ALTER TABLE sample_substitution ADD CONSTRAINT fk_subst_bom_item FOREIGN KEY (bom_item_id) REFERENCES tech_card_bom_item(id) ON DELETE SET NULL',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

-- +migrate Down
ALTER TABLE sample_substitution DROP FOREIGN KEY fk_subst_bom_item;
ALTER TABLE sample_substitution ADD CONSTRAINT fk_subst_bom_item FOREIGN KEY (bom_item_id) REFERENCES tech_card_bom_item(id) ON DELETE RESTRICT;
