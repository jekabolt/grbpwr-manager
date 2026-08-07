-- +migrate Up

-- РЕЖИМ ГЕЙТА ГОТОВНОСТИ ПРОГОНА (Ф6.1/Ф6.9) — третий жилец дома настроек цеха.
--
-- НОМЕР. Ф6 планировалась на 0280-0282, но к моменту работы 0279 оказался первым по-настоящему
-- свободным: 0274 зарезервирован и не занят, 0278 занят ДВАЖДЫ (0278_bom_item_kind.sql и
-- 0278_marker_size_area.sql — две параллельные ветки взяли один номер и обе уже на бете, так что
-- коллизия дедовская и остаётся). Брать 0280, оставив 0279 висеть, значило бы завести вторую дыру
-- в нумерации без причины. sql-migrate ключует миграцию ПОЛНЫМ ИМЕНЕМ ФАЙЛА, поэтому переименовать
-- этот файл потом будет нельзя — номер выбран один раз и навсегда.
--
-- 0272 НАЗВАЛА ЭТОГО ЖИЛЬЦА ЗАРАНЕЕ, дословно: «Ф6.1 wants a report-only MODE and Ф6.9 a
-- blocking-mode switch — booleans. In a DECIMAL k/v table a boolean is a 0/1 lie; here it is a
-- BOOLEAN column» (0272:33-34). Это тот случай.
--
-- NULL ЗДЕСЬ ИМЕЕТ ОПРЕДЕЛЁННОЕ ПОВЕДЕНИЕ, И ЭТИМ ОН ОТЛИЧАЕТСЯ ОТ ВСЕХ ОСТАЛЬНЫХ КОЛОНОК ЭТОЙ
-- ТАБЛИЦЫ. У длины стола и у припуска NULL значит «нет вердикта»: потребитель обязан промолчать.
-- Здесь промолчать нельзя — создание прогона либо отбивается, либо нет, третьего состояния у
-- команды не бывает. Поэтому NULL (и FALSE) значат РЕЖИМ ОТЧЁТА: гейт считает всё, показывает всё,
-- пишет в лог и ПРОПУСКАЕТ.
--
-- Иначе выкатка колонки остановила бы производство целиком. В день релиза Ф6 ни одна карточка не
-- несёт нормы с записанными условиями съёмки — Ф3 объявила все прежние раскладки «старой нормой»
-- (07:84-88) — то есть правило «нет вердикта ⇒ отказ» отказало бы ВСЕМ. Настройка, умолчание
-- которой останавливает завод, не заводится.
--
-- TRUE СТАВИТСЯ ТОЛЬКО ПОСЛЕ КАМПАНИИ Д3 (пересъёмка норм), и порядок физически необратим:
-- направления (Д1) → пересъёмка после релиза Ф3 (Д3) → блокировка (Д4). Ставится одной командой
-- UpdateWorkshopSettings, без деплоя.
--
-- НИКАКОГО БЭКФИЛЛА: NULL на существующей singleton-строке И ЕСТЬ правильное «не настроено».
--
-- ИДЕМПОТЕНТНОСТЬ (CLAUDE.md): шаг под собственной проверкой в information_schema,
-- PREPARE/EXECUTE/DEALLOCATE по одному оператору на строку (прод подключается БЕЗ multiStatements,
-- и контейнерный тест это маскирует).

SET @col := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'workshop_settings'
      AND COLUMN_NAME = 'run_readiness_blocking');
SET @ddl := IF(@col = 0,
    'ALTER TABLE workshop_settings ADD COLUMN run_readiness_blocking BOOLEAN NULL COMMENT ''Ф6.9: TRUE = гейт готовности ОТБИВАЕТ создание прогона; FALSE/NULL = режим отчёта (считает и логирует, но пропускает)'' AFTER default_seam_allowance_cm',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- +migrate Down

-- ГВАРД ПЕРВЫМ, как в 0277, но по ЗЕРКАЛЬНОЙ причине, и её надо назвать вслух: опасен не откат
-- настроенного значения, а откат ВКЛЮЧЁННОЙ блокировки. Дроп колонки, на которой стоит TRUE, тихо
-- возвращает завод в режим отчёта — гейт перестаёт отбивать, и никто об этом не узнает, потому что
-- «пропустили» выглядит как «всё в порядке». Поэтому Down ОТКАЗЫВАЕТСЯ ронять колонку, на которой
-- блокировка включена, и говорит об этом текстом.
--
-- FALSE и NULL дропаются свободно: обе означают режим отчёта, то есть ровно то состояние, в которое
-- откат и возвращает. Терять там нечего.
--
-- Гвард обязан переживать частичный откат — он ссылается на колонку, которую этот же файл дропает,
-- поэтому запрос собирается PREPARE'ом после проверки наличия колонки.
SET @blocking := 0;
SET @have := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'workshop_settings'
      AND COLUMN_NAME = 'run_readiness_blocking');
SET @ddl := IF(@have = 0, 'SELECT 0 INTO @blocking',
    'SELECT (SELECT COUNT(*) FROM workshop_settings WHERE run_readiness_blocking = TRUE) INTO @blocking');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @ddl := IF(@blocking = 0, 'SELECT 1',
    -- The refusal travels as an unknown-identifier error, so the text has to survive MySQL truncating
    -- it: the actionable half comes FIRST and the explanation second.
    'SELECT `0279 Down refused: turn run_readiness_blocking OFF first. Dropping it while ON silently returns the shop to report-only`');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @col := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'workshop_settings'
      AND COLUMN_NAME = 'run_readiness_blocking');
SET @ddl := IF(@col > 0,
    'ALTER TABLE workshop_settings DROP COLUMN run_readiness_blocking',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;
