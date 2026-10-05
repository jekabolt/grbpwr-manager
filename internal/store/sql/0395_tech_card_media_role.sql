-- +migrate Up

-- Moodboard picture roles (64-DEFERRED E3): each moodboard picture states what it is FOR —
-- 'target' (the garment we make), 'detail' (a detail reference), 'material' (fabric / colour /
-- texture), 'mood' (atmosphere only). '' = unassigned (prompts treat it as mood and say nothing).
-- Technical rows keep ''. The column rides the existing full-replace card save (insertTechCardMedia).
--
-- ADD COLUMN with a DEFAULT only — no CHECK (a retroactive CHECK halts the deploy); the vocabulary
-- is enforced in Go (dto parseTechCardMediaItems, entity.IsTechCardMediaRole).
--
-- Idempotent: guard by information_schema, one statement per PREPARE (prod has no multiStatements).

SET @col_exists := (
    SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'tech_card_media'
      AND COLUMN_NAME = 'role'
);
SET @ddl := IF(@col_exists = 0,
    'ALTER TABLE tech_card_media ADD COLUMN role VARCHAR(16) NOT NULL DEFAULT '''' COMMENT ''moodboard picture role: target|detail|material|mood; empty = unassigned''',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- +migrate Down

SET @col_exists := (
    SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'tech_card_media'
      AND COLUMN_NAME = 'role'
);
SET @ddl := IF(@col_exists = 1,
    'ALTER TABLE tech_card_media DROP COLUMN role',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;
