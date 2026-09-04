-- Цвет PANTONE на строке спецификации — намерение стадии замысла.
--
-- ЗАЧЕМ, ЕСЛИ `material.pantone` УЖЕ ЕСТЬ. Артикул выбирают ПОЗЖЕ, чем решают цвет. На вкладке
-- CONSTRUCTION строка спецификации живёт с ролью, назначением и родом, но без `material_id` —
-- решение «эта деталь будет вот такого цвета» уже принято, а записать его некуда. Каталожный
-- цвет остаётся СТАРШЕ: он про то, что реально купят; это поле отвечает на вопрос раньше.
--
-- VARCHAR(64), СВОБОДНЫЙ ТЕКСТ, А НЕ СЛОВАРЬ: PANTONE это чужой каталог из тысяч кодов, он
-- обновляется, и наша копия устарела бы молча — отвергая законный код, которого мы не знаем.
-- 64 знака с запасом на самое длинное написание с системой («19-4005 TCX», «Black 6 C»).
--
-- NULL, А НЕ '': «цвет не решён» и «цвет решён как пустая строка» — разные состояния, и первое
-- у каждой существующей строки. Отсюда же и подпись секции: код входит в неё ПАРОЙ хвоста,
-- которая рождается только у заполненного поля, поэтому отпечаток ни одной сегодняшней строки
-- не двигается.
--
-- Идемпотентно, охрана по information_schema. ADD COLUMN без CHECK — INSTANT, таблица не
-- копируется и деплой не упирается в пятиминутный потолок.

-- +migrate Up

SET @bom_pantone := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_bom_item'
      AND COLUMN_NAME = 'pantone');
SET @ddl := IF(@bom_pantone = 0,
    'ALTER TABLE tech_card_bom_item
        ADD COLUMN pantone VARCHAR(64) NULL COMMENT ''цвет PANTONE строки до выбора артикула; каталожный material.pantone старше''',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- +migrate Down

SET @bom_pantone := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_bom_item'
      AND COLUMN_NAME = 'pantone');
SET @ddl := IF(@bom_pantone = 1, 'ALTER TABLE tech_card_bom_item DROP COLUMN pantone', 'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;
