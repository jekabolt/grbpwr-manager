-- +migrate Up

-- production-costing Phase 2 dead-schema drop (plan 13): tech_card.collection_id was a 0154
-- backfill orphan — the tech-card form writes the collection NAME string, nothing ever read or
-- wrote the FK column again, and a renamed collection silently orphaned the id. Verified by grep
-- (the only Go reference was a comment). FK first, then the column, both guarded.
SET @have_fk := (SELECT COUNT(*) FROM information_schema.TABLE_CONSTRAINTS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card' AND CONSTRAINT_NAME = 'fk_tech_card_collection');
SET @sql := IF(@have_fk > 0, 'ALTER TABLE tech_card DROP FOREIGN KEY fk_tech_card_collection', 'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

SET @have_col := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card' AND COLUMN_NAME = 'collection_id');
SET @sql := IF(@have_col > 0, 'ALTER TABLE tech_card DROP COLUMN collection_id', 'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

-- +migrate Down

SELECT 1;
