-- +migrate Up

-- ПРОЕКТ ↔ ЗАДАЧА: «ЧТО ЕЩЁ ОСТАЛОСЬ СДЕЛАТЬ ПО ЭТОЙ СЪЁМКЕ».
--
-- 0320 научила тему быть ПРОЕКТОМ и разложила файлы внутри него по ролям, 0321 связала проект со
-- стилями. Работа, ради которой проект заведён, при этом жила отдельно: задача «отретушировать
-- отобранное» принадлежит конкретной съёмке, но сказать об этом системе было нечем — человек
-- писал название съёмки в заголовок карточки и надеялся, что второй напишет так же.
--
-- КОЛОНКА, А НЕ ВТОРАЯ ТАБЛИЦА СВЯЗИ, И ЭТО ГЛАВНОЕ РЕШЕНИЕ ФАЙЛА. У `task` уже СЕМЬ «глубоких
-- ссылок» (tech_card_id, product_id, order_uuid, archive_id, fitting_id, production_run_id,
-- sample_id — 0090, 0092, 0099, 0108), все NULLable, все ON DELETE SET NULL, все с индексом. На
-- экране это один пикер с одним чипом, и проект становится ВОСЬМЫМ его типом.
--
-- Довод не в экономии таблицы, а в существе: задача — ЕДИНИЦА РАБОТЫ, и работа происходит в ОДНОМ
-- контексте. «Отретушировать отобранное» принадлежит одной съёмке; принадлежать двум одновременно
-- она не может, не перестав быть одной задачей. Сравни с 0321, где связь многие-ко-многим не
-- осторожность, а факт: съёмка покрывает капсулу из восьми вещей, и та же вещь попадает и в
-- съёмку, и в лукбук. У задачи такой множественности нет ни с одной стороны.
--
-- Вторая таблица к тому же РАЗОШЛАСЬ БЫ С СЕМЬЮ СОСЕДЯМИ в том же экране: обратный вопрос «какие
-- задачи у проекта» из колонки читается фильтром уже существующего ListTasks, а через таблицу
-- потребовал бы второго пути к тем же данным — с собственными правами, которым нечем помешать
-- однажды разойтись с правами задач.
--
-- ON DELETE SET NULL, КАК У ВСЕХ СЕМИ. Удаление темы не имеет права упереться в задачу: тема
-- удаляется только пустой (DeleteTopic отказывает, пока её несут файлы), и RESTRICT здесь сделал
-- бы ПУСТОЙ проект неудаляемым из-за карточки, которой на экране тем не видно. Задача при этом
-- ОСТАЁТСЯ — она про работу, а не про съёмку, — и теряет только ссылку. Сколько задач её потеряло,
-- удаление ВОЗВРАЩАЕТ: молчание сделало бы «убрал законченную съёмку с глаз» и «у двенадцати
-- карточек пропал контекст» двумя событиями, между которыми месяц.
--
-- ССЫЛАТЬСЯ МОЖНО ТОЛЬКО НА ТЕМУ kind='project', И ПРОВЕРЯЕТСЯ ЭТО В КОДЕ, А НЕ CHECK'ом.
-- Ретроактивный CHECK проверяет ВСЮ историю таблицы и останавливает старт прода (2026-08-08,
-- tech_card_marker). Здесь он к тому же невыразим без денормализации `kind` в `task`, а
-- денормализация давала бы устаревающие строки при смене типа темы — ровно тот довод, по которому
-- 0320 не потащила `kind` в library_file_topic и 0321 не потащила его в file_topic_tech_card.
-- Проверка стоит в единственном месте, где ссылка записывается (task.Store.AddTask/UpdateTask),
-- внутри пишущей транзакции: она SERIALIZABLE, поэтому чтение `kind` запирает строку темы, и
-- параллельное понижение проекта не проскакивает между проверкой и записью. Отвечает она фразой
-- (InvalidArgument), а не 1452 с именем ключа.
--
-- INT SIGNED — file_topic.id объявлен в 0312 как `INT PRIMARY KEY AUTO_INCREMENT`, а MySQL 8
-- отвергает внешний ключ между колонками разной знаковости ошибкой 3780.
--
-- ИДЕМПОТЕНТНОСТЬ (CLAUDE.md): MySQL 8 авто-коммитит DDL, поэтому падение в середине файла
-- оставляет схему полуприменённой БЕЗ строки в gorp_migrations, и следующая загрузка перезапускает
-- файл с начала. Здесь ТРИ независимых оператора, каждый под своим гейтом information_schema:
-- колонка (COLUMNS), индекс (STATISTICS — у CREATE INDEX в MySQL нет IF NOT EXISTS вовсе) и
-- внешний ключ (TABLE_CONSTRAINTS). Любое половинчатое состояние доводится следующим прогоном с
-- начала файла. PREPARE / EXECUTE / DEALLOCATE — ПО ОДНОМУ ОПЕРАТОРУ НА СТРОКУ: у прода и беты в
-- DSN нет multiStatements, и трио, написанное в одну строку, уезжает туда одним запросом и валит
-- старт ошибкой 1064 (0185, 2026-07-18). Ретроактивных CHECK нет ни одного, бэкфилла нет:
-- существующие карточки получают NULL, то есть «проекта не указано», и это правда о них.
--
-- ПОРЯДОК ШАГОВ 2 И 3: ИНДЕКС РАНЬШЕ КЛЮЧА — И ЭТО НЕ ТО, ЧЕМ КАЗАЛОСЬ. Ожидание было такое: MySQL
-- требует индекс на ссылающейся колонке и, не найдя его, заводит СВОЙ с именем констрейнта, а
-- значит при обратном порядке гейт шага 2 не нашёл бы `idx_task_project` и добавил бы ВТОРОЙ индекс
-- по той же колонке. ЗАМЕРЕНО НА MySQL 8.0 — НЕВЕРНО: создание равнозначного пользовательского
-- индекса УБИРАЕТ служебный, и оба порядка сходятся ровно к одному индексу (проверено на
-- одноразовой базе: после ADD CONSTRAINT виден `fk_…`, после CREATE INDEX остаётся только `idx_…`).
-- Мутант «переставить шаги местами» поэтому НЕ КРАСНЕЕТ ни одного теста, и это записано здесь
-- честно, а не выдано за довод.
--
-- Порядок оставлен прежним по единственной причине, которая переживает замер: он не опирается на
-- эту замену вовсе — ключ принимает уже СТОЯЩИЙ индекс с известным именем, и постусловие каждого
-- шага читается независимо от того, как MySQL обходится со своими служебными индексами. Ровно так
-- же обошлась 0092 (fitting): ADD INDEX и ADD CONSTRAINT одним оператором в этом порядке. У 0108
-- (sample) явного индекса нет вовсе, и там до сих пор живёт служебный `fk_task_sample`.

-- 1. Сама ссылка.
SET @task_project := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'task'
      AND COLUMN_NAME = 'project_topic_id');
SET @ddl := IF(@task_project = 0,
    'ALTER TABLE task
        ADD COLUMN project_topic_id INT NULL
            COMMENT ''FK file_topic(id); восьмая глубокая ссылка карточки — проект библиотеки файлов. Осмысленна только при kind=project (проверяется в сторе, а не через CHECK); NULL = проекта не указано''',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- 2. Индекс под ОБРАТНЫЙ вопрос «какие задачи у этого проекта» — тот самый фильтр ListTasks, ради
--    которого фаза и заведена. Без него он читался бы полным сканом доски.
SET @task_project_idx := (SELECT COUNT(*) FROM information_schema.STATISTICS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'task'
      AND INDEX_NAME = 'idx_task_project');
SET @ddl := IF(@task_project_idx = 0,
    'CREATE INDEX idx_task_project ON task (project_topic_id)',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- 3. Внешний ключ. Проверять ему на существующих данных нечего: колонка только что заведена и вся
--    в NULL, поэтому ADD CONSTRAINT здесь не является утверждением об истории таблицы.
SET @task_project_fk := (SELECT COUNT(*) FROM information_schema.TABLE_CONSTRAINTS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'task'
      AND CONSTRAINT_NAME = 'fk_task_project' AND CONSTRAINT_TYPE = 'FOREIGN KEY');
SET @ddl := IF(@task_project_fk = 0,
    'ALTER TABLE task
        ADD CONSTRAINT fk_task_project FOREIGN KEY (project_topic_id)
            REFERENCES file_topic (id) ON DELETE SET NULL',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- +migrate Down
--
-- Порядок обратный Up, гейт симметричный: каждый шаг снимается своим оператором, поэтому падение
-- посреди отката оставляет состояние, с которого повтор продолжает. Отдельного DROP INDEX нет:
-- DROP COLUMN снимает индексы, в которые колонка входит, и на втором прогоне отдельный DROP INDEX
-- упал бы о несуществующий индекс (тот же довод, что в Down 0320).
SET @task_project_fk_back := (SELECT COUNT(*) FROM information_schema.TABLE_CONSTRAINTS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'task'
      AND CONSTRAINT_NAME = 'fk_task_project' AND CONSTRAINT_TYPE = 'FOREIGN KEY');
SET @ddl := IF(@task_project_fk_back > 0,
    'ALTER TABLE task DROP FOREIGN KEY fk_task_project',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @task_project_back := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'task'
      AND COLUMN_NAME = 'project_topic_id');
SET @ddl := IF(@task_project_back = 1,
    'ALTER TABLE task DROP COLUMN project_topic_id',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;
