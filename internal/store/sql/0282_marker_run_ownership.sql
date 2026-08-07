-- +migrate Up

-- РАСКРОЙНЫЙ МАРКЕР ПРИНАДЛЕЖИТ ПРОГОНУ (Ф4.2). Норма — актив карточки (Ф3), но маркеры секций
-- строятся под конкретный прогон, и храниться им сегодня негде, кроме таблицы карточки (05:64-74).
--
-- ПРЕФЛАЙТ. Эта миграция ставит инвариант «маркер прогона не может быть нормой», а колонку is_norm
-- заводит Ф3 (0276). Применённая ДО Ф3 миграция тихо пропустила бы инвариант и оставила бы дыру,
-- которую никто больше не закроет. Поэтому отсутствие is_norm — ОТКАЗ, а не пропуск. (0276 на бете
-- и на проде уже применена; гвард существует ради чистой базы, которая проигрывает набор с нуля в
-- неверном порядке, и ради ветки, где Ф3 откатили.)
--
-- ИДЕМПОТЕНТНОСТЬ (CLAUDE.md): MySQL 8 автокоммитит DDL, поэтому падение в середине файла оставляет
-- схему полуприменённой без строки в gorp_migrations, и следующая загрузка перезапускает файл с
-- начала. Каждый шаг под собственной проверкой в information_schema, PREPARE/EXECUTE/DEALLOCATE по
-- одному оператору на строку (multiStatements=true в контейнерных тестах замаскировал бы баг,
-- который прод не переживёт). Явный ALGORITHM/LOCK не пишется (довод 0273:54-57).
SET @has_norm := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_marker' AND COLUMN_NAME = 'is_norm');
SET @ddl := IF(@has_norm = 1, 'SELECT 1',
    'SELECT `0282 requires 0276 (is_norm) applied first`');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- 1. run_id + FK + индекс. CASCADE — это и есть «умирает вместе с прогоном»: не соглашение
--    приложения, а свойство схемы, которое переживёт любой путь удаления. Индекс объявляется ДО
--    внешнего ключа в том же ALTER, чтобы InnoDB взял его для FK, а не завёл рядом второй под
--    именем ограничения.
SET @col := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_marker' AND COLUMN_NAME = 'run_id');
SET @ddl := IF(@col = 0,
    'ALTER TABLE tech_card_marker
       ADD COLUMN run_id INT NULL COMMENT ''прогон, под который снят РАСКРОЙНЫЙ маркер; NULL = карточный маркер (норма и все сегодняшние). Скрыт из списков карточки, умирает с прогоном'' AFTER colorway_id,
       ADD INDEX idx_tcm_run (run_id),
       ADD CONSTRAINT fk_tcm_run FOREIGN KEY (run_id) REFERENCES production_run (id) ON DELETE CASCADE',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- 2. run_key — генерируемая колонка, существующая ТОЛЬКО чтобы уникальность имени пережила второй
--    прогон. Та же ловушка и то же лечение, что size_key в 0273:88-99: uniq_tcm_card_sizekey_name
--    складывает ВСЕ маркеры с составом в ведро (card, 0, name), и два прогона одной модели с
--    раскладкой «основная 40-42» столкнутся. IFNULL(run_id, 0) безопасен по построению: до этой
--    миграции run_id NULL у КАЖДОЙ строки, значит новый индекс тождествен старому и упасть на
--    существующих данных не может.
SET @col := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_marker' AND COLUMN_NAME = 'run_key');
SET @ddl := IF(@col = 0,
    'ALTER TABLE tech_card_marker ADD COLUMN run_key INT AS (IFNULL(run_id, 0)) VIRTUAL COMMENT ''0 = карточный маркер; существует только чтобы уникальность имени пережила второй прогон''',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- 3. Новый индекс ДО дропа старого: если файл упадёт между ними, уникальность останется СТРОЖЕ
--    нужного, а не слабее. Обратный порядок открыл бы окно, в котором дубликат имени вставляется.
SET @idx := (SELECT COUNT(*) FROM information_schema.STATISTICS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_marker'
      AND INDEX_NAME = 'uniq_tcm_card_run_sizekey_name');
SET @ddl := IF(@idx = 0,
    'ALTER TABLE tech_card_marker ADD CONSTRAINT uniq_tcm_card_run_sizekey_name UNIQUE (tech_card_id, run_key, size_key, name)',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @idx := (SELECT COUNT(*) FROM information_schema.STATISTICS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_marker'
      AND INDEX_NAME = 'uniq_tcm_card_sizekey_name');
SET @ddl := IF(@idx > 0,
    'ALTER TABLE tech_card_marker DROP INDEX uniq_tcm_card_sizekey_name',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- 3b. ВТОРОЙ УЗКИЙ ИНДЕКС, КОТОРЫЙ СПЕКА НЕ ПЕРЕЧИСЛИЛА, И ОН ЛОМАЕТ ТОТ ЖЕ СЦЕНАРИЙ. 0273 завела
--     uniq_tcm_card_sizekey_name, но НЕ ДРОПНУЛА uniq_tcm_card_size_name из 0257:56 — она и сегодня
--     живёт на таблице как UNIQUE (tech_card_id, size_id, name). Для маркера С СОСТАВОМ она мертва
--     (size_id NULL, а NULL ≠ NULL — ровно та дыра, ради которой 0273 и завела size_key), но
--     раскройный маркер НЕ ОБЯЗАН иметь состав: TechCardMarkerInsert по сей день принимает легаси-
--     форму (size_id > 0 + sets >= 1), и копия однородной нормы под второй прогон приедет именно
--     такой. Тогда второй прогон той же модели упирается в старый индекс — то есть в тот же отказ,
--     от которого шаг 3 только что защитил, просто с другого бока.
--
--     Расширяется тем же run_key и с тем же доказательством безопасности: до этой миграции run_id
--     NULL у КАЖДОЙ строки, значит run_key ≡ 0, значит новый индекс тождествен старому и упасть на
--     существующих данных не может. Порядок тот же: новый ДО дропа старого.
SET @idx := (SELECT COUNT(*) FROM information_schema.STATISTICS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_marker'
      AND INDEX_NAME = 'uniq_tcm_card_run_size_name');
SET @ddl := IF(@idx = 0,
    'ALTER TABLE tech_card_marker ADD CONSTRAINT uniq_tcm_card_run_size_name UNIQUE (tech_card_id, run_key, size_id, name)',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @idx := (SELECT COUNT(*) FROM information_schema.STATISTICS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_marker'
      AND INDEX_NAME = 'uniq_tcm_card_size_name');
SET @ddl := IF(@idx > 0,
    'ALTER TABLE tech_card_marker DROP INDEX uniq_tcm_card_size_name',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- 4. «Норма — единственный выживающий» (05:73) — инвариантом, а не соглашением. Маркер прогона
--    физически не может нести признак нормы, поэтому смерть прогона не может унести норму, а
--    транзакция SetMarkerNorm (Ф3) не может выбрать норму из однодневок. На существующих данных
--    CHECK тривиально истинен: run_id NULL у каждой строки.
SET @chk := (SELECT COUNT(*) FROM information_schema.TABLE_CONSTRAINTS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_marker'
      AND CONSTRAINT_NAME = 'chk_tcm_run_not_norm');
SET @ddl := IF(@chk = 0,
    'ALTER TABLE tech_card_marker ADD CONSTRAINT chk_tcm_run_not_norm CHECK (run_id IS NULL OR is_norm = FALSE)',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- +migrate Down

-- ГВАРД ПЕРВЫМ. Дропнуть run_id значит вывалить раскройные однодневки в список карточки
-- НЕОБРАТИМО — какой маркер был прогонным, в данных не останется. Хуже: возврат узкого индекса
-- uniq_tcm_card_sizekey_name упал бы на дубликате имён из двух прогонов, но упал бы УЖЕ ПОСЛЕ
-- дропа колонки, то есть после потери данных.
SET @have := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_marker' AND COLUMN_NAME = 'run_id');
SET @ddl := IF(@have = 0, 'SELECT 0 INTO @blocking',
    'SELECT COUNT(*) INTO @blocking FROM tech_card_marker WHERE run_id IS NOT NULL');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @ddl := IF(@blocking = 0, 'SELECT 1',
    CONCAT('SELECT `0282 Down blocked: ', @blocking, ' markers belong to a run`'));
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- Дальше — симметричные дропы, каждый под своей проверкой information_schema, в порядке, обратном
-- Up: CHECK → старый индекс обратно → новый индекс → run_key → FK → индекс FK → колонка. Узкий
-- индекс возвращается ДО дропа широкого по тому же доводу, что и в Up: окно между ними держит
-- уникальность строже нужного, а не слабее. Он не может упасть: гвард выше уже доказал, что
-- run_id NULL у каждой строки, то есть run_key ≡ 0 и широкий индекс тождествен узкому.
SET @chk := (SELECT COUNT(*) FROM information_schema.TABLE_CONSTRAINTS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_marker'
      AND CONSTRAINT_NAME = 'chk_tcm_run_not_norm');
SET @ddl := IF(@chk > 0,
    'ALTER TABLE tech_card_marker DROP CHECK chk_tcm_run_not_norm',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @idx := (SELECT COUNT(*) FROM information_schema.STATISTICS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_marker'
      AND INDEX_NAME = 'uniq_tcm_card_sizekey_name');
SET @ddl := IF(@idx = 0,
    'ALTER TABLE tech_card_marker ADD CONSTRAINT uniq_tcm_card_sizekey_name UNIQUE (tech_card_id, size_key, name)',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @idx := (SELECT COUNT(*) FROM information_schema.STATISTICS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_marker'
      AND INDEX_NAME = 'uniq_tcm_card_run_sizekey_name');
SET @ddl := IF(@idx > 0,
    'ALTER TABLE tech_card_marker DROP INDEX uniq_tcm_card_run_sizekey_name',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- Зеркало шага 3b: узкий индекс 0257 возвращается, широкий уходит. Тот же порядок и то же
-- доказательство — гвард выше уже установил, что run_key ≡ 0.
SET @idx := (SELECT COUNT(*) FROM information_schema.STATISTICS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_marker'
      AND INDEX_NAME = 'uniq_tcm_card_size_name');
SET @ddl := IF(@idx = 0,
    'ALTER TABLE tech_card_marker ADD CONSTRAINT uniq_tcm_card_size_name UNIQUE (tech_card_id, size_id, name)',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @idx := (SELECT COUNT(*) FROM information_schema.STATISTICS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_marker'
      AND INDEX_NAME = 'uniq_tcm_card_run_size_name');
SET @ddl := IF(@idx > 0,
    'ALTER TABLE tech_card_marker DROP INDEX uniq_tcm_card_run_size_name',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @col := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_marker' AND COLUMN_NAME = 'run_key');
SET @ddl := IF(@col > 0,
    'ALTER TABLE tech_card_marker DROP COLUMN run_key',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @fk := (SELECT COUNT(*) FROM information_schema.TABLE_CONSTRAINTS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_marker'
      AND CONSTRAINT_NAME = 'fk_tcm_run' AND CONSTRAINT_TYPE = 'FOREIGN KEY');
SET @ddl := IF(@fk > 0,
    'ALTER TABLE tech_card_marker DROP FOREIGN KEY fk_tcm_run',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @idx := (SELECT COUNT(*) FROM information_schema.STATISTICS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_marker'
      AND INDEX_NAME = 'idx_tcm_run');
SET @ddl := IF(@idx > 0,
    'ALTER TABLE tech_card_marker DROP INDEX idx_tcm_run',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @col := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_marker' AND COLUMN_NAME = 'run_id');
SET @ddl := IF(@col > 0,
    'ALTER TABLE tech_card_marker DROP COLUMN run_id',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;
