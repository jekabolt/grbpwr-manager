-- +migrate Up

-- MARKDOWN-ЗАМЕТКА: «КТО ПРАВИЛ ПОСЛЕДНИМ» И ВЫДЕРЖКА ТЕКСТА ДЛЯ ПЛИТКИ.
--
-- ЗАМЕТКА — ОБЫЧНЫЙ `library_file`, И ЭТО ГЛАВНОЕ РЕШЕНИЕ ФАЗЫ. У неё `content_type
-- text/markdown`, ключ в S3 и строка здесь — значит темы, владельцы, доступ, обсуждение и задачи
-- достаются ей бесплатно, ровно теми же запросами. Отдельная сущность «заметка» потребовала бы
-- второго экземпляра каждой из этих связей, и первый же день, когда они разъедутся, даст заметку,
-- которую не видно в библиотеке. Поэтому новых таблиц тут нет вовсе — три колонки на файле.
--
-- ПОЧЕМУ «КТО ПРАВИЛ» ОТДЕЛЬНО ОТ «КТО ЗАГРУЗИЛ». `uploaded_by` — кто ЗАВЁЛ файл, факт на всю
-- жизнь строки. Заметку правят потом и другие люди, и шапка обязана называть последнего из них,
-- иначе «правил pasha» будет враньём про текст, который написал kirill. Пустое значение читается
-- как «редактором ни разу не правили» — тогда шапка честно показывает «загрузил {uploaded_by}»,
-- а не выдумывает автора правки.
--
-- СТРОКА, А НЕ ССЫЛКА (симметрия с uploaded_by, 0312): «правил kirill» обязано пережить удаление
-- аккаунта. Живой ссылки здесь нет намеренно — аватар редактора нигде не рисуется, а колонка,
-- которую никто не читает, стоила бы ещё одного FK на admins.
--
-- ВЫДЕРЖКА ЛЕЖИТ В СТРОКЕ, А НЕ СЧИТАЕТСЯ НА ЛЕТУ, потому что текст живёт в S3: собрать превью
-- плитки для страницы из 60 файлов означало бы 60 чтений объектов на КАЖДУЮ отрисовку сетки.
-- Денормализация здесь — не оптимизация, а единственный способ показать превью вообще.
-- Заполняется при создании и при каждом сохранении заметки — там же, где считается новый sha256.
--
-- ПУСТАЯ ВЫДЕРЖКА ЛЕГАЛЬНА И ПОСТОЯННА, а не «переходное состояние»: .md, ЗАЛИТЫЙ файлом, текста
-- в строку не приносит — на пути стриминга загрузки чтения содержимого нет и не будет (стрим
-- считает sha256 и размер, разбирать по дороге markdown он не обязан). Такой файл показывает на
-- плитке плашку вместо выдержки и дозаполняется первым сохранением через редактор. Бэкфилла нет
-- по той же причине: сервер не читает S3 в миграции.
--
-- VARCHAR(500), А НЕ TEXT: это ровно превью на плитку — несколько первых строк. TEXT соблазнял бы
-- сложить туда весь текст заметки и завести вторую копию содержимого рядом с S3-объектом, которая
-- немедленно начала бы расходиться с ним.
--
-- СТАРЫЙ БИНАРЬ ЭТО ПЕРЕЖИВАЕТ (DO при провале деплоя откатывает бинарь, миграции остаются):
-- стор собран через d.Unsafe(), лишние колонки в SELECT * его не ломают, а его INSERT'ы их не
-- называют — строковые падают в DEFAULT '', nullable в NULL. Разрушительных операторов нет.
--
-- ИДЕМПОТЕНТНОСТЬ (CLAUDE.md): MySQL 8 автокоммитит DDL, поэтому падение в середине файла
-- оставляет схему полуприменённой БЕЗ строки в gorp_migrations, и следующая загрузка перезапускает
-- файл с начала. Каждый ALTER — под своим гейтом information_schema и отдельным независимым
-- оператором, PREPARE / EXECUTE / DEALLOCATE по одному оператору на строку (multiStatements=true
-- в контейнерных тестах иначе маскирует поломку, которая на проде валит старт). Ретроактивных
-- CHECK нет ни одного: они проверяют ВСЮ историю и останавливают старт прода.

-- 1. Кто правил последним.
SET @lf_cub := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'library_file'
      AND COLUMN_NAME = 'content_updated_by');
SET @ddl := IF(@lf_cub = 0,
    'ALTER TABLE library_file
        ADD COLUMN content_updated_by VARCHAR(255) NOT NULL DEFAULT ''''
            COMMENT ''username последнего правившего содержимое через редактор заметок; пусто = редактором не правили ни разу (шапка показывает uploaded_by)''',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- 2. Когда правили последний раз. NULL = ни разу; это не первая TIMESTAMP-колонка таблицы,
--    поэтому неявного DEFAULT CURRENT_TIMESTAMP она не получает, но DEFAULT NULL написан явно.
SET @lf_cua := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'library_file'
      AND COLUMN_NAME = 'content_updated_at');
SET @ddl := IF(@lf_cua = 0,
    'ALTER TABLE library_file
        ADD COLUMN content_updated_at TIMESTAMP NULL DEFAULT NULL
            COMMENT ''когда содержимое правили через редактор заметок; NULL = ни разу''',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- 3. Выдержка текста для плитки.
SET @lf_exc := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'library_file'
      AND COLUMN_NAME = 'content_excerpt');
SET @ddl := IF(@lf_exc = 0,
    'ALTER TABLE library_file
        ADD COLUMN content_excerpt VARCHAR(500) NOT NULL DEFAULT ''''
            COMMENT ''первые строки текста заметки для превью плитки; пусто = превью нет (.md, залитый файлом, дозаполнится первым сохранением через редактор)''',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- +migrate Down
--
-- Порядок обратный Up, гейт симметричный: каждая колонка снимается своим оператором, поэтому
-- падение посреди отката оставляет состояние, с которого повтор продолжает.
SET @lf_exc_back := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'library_file'
      AND COLUMN_NAME = 'content_excerpt');
SET @ddl := IF(@lf_exc_back = 1,
    'ALTER TABLE library_file DROP COLUMN content_excerpt',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @lf_cua_back := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'library_file'
      AND COLUMN_NAME = 'content_updated_at');
SET @ddl := IF(@lf_cua_back = 1,
    'ALTER TABLE library_file DROP COLUMN content_updated_at',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @lf_cub_back := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'library_file'
      AND COLUMN_NAME = 'content_updated_by');
SET @ddl := IF(@lf_cub_back = 1,
    'ALTER TABLE library_file DROP COLUMN content_updated_by',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;
