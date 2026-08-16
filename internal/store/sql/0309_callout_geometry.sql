-- Геометрия карточной выноски: у указания на эскизе появляется вид, якоря и цвет.
--
-- До сих пор выноска карточки умела ровно одно — нумерованная точка с запиской. На снимке ШАГА
-- (0308) уже можно показать мерку между двумя точками, скобку над участком и дугу по окату, и
-- ровно этих указаний не хватало там, где их рисуют чаще всего: на техническом эскизе и на
-- мудборде. Заводить рядом ВТОРОЙ список выносок на той же картинке нельзя — человеку пришлось бы
-- выбирать, в какой из двух систем он сейчас рисует, а читателю смотреть в обе.
--
-- РАСШИРЕНИЕ АДДИТИВНОЕ. pos_x/pos_y СОХРАНЯЮТ смысл «где стоит нумерованный маркер»; points
-- держит якоря фигуры и у пина пуст. Поэтому каждая живая выноска читается ровно так, как читалась,
-- и дефолт 'pin' — не заглушка, а её настоящий вид.
--
-- ПОЧЕМУ ЯКОРЯ JSON-КОЛОНКОЙ, А НЕ ЧЕТВЁРТОЙ ТАБЛИЦЕЙ — тот же довод, что в 0308: у точки нет
-- внешних ссылок, она читается и пишется только целиком со своей выноской, а отдельные строки дали
-- бы на неё сослаться. Форму проверяет Go рядом с читаемым сообщением, до стора.
--
-- POINTS NULL, А НЕ NOT NULL. В 0308 колонка выносок объявлена NOT NULL, потому что таблица
-- рождалась пустой и писать её мог только новый стор. Здесь таблица ЖИВАЯ и полна строк, а JSON в
-- MySQL 8 не имеет литерального DEFAULT — NOT NULL пришлось бы дозаполнять UPDATE'ом по всей
-- истории. NULL здесь и означает ровно то, что случилось: «якорей никто не ставил».
--
-- CHECK на вид намеренно НЕ ставится: ретроактивный CHECK проверяет ВСЮ историю и роняет старт
-- прода, а словарь вида всё равно проверяется в Go, где отказ называет карточку, выноску и
-- допустимые значения — вместо сырого 3819 с именем колонки.
--
-- Идемпотентно: колонки добавляются одним охраняемым ALTER (MySQL не знает ADD COLUMN IF NOT
-- EXISTS), охрана — по первой из трёх, потому что добавляются они вместе или не добавляются вовсе.

-- +migrate Up

SET @callout_geom := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_callout'
      AND COLUMN_NAME = 'kind');
SET @ddl := IF(@callout_geom = 0,
    'ALTER TABLE tech_card_callout
        ADD COLUMN kind VARCHAR(16) NOT NULL DEFAULT ''pin'' COMMENT ''вид указания: pin|label|dim|bracket|multi|arc (см. TechCardAnnotationKind)'',
        ADD COLUMN color VARCHAR(16) NOT NULL DEFAULT '''' COMMENT ''цвет указания: пусто = чернильный; red|blue|green|orange'',
        ADD COLUMN points JSON NULL COMMENT ''якоря геометрии в долях кадра; NULL/[] = пин, маркер в pos_x/pos_y''',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- +migrate Down

SET @callout_geom_down := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_callout'
      AND COLUMN_NAME = 'kind');
SET @ddl_down := IF(@callout_geom_down = 1,
    'ALTER TABLE tech_card_callout DROP COLUMN points, DROP COLUMN color, DROP COLUMN kind',
    'SELECT 1');
PREPARE stmt FROM @ddl_down;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;
