-- +migrate Up

-- Отстрочка: число строчек шага 1..10 вместо 1..4 (владелец, 30.10). Потолок в Go —
-- entity.MaxTopstitchRows; схема лишь повторяет его.
-- data-checked: widening 1..4 → 1..10; every existing row satisfied the old CHECK
--
-- Идемпотентность: каждый шаг под своей проверкой information_schema, один оператор на строку.
-- Старый бинарь схему переживает: он пишет не больше четырёх.

-- 1. Снять старую проверку по стабильному имени (на какой-то базе её может не быть).
SET @old := (SELECT COUNT(*) FROM information_schema.TABLE_CONSTRAINTS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_operation'
      AND CONSTRAINT_NAME = 'chk_op_topstitch_rows');
SET @ddl := IF(@old = 1, 'ALTER TABLE tech_card_operation DROP CHECK chk_op_topstitch_rows', 'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- 2. Новая проверка 1..10 и комментарий колонки (тип и позиция те же).
SET @new := (SELECT COUNT(*) FROM information_schema.TABLE_CONSTRAINTS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_operation'
      AND CONSTRAINT_NAME = 'chk_op_topstitch_rows');
SET @ddl := IF(@new = 0,
    'ALTER TABLE tech_card_operation
        MODIFY COLUMN topstitch_rows TINYINT NULL COMMENT ''число строчек отстрочки, 1..10; NULL = не задано'',
        ADD CONSTRAINT chk_op_topstitch_rows CHECK (topstitch_rows IS NULL OR (topstitch_rows >= 1 AND topstitch_rows <= 10))',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- +migrate Down
--
-- Возвращает потолок 1..4. Если успели записать больше четырёх, откат упадёт на CHECK — осознанно.
SET @old := (SELECT COUNT(*) FROM information_schema.TABLE_CONSTRAINTS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_operation'
      AND CONSTRAINT_NAME = 'chk_op_topstitch_rows');
SET @ddl := IF(@old = 1,
    'ALTER TABLE tech_card_operation
        DROP CHECK chk_op_topstitch_rows,
        MODIFY COLUMN topstitch_rows TINYINT NULL COMMENT ''число строчек отстрочки, 1..4; NULL = не задано'',
        ADD CONSTRAINT chk_op_topstitch_rows CHECK (topstitch_rows IS NULL OR (topstitch_rows >= 1 AND topstitch_rows <= 4))',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;
