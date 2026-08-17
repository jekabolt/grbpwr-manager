-- +migrate Up

-- ГРУППИРОВКА ФАЙЛОВ: ТЕМА-ПРОЕКТ И РОЛЬ ФАЙЛА В НЁМ.
--
-- РОЛЬ ЖИВЁТ НА СТРОКЕ СВЯЗИ, А НЕ МЕТКОЙ НА ФАЙЛЕ, И ЭТО ВЕСЬ СМЫСЛ МИГРАЦИИ. Плоский набор
-- меток теряет пару: снимок лежит в съёмке как «отобранное» и в лукбуке как «референс», на файле
-- оказывается {съёмка, лукбук, отобранное, референс}, и пересечение «съёмка × референс» находит
-- его — МОЛЧА и ЛОЖНО. Выдача при этом выглядит правдоподобной, и проверить её нечем.
--
-- `library_file_topic.role_id` выражает пару точно: строка (F, съёмка, отобранное) и строка
-- (F, лукбук, референс) — два разных факта, и перекрёстный запрос не находит ничего. Правило
-- «одна роль на файл в проекте» при этом получается ДАРОМ из уже стоящего UNIQUE(file_id,
-- topic_id) (0312:74): на пару приходится ровно одна строка, у строки ровно одно поле роли. Ни
-- констрейнта, ни проверки в сторе для этого не нужно.
--
-- ПОЧЕМУ РОЛИ — СВОЯ ТАБЛИЦА, А НЕ ТРЕТЬЕ ЗНАЧЕНИЕ `kind`. Роль в file_topic пришлось бы
-- исключать в каждом уже отгруженном пути, который перечисляет темы со счётчиками (рельс, чипы,
-- четыре пикера) — шесть-семь правок в коде, который только что прошёл ревью. Отдельная таблица
-- не трогает ни один из них. Заодно затравка не может столкнуться с одноимённой темой: заведи
-- владелец тему «исходники» руками, INSERT IGNORE в file_topic молча не сделал бы ничего.
--
-- ИДЕМПОТЕНТНОСТЬ (CLAUDE.md): MySQL 8 авто-коммитит DDL, поэтому падение в середине файла
-- оставляет схему полуприменённой БЕЗ строки в gorp_migrations, и следующая загрузка перезапускает
-- файл с начала. Каждый ALTER — под своим гейтом information_schema, ОТДЕЛЬНЫМ независимым
-- оператором; у CREATE INDEX в MySQL нет IF NOT EXISTS вовсе, поэтому гейт нужен и ему (по
-- information_schema.STATISTICS). PREPARE / EXECUTE / DEALLOCATE — по одному оператору на строку
-- (multiStatements=true в контейнерных тестах иначе маскирует поломку, которая на проде валит
-- старт). Ретроактивных CHECK нет ни одного: они проверяют ВСЮ историю и останавливают старт
-- прода. Разрушительных операций нет: на бете уже лежат заведённые руками темы, и все они
-- получают kind='plain' из DEFAULT — бэкфилла нет, догадки «это похоже на проект» нет (она
-- ошиблась бы на `collections` и `products`, а молча переставленный тип потом не найти).

-- 1. Тип темы: обычный ярлык или проект. РОЛИ ЗДЕСЬ НЕТ — она в отдельной таблице ниже.
SET @ft_kind := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'file_topic'
      AND COLUMN_NAME = 'kind');
SET @ddl := IF(@ft_kind = 0,
    'ALTER TABLE file_topic
        ADD COLUMN kind ENUM(''plain'', ''project'') NOT NULL DEFAULT ''plain''
            COMMENT ''обычная тема-ярлык или проект (даты, разбивка по ролям, своя страница); существующие темы остаются plain''',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- 2. Начало проекта. DATE, а не TIMESTAMP: съёмка идёт «12–14 сентября», часовой пояс здесь
--    не значит ничего и только породил бы вопрос, в чьей полуночи начинается день.
SET @ft_starts := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'file_topic'
      AND COLUMN_NAME = 'starts_at');
SET @ddl := IF(@ft_starts = 0,
    'ALTER TABLE file_topic
        ADD COLUMN starts_at DATE NULL COMMENT ''начало проекта; NULL = не задано''',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- 3. Конец проекта.
SET @ft_ends := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'file_topic'
      AND COLUMN_NAME = 'ends_at');
SET @ddl := IF(@ft_ends = 0,
    'ALTER TABLE file_topic
        ADD COLUMN ends_at DATE NULL COMMENT ''конец проекта; NULL = не задано''',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- 4. Архив. Он НЕ УКРАШЕНИЕ: проекты копятся, а роли нет — двадцать съёмок за год дают двадцать
--    чипов в ряду, и без архива ряд проектов растёт монотонно и никогда не сокращается, то есть
--    группировка со временем делает холст ХУЖЕ. Удалить проект нельзя почти никогда (файлы в нём
--    есть всегда, а FK на тему стоит без каскада), поэтому архив — единственный штатный способ
--    убрать законченную съёмку с глаз. Применим и к обычным темам.
SET @ft_arch := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'file_topic'
      AND COLUMN_NAME = 'archived_at');
SET @ddl := IF(@ft_arch = 0,
    'ALTER TABLE file_topic
        ADD COLUMN archived_at TIMESTAMP NULL DEFAULT NULL
            COMMENT ''в архиве с этого момента: пропадает из чипов и пикеров, остаётся на экране тем и по прямой ссылке; NULL = не в архиве''',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- 5. Индекс под «проекты, не в архиве» — ряд чипов спрашивает ровно это.
--    У CREATE INDEX нет IF NOT EXISTS, поэтому гейт по STATISTICS, а не по надежде.
SET @ft_kind_idx := (SELECT COUNT(*) FROM information_schema.STATISTICS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'file_topic'
      AND INDEX_NAME = 'idx_file_topic_kind');
SET @ddl := IF(@ft_kind_idx = 0,
    'CREATE INDEX idx_file_topic_kind ON file_topic (kind, archived_at)',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- 6. Словарь ролей — ЗАКРЫТЫЙ. Заводится ровно одним действием с экрана тем; ни `new_topics`, ни
--    модалка вставки, ни массовое проставление тем роли не создают — они пишут в file_topic, а
--    роли живут здесь. Закрытость нужна не для порядка: сквозной вопрос «все исходники по всем
--    съёмкам» имеет смысл, только если «исходники» — одна и та же сущность везде. Свободный текст
--    расходится гарантированно (исходники / исходные / raw / сырцы), и фильтр начинает молча
--    возвращать половину правды. Ошибка ввода чинится слиянием ролей, а не запретом.
CREATE TABLE IF NOT EXISTS file_role (
  id INT PRIMARY KEY AUTO_INCREMENT,
  name VARCHAR(64) NOT NULL COMMENT 'название роли; уникально регистро-независимо (коллация ai_ci)',
  sort_order INT NOT NULL DEFAULT 0 COMMENT 'порядок секций на странице проекта; равные сортируются по имени',
  archived_at TIMESTAMP NULL DEFAULT NULL COMMENT 'в архиве: пропадает из чипов и пикеров, остаётся на экране тем; NULL = не в архиве',
  created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  UNIQUE KEY uniq_file_role_name (name),
  INDEX idx_file_role_order (archived_at, sort_order)
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4
  COMMENT 'Словарь ролей файла в проекте: закрытый, заводится только с экрана тем';

-- Затравка. Дальше набор доводит владелец; за первый месяц он устаканится и больше не изменится.
INSERT IGNORE INTO file_role (name, sort_order) VALUES
  ('исходники', 10), ('обработанные', 20), ('идея', 30), ('планирование', 40);

-- 7. РОЛЬ ФАЙЛА ИМЕННО В ЭТОЙ ТЕМЕ. ADD COLUMN и ADD CONSTRAINT ОДНИМ оператором, чтобы
--    половинчатого состояния «колонка есть, ключа нет» не возникало вовсе (приём 0314:29-40).
--
--    FK БЕЗ КАСКАДА (RESTRICT), симметрично ключу на тему: занятую роль удалить нельзя, и это
--    последний рубеж, а не только проверка в сторе. INT signed — file_role.id объявлен выше как
--    INT AUTO_INCREMENT, а MySQL 8 отвергает внешний ключ между колонками разной знаковости
--    ошибкой 3780.
SET @lft_role := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'library_file_topic'
      AND COLUMN_NAME = 'role_id');
SET @ddl := IF(@lft_role = 0,
    'ALTER TABLE library_file_topic
        ADD COLUMN role_id INT NULL COMMENT ''роль файла ИМЕННО В ЭТОЙ теме; NULL = без роли; непусто только у тем kind=project'',
        ADD CONSTRAINT fk_library_file_topic_role FOREIGN KEY (role_id)
            REFERENCES file_role (id)',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- 8. Индекс под плечо «проект × роль» — секция страницы проекта спрашивает ровно эту пару.
SET @lft_role_idx := (SELECT COUNT(*) FROM information_schema.STATISTICS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'library_file_topic'
      AND INDEX_NAME = 'idx_library_file_topic_role');
SET @ddl := IF(@lft_role_idx = 0,
    'CREATE INDEX idx_library_file_topic_role ON library_file_topic (topic_id, role_id)',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- +migrate Down
--
-- Порядок обратный Up, гейт симметричный: каждый шаг снимается своим оператором, поэтому падение
-- посреди отката оставляет состояние, с которого повтор продолжает. Индекс роли уходит ВМЕСТЕ с
-- колонкой (DROP COLUMN снимает индексы, в которые колонка входит), поэтому отдельного DROP INDEX
-- для него нет — он упал бы о несуществующий индекс на втором прогоне.
SET @lft_role_back := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'library_file_topic'
      AND COLUMN_NAME = 'role_id');
SET @ddl := IF(@lft_role_back = 1,
    'ALTER TABLE library_file_topic
        DROP FOREIGN KEY fk_library_file_topic_role,
        DROP COLUMN role_id',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

DROP TABLE IF EXISTS file_role;

SET @ft_kind_idx_back := (SELECT COUNT(*) FROM information_schema.STATISTICS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'file_topic'
      AND INDEX_NAME = 'idx_file_topic_kind');
SET @ddl := IF(@ft_kind_idx_back > 0,
    'DROP INDEX idx_file_topic_kind ON file_topic',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @ft_arch_back := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'file_topic'
      AND COLUMN_NAME = 'archived_at');
SET @ddl := IF(@ft_arch_back = 1,
    'ALTER TABLE file_topic DROP COLUMN archived_at',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @ft_ends_back := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'file_topic'
      AND COLUMN_NAME = 'ends_at');
SET @ddl := IF(@ft_ends_back = 1,
    'ALTER TABLE file_topic DROP COLUMN ends_at',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @ft_starts_back := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'file_topic'
      AND COLUMN_NAME = 'starts_at');
SET @ddl := IF(@ft_starts_back = 1,
    'ALTER TABLE file_topic DROP COLUMN starts_at',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @ft_kind_back := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'file_topic'
      AND COLUMN_NAME = 'kind');
SET @ddl := IF(@ft_kind_back = 1,
    'ALTER TABLE file_topic DROP COLUMN kind',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;
