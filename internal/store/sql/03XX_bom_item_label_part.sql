-- +migrate Up

-- ЧАСТЬ СОСТАВНИКА (label_part) строки BOM: в какую колонку этикетки состава (care label) идёт
-- состав этой строки — SHELL, BODY LINING, SLEEVE LINING … Свойство ЛЕНТЫ, а не геометрии и не
-- закупки, поэтому своя ось рядом с purpose (0265) и kind (0278), а не их производная: подкладка
-- рукава и подкладка стана обе purpose='lining', а на ленте это две колонки.
--
-- NULL = «авто»: дефолт части по section/purpose выводит КЛИЕНТ (defaultLabelPart), сервер хранит
-- только явный выбор. Поэтому ничего не бэкфиллится — хранимый NULL честно значит «не решали», и
-- смена правила дефолта не требует миграции данных. `not_on_label` — явное «на ленту не идёт».
--
-- Пары с секцией в схеме нет намеренно: любой раздел может попасть на этикетку явно.
--
-- БЕЗОПАСНОСТЬ ПРОТИВ ПРОДА: колонка НОВАЯ, ADD COLUMN NULL ничего не переписывает, и CHECK при
-- добавлении проверяет строки, у которых label_part везде NULL, — а проверка на NULL истинна. То
-- есть ограничение не ретроактивно.
--
-- Идемпотентность (CLAUDE.md): колонка и CHECK едут ОДНИМ ALTER (атомарный DDL MySQL 8) под
-- проверкой наличия `label_part`, поэтому повтор файла после падения — no-op. CHECK ИМЕНОВАН:
-- авто-имя <table>_chk_<n> позиционно и дрейфует, дропать его нельзя.
--
-- ЛОВУШКА РЕГИСТРА, унаследованная от 0265/0278: REGEXP наследует коллацию столбца, а под
-- utf8mb3_general_ci прода и utf8mb4_0900_ai_ci контейнерных тестов она регистронезависима — голый
-- шаблон принял бы 'SHELL'. `REGEXP BINARY` под utf8mb4 даёт 3995, поэтому байты сравниваются через
-- STRCMP с LOWER. Шаблон без префикса набора символов — его грепает TestBomLabelPartDBCheckNoDrift.

SET @col_exists := (
    SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'tech_card_bom_item'
      AND COLUMN_NAME = 'label_part'
);
SET @ddl := IF(@col_exists = 0,
    'ALTER TABLE tech_card_bom_item ADD COLUMN label_part VARCHAR(16) NULL COMMENT ''часть составника (care label); NULL = авто, дефолт по section/purpose выводит клиент'' AFTER kind_note, ADD CONSTRAINT chk_bom_item_label_part CHECK (label_part IS NULL OR (label_part REGEXP ''^(shell|body_lining|sleeve_lining|pocket_lining|hood_lining|filling|trim|not_on_label)$'' AND STRCMP(CAST(label_part AS BINARY), CAST(LOWER(label_part) AS BINARY)) = 0))',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- +migrate Down
SET @col_exists := (
    SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'tech_card_bom_item'
      AND COLUMN_NAME = 'label_part'
);
SET @ddl := IF(@col_exists = 1,
    'ALTER TABLE tech_card_bom_item DROP CONSTRAINT chk_bom_item_label_part, DROP COLUMN label_part',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;
