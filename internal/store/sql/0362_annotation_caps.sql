-- Наконечники линии у выноски: чем кончается отрезок или дуга.
--
-- ЗАЧЕМ ОТДЕЛЬНАЯ ОСЬ, А НЕ ЕЩЁ ОДИН ВИД. В наборе стояли два вида, различавшихся ТОЛЬКО концами:
-- `dim` (засечки) и `bracket` (скобки). Человек выбирал между ними как между разными
-- инструментами, хотя рисовал одну и ту же линию, а стрелки и точки — самые нужные концы на
-- фотографии узла — не выражались вовсе. Ещё три вида линии и четыре вида дуги дали бы девять
-- ключей вместо одного поля.
--
-- ПУСТАЯ СТРОКА — НЕ «БЕЗ НАКОНЕЧНИКОВ», А «ПО ВИДУ»: dim → засечки, bracket → скобки, arc → без.
-- Отсюда главное свойство выката: ни одна уже нарисованная выноска не меняет ни картинки, ни
-- байтов. Подпись секции DESIGN открывает третий хвост только заданным наконечником, поэтому
-- отпечаток карточки, где никто не выбирал концы, остаётся прежним.
--
-- ПОЧЕМУ NOT NULL DEFAULT '' , А НЕ NULL: у каждой существующей линии наконечник ЕСТЬ (тот, что
-- задаёт её вид), просто он не выбран отдельно. NULL означал бы «сведений нет», а сведения есть.
-- Та же логика, по которой 0310 завёл dashed как TINYINT с дефолтом, а не как NULL.
--
-- CHECK на согласованность (наконечник только у dim/bracket/arc) намеренно НЕ ставится:
-- ретроактивный CHECK проверяет ВСЮ историю и роняет старт прода — см. довод в 0310. Правило
-- живёт в Go: entity.NormalizeAnnotationCaps приводит наконечник к пустому у вида без концов.
--
-- ВЫНОСКИ СНИМКА ШАГА И ЗАДАЧИ СЮДА НЕ ВХОДЯТ: они лежат JSON-колонкой (`annotations`), и поле
-- добавляется в структуру, а не в схему. Здесь только две таблицы, где стиль выноски разложен
-- колонками, — 0310 (тех-карта) и 0319 (примерка).
--
-- Идемпотентно: два охраняемых ALTER, каждый по своей колонке. INSTANT-операция (ADD COLUMN в
-- конец без CHECK), поэтому таблицы не копируются и деплой не упирается в пятиминутный потолок.

-- +migrate Up

SET @tc_caps := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_callout'
      AND COLUMN_NAME = 'caps');
SET @ddl := IF(@tc_caps = 0,
    'ALTER TABLE tech_card_callout
        ADD COLUMN caps VARCHAR(16) NOT NULL DEFAULT '''' COMMENT ''наконечник линии: tick|bracket|bullet|arrow; пусто = по виду''',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @ft_caps := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'fitting_callout'
      AND COLUMN_NAME = 'caps');
SET @ddl := IF(@ft_caps = 0,
    'ALTER TABLE fitting_callout
        ADD COLUMN caps VARCHAR(16) NOT NULL DEFAULT '''' COMMENT ''наконечник линии: tick|bracket|bullet|arrow; пусто = по виду''',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- +migrate Down

SET @tc_caps := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_callout'
      AND COLUMN_NAME = 'caps');
SET @ddl := IF(@tc_caps = 1, 'ALTER TABLE tech_card_callout DROP COLUMN caps', 'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @ft_caps := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'fitting_callout'
      AND COLUMN_NAME = 'caps');
SET @ddl := IF(@ft_caps = 1, 'ALTER TABLE fitting_callout DROP COLUMN caps', 'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;
