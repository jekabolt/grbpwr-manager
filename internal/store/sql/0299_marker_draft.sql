-- +migrate Up

-- ЧЕРНОВАЯ РАСКЛАДКА. Движок раскладки крутится В БРАУЗЕРЕ оператора (парсера DXF на Go нет), а его
-- стоимость растёт КАК КВАДРАТ числа деталей, поэтому на большом наборе поиск успевает разложить
-- ЧАСТЬ деталей и упирается в бюджет. Это НОРМАЛЬНЫЙ исход, а не сбой. Сегодня он не оставляет
-- НИЧЕГО: SaveMarker отказывает на placed_count != total_count, минуты единственного исполнителя,
-- который у нас есть, выбрасывают, и оператор не видит даже следа того, что задание вообще считалось.
--
-- Колонка заводит третье состояние вместо второго отказа: частично уложенную раскладку МОЖНО хранить
-- — видеть, перезапускать — но она структурно неспособна стать нормой и двинуть деньги. Норму она не
-- получит по CHECK ниже; расход по ней не публикуется на проводе; в секцию настила она не встанет.
--
-- ГРАНИЦА СНИЗУ. Прогон, не уложивший НИ ОДНОЙ детали, маркера не оставляет вовсе: used_length_cm
-- обязан быть положительным, а API отказывает на пустом срезе размещений. Так что is_draft = TRUE
-- всегда означает «часть легла», и «поиск не начался» в этой таблице не выражается — такая запись
-- живёт у клиента.
--
-- ЧТО КОЛОНКА НЕ ДОКАЗЫВАЕТ. FALSE — это не «все детали легли», а «клиент прислал placed = total».
-- Числитель сервер сверяет с блобом (число размещений), знаменатель — нет: кратность деталей живёт в
-- карточке. Заниженный total_count и после этой миграции выдаёт неполную раскладку за полную, ровно
-- как до неё. Долг унаследован вместе с прежним отказом и назван в
-- ConvertPbTechCardMarkerInsertToEntity.
--
-- ПОЧЕМУ РЕТРОАКТИВНЫЙ CHECK ЗДЕСЬ БЕЗОПАСЕН. ADD CONSTRAINT проверяет ВСЮ историю таблицы, и
-- CHECK, ложный хотя бы на одной старой строке, останавливает старт прода (MYSQL_AUTOMIGRATE=true и
-- на бете, и на проде). Этот тривиально истинен на каждой существующей строке: колонка появляется в
-- том же ALTER со значением DEFAULT FALSE, то есть is_draft = FALSE у всех, а `is_draft = FALSE OR
-- ...` при этом истинно независимо от is_norm. Проверять нечего — потому его и можно ставить задним
-- числом, в отличие от потолка на уже заполненной колонке.
--
-- ИДЕМПОТЕНТНОСТЬ (CLAUDE.md): MySQL 8 автокоммитит DDL, поэтому падение в середине файла оставляет
-- схему полуприменённой без строки в gorp_migrations, и следующая загрузка перезапускает файл с
-- начала. Каждый шаг под собственной проверкой в information_schema,
-- PREPARE/EXECUTE/DEALLOCATE по одному оператору на строку (multiStatements=true в контейнерных
-- тестах замаскировал бы баг, который прод не переживёт).

-- 1. Колонка. Заводится ОТДЕЛЬНЫМ шагом от CHECK, чтобы повторный прогон файла после падения между
--    ними нашёл колонку на месте и поставил только ограничение.
SET @col := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_marker' AND COLUMN_NAME = 'is_draft');
SET @ddl := IF(@col = 0,
    'ALTER TABLE tech_card_marker
       ADD COLUMN is_draft BOOLEAN NOT NULL DEFAULT FALSE COMMENT ''движок не уложил все детали (placed_count < total_count); раскладку видно и можно перезапустить, но нормой она не станет и расход по ней не публикуется'' AFTER total_count',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- 2. «Черновик не бывает нормой» — инвариантом, а не соглашением. Гвард в Go (SetMarkerNorm) даёт
--    оператору предложение, по которому можно действовать; этот CHECK гарантирует, что путь,
--    забывший гвард, не запишет НИЧЕГО. Ровно то же разделение труда, что у chk_tcm_run_not_norm
--    (0282). Имя явное — DROP CHECK по позиционному <table>_chk_<n> запрещён (migrationlint).
SET @chk := (SELECT COUNT(*) FROM information_schema.TABLE_CONSTRAINTS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_marker'
      AND CONSTRAINT_NAME = 'chk_tcm_draft_not_norm');
SET @ddl := IF(@chk = 0,
    'ALTER TABLE tech_card_marker ADD CONSTRAINT chk_tcm_draft_not_norm CHECK (is_draft = FALSE OR is_norm = FALSE)',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- +migrate Down

-- ГВАРД ПЕРВЫМ, по доводу 0282. Дропнуть колонку значит НЕОБРАТИМО выдать черновики за измеренные
-- раскладки: в данных не останется, какая из них неполная, — а расход по ним сразу начнёт
-- публиковаться и его можно будет применить в рецепт. Отказ здесь дороже потери отката.
SET @have := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_marker' AND COLUMN_NAME = 'is_draft');
SET @ddl := IF(@have = 0, 'SELECT 0 INTO @blocking',
    'SELECT COUNT(*) INTO @blocking FROM tech_card_marker WHERE is_draft = TRUE');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @ddl := IF(@blocking = 0, 'SELECT 1',
    CONCAT('SELECT `0299 Down blocked: ', @blocking, ' markers are drafts`'));
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- Дальше — симметричные дропы в порядке, обратном Up, каждый под своей проверкой.
SET @chk := (SELECT COUNT(*) FROM information_schema.TABLE_CONSTRAINTS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_marker'
      AND CONSTRAINT_NAME = 'chk_tcm_draft_not_norm');
SET @ddl := IF(@chk > 0,
    'ALTER TABLE tech_card_marker DROP CHECK chk_tcm_draft_not_norm',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @col := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_marker' AND COLUMN_NAME = 'is_draft');
SET @ddl := IF(@col > 0,
    'ALTER TABLE tech_card_marker DROP COLUMN is_draft',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;
