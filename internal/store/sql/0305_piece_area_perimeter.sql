-- +migrate Up

-- ПЕРИМЕТР КОНТУРА РЯДОМ С ЕГО ПЛОЩАДЬЮ (Ф1). Второе — и единственное недостающее — число, которым
-- описывается КРАЕВОЕ дублирование: клеевая, положенная полосой вдоль среза, стоит `периметр ×
-- ширину полосы`, тогда как площадь отвечает только на вопрос про дублирование ЦЕЛИКОМ (0304).
--
-- ПОЧЕМУ В ЭТУ ЖЕ ТАБЛИЦУ, А НЕ В СВОЮ. Периметр — не отдельный факт про деталь, а ВТОРАЯ МЕРА ТОГО
-- ЖЕ САМОГО КОНТУРА, снятая тем же разбором того же листа. Всё, что делает площадь числом, а не
-- мнением, слово в слово относится и к нему: слой контура (шов надо раздуть припуском, крой — уже
-- нет), сам припуск, выпуклая оболочка, неоднозначный выбор кандидата, отпечаток набора листов,
-- кто и когда подтвердил замер. Отдельная таблица означала бы второй экземпляр каждого из этих
-- условий — и первый же день, когда они разъедутся, даст периметр, снятый по одному слою, рядом с
-- площадью по другому. Здесь же они не могут разъехаться by construction: одна строка, одна
-- транзакция, один отпечаток, одно устаревание.
--
-- NULL — ЗАКОННОЕ ЗНАЧЕНИЕ, и это не переходный период, а постоянное состояние. Все уже снятые
-- замеры (0297) периметра не несут: клиент его тогда не считал. Такая строка честно означает
-- «площадь есть, периметра нет» — по ней считается дублирование целиком и ОТКАЗЫВАЕТСЯ краевое
-- (entity.AreaEstimateNoPerimeter), вместо того чтобы вывести полосу из площади через какую-нибудь
-- правдоподобную формулу. Правдоподобной формулы не существует: одна и та же площадь бывает у
-- компактной детали с коротким периметром и у длинной узкой с периметром вдвое больше, и ошибка
-- ушла бы прямиком в закупку клеевой.
--
-- БЭКФИЛЛА НЕТ И БЫТЬ НЕ МОЖЕТ. Периметр считается по контуру, а контуры живут в DXF, который
-- разбирает ТОЛЬКО браузер (парсера DXF в Go нет и не будет — 0280, 0297). Сервер физически не
-- может вычислить это число; оно появляется, когда оператор в следующий раз пересчитает площади
-- этого скоупа. Ровно поэтому отказ обязан называть недостающий факт по имени, а не молчать.
--
-- ЕДИНИЦА — САНТИМЕТРЫ, как у площади (см²) и у всей геометрии этой таблицы, хотя ширина полосы на
-- детали хранится в миллиметрах (0304). Это не разнобой: единицу выбирает тот, кто ВВОДИТ, а
-- периметр никто не вводит — его считает разбор, и он обязан быть в той же системе, что площадь,
-- иначе первое же произведение разойдётся в 10 раз. Перевод мм→см делает ОДНО место
-- (entity.AreaEstimateNorm), у которого оба сомножителя на руках.
--
-- DECIMAL(12,2) — та же форма, что у area_cm2 соседней колонкой. Периметр всегда меньше площади в
-- числах реального размера (полочка: 4200 см² против ~260 см), так что запас заведомо избыточен;
-- взята та же форма, потому что разная точность у двух мер одного контура — это приглашение
-- однажды объяснять, почему одна округлилась, а другая нет.
--
-- РЕТРОАКТИВНОСТЬ БЕЗОПАСНА BY CONSTRUCTION (ловушка, останавливавшая старт прода): CHECK добавляется
-- к колонке, заведённой NULL'ом в этой же миграции, и первая дизъюнкция пропускает всю историю.
--
-- ИДЕМПОТЕНТНОСТЬ (CLAUDE.md): каждый шаг под своей проверкой в information_schema,
-- PREPARE/EXECUTE/DEALLOCATE по одному оператору на строку.

SET @col_exists := (
    SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'tech_card_piece_area'
      AND COLUMN_NAME = 'perimeter_cm'
);
SET @ddl := IF(@col_exists = 0,
    'ALTER TABLE tech_card_piece_area ADD COLUMN perimeter_cm DECIMAL(12,2) NULL COMMENT ''периметр ОДНОГО экземпляра контура, см, по тем же условиям замера; NULL = не снимался (замер до 0305)'' AFTER area_cm2',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @chk := (
    SELECT COUNT(*) FROM information_schema.TABLE_CONSTRAINTS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'tech_card_piece_area'
      AND CONSTRAINT_NAME = 'chk_tcpa_perimeter_positive'
);
SET @ddl := IF(@chk = 0,
    'ALTER TABLE tech_card_piece_area ADD CONSTRAINT chk_tcpa_perimeter_positive CHECK (perimeter_cm IS NULL OR perimeter_cm > 0)',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- +migrate Down

SET @chk := (
    SELECT COUNT(*) FROM information_schema.TABLE_CONSTRAINTS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'tech_card_piece_area'
      AND CONSTRAINT_NAME = 'chk_tcpa_perimeter_positive'
);
SET @ddl := IF(@chk > 0,
    'ALTER TABLE tech_card_piece_area DROP CHECK chk_tcpa_perimeter_positive',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @col_exists := (
    SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'tech_card_piece_area'
      AND COLUMN_NAME = 'perimeter_cm'
);
SET @ddl := IF(@col_exists > 0,
    'ALTER TABLE tech_card_piece_area DROP COLUMN perimeter_cm',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;
