-- +migrate Up

-- Make the controlled colour dictionary the sole colour identity for tech-card colourways.
-- The legacy code/name/pantone/hex columns remain PLM metadata and are never used to derive a new
-- write. Existing rows are migrated once: linked product colour wins, then an exact dictionary
-- code/name match, and only genuinely unresolved legacy data receives the explicit dictionary value
-- UNK. After this migration every row must carry a canonical FK-backed code.
--
-- Idempotent: the backfill only touches NULL, and the ALTER is guarded through information_schema.

UPDATE product p
JOIN tech_card_colorway cw ON cw.product_id = p.id
SET p.color_code = 'UNK'
WHERE p.color_code IS NULL;

UPDATE tech_card_colorway cw
LEFT JOIN product p ON p.id = cw.product_id
LEFT JOIN color code_match ON code_match.code = UPPER(TRIM(cw.code))
LEFT JOIN color name_match ON LOWER(name_match.name) = LOWER(TRIM(cw.name))
SET cw.color_code = COALESCE(p.color_code, code_match.code, name_match.code, 'UNK')
WHERE cw.color_code IS NULL;

SET @color_code_nullable := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_colorway'
      AND COLUMN_NAME = 'color_code' AND IS_NULLABLE = 'YES');
SET @sql := IF(@color_code_nullable > 0,
    'ALTER TABLE tech_card_colorway MODIFY COLUMN color_code CHAR(3) NOT NULL',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;
