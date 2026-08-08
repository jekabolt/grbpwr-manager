-- +migrate Up

-- ВЫКРОЙКА БЕЗ РАЗМЕРА: tech_card_size_pattern.size_id становится NULLable.
--
-- ЗАЧЕМ. Градуированный DXF несёт ВЕСЬ размерный ряд в одном файле — размер записан в именах
-- блоков, и его читает браузер (deriveBlockSizes), а не сервер. Столбец size_id для такого листа
-- давно не факт о файле, а место хранения: клиент кладёт КАЖДЫЙ лист под наименьший размер ряда и
-- сам называет это артефактом (patterns-field.tsx). 0280 довёл эту мысль до конца — серверный ответ
-- «есть ли выкройка на этот размер» теперь даёт tech_card_pattern_size_index, а не этот столбец.
--
-- Пока столбец NOT NULL, у него остаётся ровно одно живое следствие, и оно вредное: НЕЛЬЗЯ ЗАЛИТЬ
-- DXF, ПОКА У КАРТОЧКИ ПУСТОЙ РАЗМЕРНЫЙ РЯД. Хранить лист не подо что, dto отвергает size_id вне
-- ряда — и загрузка выкроек оказывается заперта за решением, которое сам файл уже принял. Работа
-- при этом идёт в обратном порядке: конструктор отдаёт DXF раньше, чем кто-либо утверждает ряд.
--
-- NULL ЧИТАЕТСЯ КАК «РАЗМЕР ЖИВЁТ В ФАЙЛЕ», а не как «размер неизвестен и это дыра»: ни один
-- потребитель (просмотр, раскладка, сопоставление деталей, экспорт маркера, индекс 0280) size_id не
-- читает. Единственный, кто читал, — readiness.pattern_sizes, и Р4 (0280) уже увела эту строку
-- чеклиста на индекс; сам счётчик остался в фактах, но ни одного требования им больше не судят.
--
-- FK на size(id) СОХРАНЯЕТСЯ: NULL его не нарушает, а непустое значение по-прежнему обязано быть
-- живым размером. Индекс (tech_card_id, size_id) тоже остаётся — NULL в неуникальном индексе
-- законен, и выборки по карточке им пользуются как раньше.
--
-- fitting_pattern.size_id NULLable С 0077 — та же семантика, тот же смысл; эта миграция просто
-- доводит вторую таблицу до состояния первой.
--
-- Идемпотентна: MODIFY выполняется только когда столбец ещё NOT NULL.

SET @is_not_null := (
    SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'tech_card_size_pattern'
      AND COLUMN_NAME = 'size_id'
      AND IS_NULLABLE = 'NO'
);
SET @ddl := IF(@is_not_null = 1,
    'ALTER TABLE tech_card_size_pattern MODIFY COLUMN size_id INT NULL COMMENT ''FK size(id); NULL = лист градуированный, размеры живут в самом файле (см. tech_card_pattern_size_index)''',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- +migrate Down

-- Down обязан вернуть NOT NULL, а строки с NULL этого не переживут — до Up их не существовало, так
-- что откат разбирается с ними сам, и оба шага НЕОБРАТИМЫ по-разному. Сначала лист вешается на
-- любой живой размер своей карточки (какой именно — неважно ровно по той причине, по которой этот
-- столбец и стал NULLable), и только лист карточки, у которой размерного ряда нет вовсе, удаляется:
-- представить его в схеме NOT NULL нечем. Сам файл в бакете при этом остаётся — GC сносит объект
-- только когда его url перестаёт упоминать ЛЮБАЯ строка, а откат схемы через store не идёт.
--
-- Несколько листов, севших на один и тот же MIN(size), могут разделить один version внутри пары
-- (карточка, размер) — «Rev.2» дважды. Нумерация не уникальна схемой, это подпись для человека, и
-- следующий же сейв карточки пересчитает её штатно; разводить номера здесь значило бы писать в
-- откате логику, которой нет в самом накате.

UPDATE tech_card_size_pattern p
SET p.size_id = (SELECT MIN(z.size_id) FROM tech_card_size z WHERE z.tech_card_id = p.tech_card_id)
WHERE p.size_id IS NULL;

DELETE FROM tech_card_size_pattern WHERE size_id IS NULL;

SET @is_nullable := (
    SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'tech_card_size_pattern'
      AND COLUMN_NAME = 'size_id'
      AND IS_NULLABLE = 'YES'
);
SET @ddl := IF(@is_nullable = 1,
    'ALTER TABLE tech_card_size_pattern MODIFY COLUMN size_id INT NOT NULL COMMENT ''FK size(id); the graded size this выкройка is for''',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;
