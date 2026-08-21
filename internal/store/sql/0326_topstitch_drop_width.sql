-- +migrate Up

-- РЕЖИМ ОТСТРОЧКИ `width` СНЯТ: ОН И `edge` БЫЛИ ОДНИМ ПРИЁМОМ, А НЕ ДВУМЯ.
--
-- 0289 завёл словарь двумя членами — `edge` («в край») и `width` («на заданной ширине»), — и это
-- выглядело как выбор приёма. На деле оба описывают ОДНУ строчку, отмеренную ОТ КРАЯ ДЕТАЛИ, и
-- различаются ровно тем, названо ли число. Технолог у пикера выбирал не приём, а заполненность
-- соседней ячейки — и получал два написания одного факта, которые дальше расходились бы по
-- печатному листу и по кортежу дайджеста молча. Поэтому режимов теперь ТРИ:
--
--   edge             — от КРАЯ ДЕТАЛИ; ширина ОПЦИОНАЛЬНА (заполнена = отступ в мм, пуста = вплотную)
--   in_ditch         — В САМ ШОВ; ширина ОТВЕРГАЕТСЯ, её у такой строчки нет и быть не может
--   parallel_to_seam — от ЛИНИИ ШВА; ширина ОБЯЗАТЕЛЬНА, без числа это не инструкция
--
-- Правило «ширина при edge отвергается», которое сервер держал с 0289, ОТМЕНЕНО. Оно живёт в
-- parseTopstitch (internal/dto/techcard_production.go), не здесь: двухколоночная проверка
-- «режим + ширина» в CHECK'е перечитывала бы всю таблицу и отвечала бы голым 3819 без имени поля.
--
-- ⚠️ ЭТО СУЖЕНИЕ СЛОВАРНОГО CHECK'А, А НЕ РАСШИРЕНИЕ, И ЭТО РОВНО ТОТ МЕХАНИЗМ, КОТОРЫЙ ОДНАЖДЫ
-- ОСТАНОВИЛ СТАРТ ПРОДА. `ADD CONSTRAINT ... CHECK` в MySQL 8 проверяет ВСЮ ИСТОРИЮ таблицы, а не
-- будущие записи: одна строка со снятым токеном — и ALTER падает, миграция не дописывает строку в
-- gorp_migrations, приложение не стартует. Все предыдущие волны (0324, 0325) поэтому словари
-- только РАСШИРЯЛИ. Здесь сужение законно по ЕДИНСТВЕННОЙ причине — переносить нечего:
--
-- ОСНОВАНИЕ — ЗАМЕР ОБЕИХ БАЗ, А НЕ РАССУЖДЕНИЕ (2026-08-21, до написания файла):
--   SELECT COUNT(*), SUM(topstitch_mode IS NOT NULL), SUM(topstitch_mode = 'width'),
--          SUM(topstitch_width_mm IS NOT NULL) FROM tech_card_operation;
--     ПРОД (grbpwr):      99 строк — topstitch_mode заполнен у 0, 'width' у 0, ширина у 0
--     БЕТА (grbpwr_beta): 11 строк — topstitch_mode заполнен у 0, 'width' у 0, ширина у 0
--   tech_card_signoff: 0 строк в ОБЕИХ базах — ни одной подписи, которую снятие могло бы
--     объявить устаревшей.
--   tech_card_release: 0 на проде, 2 на бете. Обе бетовые не могут нести этот режим: релиз
--     снимается с ОПЕРАЦИЙ, а topstitch_mode не заполнен ни у одной из одиннадцати. Отдельной
--     копии колонки у релиза нет — во всей схеме есть РОВНО ОДНА колонка topstitch_mode
--     (information_schema: tech_card_operation), и этот CHECK единственный, кто её сторожит.
--
-- Иначе говоря: колонка родилась в 0289, прожила до сегодня и не приняла ни одного значения.
-- ЕСЛИ ЗАМЕР УСТАРЕЕТ (кто-то успеет записать 'width' между этой строкой и выкаткой) — файл
-- УПАДЁТ ЧЕСТНО, на ADD CONSTRAINT, с 3819 и именем констрейнта, а не запишет ложь в схему.
-- Это проверено на одноразовом контейнере подложенной строкой, а не выведено из документации.
--
-- Номер 2 и имя TECH_CARD_TOPSTITCH_MODE_WIDTH в proto ЗАРЕЗЕРВИРОВАНЫ: отданные новому смыслу,
-- они читались бы старым клиентом как прежний член — без единой ошибки на проводе.
--
-- ОДИН ALTER, А НЕ ДВА. ADD CONSTRAINT CHECK в 8.0 поддержан только ALGORITHM=COPY, то есть
-- таблица копируется целиком; из спецификаций одного оператора движок берёт самую строгую,
-- поэтому переписать заодно COMMENT колонки (он до сих пор называл `width` как отдельный приём)
-- стоит ровно НОЛЬ сверх уже оплаченной копии. Разбить на два ALTER'а значило бы заплатить
-- за копию дважды. MODIFY стоит ПЕРВЫМ и до ADD CONSTRAINT — тот же порядок, что в 0324:
-- констрейнт нельзя писать против колонки, которая описана иначе, чем он допускает. Ширина
-- VARCHAR(16) НЕ МЕНЯЕТСЯ (её выставил 0324 под `parallel_to_seam`) и повторяется здесь лишь
-- потому, что MODIFY COLUMN обязан назвать тип целиком.
--
-- ГЕЙТ ИДЕМПОТЕНТНОСТИ — по содержимому CHECK'а, а не по факту его существования: констрейнт с
-- этим именем есть и до, и после файла, и «он есть» не отличает состояний. Токен `width` внутри
-- CHECK_CLAUSE встречается ровно один раз и только как член альтернации (колонка одна, слова
-- «width» в её имени нет), поэтому LIKE '%width%' — точная проба. Повторный прогон после успеха
-- находит клаузу без него и не делает ничего.
SET @chk_op_topstitch_width := (SELECT COUNT(*) FROM information_schema.TABLE_CONSTRAINTS tc
    JOIN information_schema.CHECK_CONSTRAINTS cc
      ON cc.CONSTRAINT_SCHEMA = tc.CONSTRAINT_SCHEMA AND cc.CONSTRAINT_NAME = tc.CONSTRAINT_NAME
    WHERE tc.TABLE_SCHEMA = DATABASE() AND tc.TABLE_NAME = 'tech_card_operation'
      AND tc.CONSTRAINT_NAME = 'chk_op_topstitch_mode' AND cc.CHECK_CLAUSE LIKE '%width%');
SET @ddl := IF(@chk_op_topstitch_width = 1,
    'ALTER TABLE tech_card_operation
        MODIFY COLUMN topstitch_mode VARCHAR(16) NULL COMMENT ''edge = от края детали (ширина опциональна: есть = отступ в мм, нет = вплотную), in_ditch = в шов (ширины нет), parallel_to_seam = от линии шва (ширина обязательна); NULL = отстрочки нет'',
        DROP CHECK chk_op_topstitch_mode,
        ADD CONSTRAINT chk_op_topstitch_mode CHECK (topstitch_mode IS NULL OR (topstitch_mode REGEXP ''^(edge|in_ditch|parallel_to_seam)$'' AND STRCMP(CAST(topstitch_mode AS BINARY), CAST(LOWER(topstitch_mode) AS BINARY)) = 0))',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- НИ ОДНОГО UPDATE'А В ФАЙЛЕ НЕТ. Переписывать 'width' в 'edge' было бы нечего (строк ноль), а
-- код такой миграции пришлось бы держать вечно ради случая, которого не существует. Колонка
-- topstitch_width_mm НЕ ТРОГАЕТСЯ вовсе: её значения законны при двух режимах из трёх, и это
-- решает Go, а не схема.

-- +migrate Down

-- Down ВОЗВРАЩАЕТ `width` В СЛОВАРЬ — и это, в отличие от Up, безопасно всегда: расширение
-- альтернации ничего ретроактивно не отвергает. Возвращается список ИМЕННО 0324 (четыре члена),
-- потому что откат этого файла оставляет схему на 0325, где действовал он.
--
-- Гейт зеркальный: откатывать есть что, только пока в клаузе НЕТ `width`.
SET @chk_op_topstitch_back := (SELECT COUNT(*) FROM information_schema.TABLE_CONSTRAINTS tc
    JOIN information_schema.CHECK_CONSTRAINTS cc
      ON cc.CONSTRAINT_SCHEMA = tc.CONSTRAINT_SCHEMA AND cc.CONSTRAINT_NAME = tc.CONSTRAINT_NAME
    WHERE tc.TABLE_SCHEMA = DATABASE() AND tc.TABLE_NAME = 'tech_card_operation'
      AND tc.CONSTRAINT_NAME = 'chk_op_topstitch_mode' AND cc.CHECK_CLAUSE NOT LIKE '%width%');
SET @ddl := IF(@chk_op_topstitch_back = 1,
    'ALTER TABLE tech_card_operation
        MODIFY COLUMN topstitch_mode VARCHAR(16) NULL COMMENT ''edge = в край, width = на заданной ширине, in_ditch = в шов, parallel_to_seam = параллельно шву; NULL = отстрочки нет'',
        DROP CHECK chk_op_topstitch_mode,
        ADD CONSTRAINT chk_op_topstitch_mode CHECK (topstitch_mode IS NULL OR (topstitch_mode REGEXP ''^(edge|width|in_ditch|parallel_to_seam)$'' AND STRCMP(CAST(topstitch_mode AS BINARY), CAST(LOWER(topstitch_mode) AS BINARY)) = 0))',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;
