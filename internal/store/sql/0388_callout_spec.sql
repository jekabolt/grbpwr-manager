-- Назначение выноски тех-карты: заметка, узел крупно, нанесение, строчка/шов, материал, разрез.
--
-- ОСЬ, А НЕ ЕЩЁ ОДИН ВИД — тот же довод, что у наконечников в 0362: вид говорит, ЧТО нарисовано
-- (точка, мерка, зона), а spec — ЗАЧЕМ. JSON-объект строкой; форму держит клиент, сервер проверяет
-- только «объект, не длиннее 16 КБ» и канонизирует ключи (entity.CanonicalCalloutSpec).
--
-- NULL = обычная выноска, как до 0388. Подпись DESIGN открывает четвёртый хвост только непустым
-- spec, поэтому отпечаток ни одной существующей карточки не меняется.
--
-- Только tech_card_callout: у примерки и у снимка шага назначений нет.
--
-- Идемпотентно: охраняемый ALTER. ADD COLUMN NULL в конец без CHECK — INSTANT, таблица не копируется.

-- +migrate Up

SET @tc_spec := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_callout'
      AND COLUMN_NAME = 'spec');
SET @ddl := IF(@tc_spec = 0,
    'ALTER TABLE tech_card_callout
        ADD COLUMN spec JSON NULL COMMENT ''назначение выноски: JSON-объект; NULL = обычная выноска''',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- +migrate Down

SET @tc_spec := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_callout'
      AND COLUMN_NAME = 'spec');
SET @ddl := IF(@tc_spec = 1, 'ALTER TABLE tech_card_callout DROP COLUMN spec', 'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;
