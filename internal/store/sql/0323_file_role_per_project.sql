-- +migrate Up

-- РОЛЬ ПРИНАДЛЕЖИТ ПРОЕКТУ. Словаря на всю библиотеку больше нет.
--
-- 0320 завела роль как ЗАКРЫТЫЙ ОБЩИЙ словарь: «исходники» были одной сущностью на всю
-- библиотеку, и держался этот выбор на сквозном вопросе «все исходники по всем съёмкам».
-- Заказ владельца прямо противоположен: «ROLE это проджект вайд онли фишка … они для каждого
-- проекта могут быть разными и не должны быть на уровне всех файлов». То есть «исходники»
-- съёмки и «исходники» лукбука — ДВЕ РАЗНЫЕ сущности, даже когда слово одно. Сквозной вопрос
-- при этом не исчезает, а меняет природу: он задаётся ПОИСКОМ ПО СЛОВУ (имя роли ищется вторым
-- EXISTS'ом с 0320) и молчит о проекте, где роль переименовали. Плата названа вслух и принята.
--
-- КОЛОНКА-ВЛАДЕЛЕЦ, А НЕ НОВАЯ ТАБЛИЦА. На file_role.id уже смотрят внешний ключ
-- library_file_topic.role_id, провод (FileRole, LibraryFileRole), слияния, счётчики и адреса
-- `?frole=N`. Новая таблица перевесила бы всё это ради нулевой выгоды: форма строки не меняется,
-- меняется её ПРИНАДЛЕЖНОСТЬ. NULL в project_topic_id разрешён ТОЛЬКО схемой — семантически
-- проект обязателен (каждый пишущий путь его требует, каждый читающий им ключуется), NULL
-- остаётся у легаси-строк переноса, недостижимых ни одним экраном.
--
-- ПОРЯДОК ШАГОВ — ЧАСТЬ КОРРЕКТНОСТИ, А НЕ ОФОРМЛЕНИЕ, НО ПЛАТА ЗА ПЕРЕСТАНОВКУ У КАЖДОЙ ПАРЫ
-- СВОЯ, И ЭТО ЗАМЕРЕНО МУТАНТАМИ, А НЕ ОБЪЯВЛЕНО:
--
--   * 2 ПОСЛЕ 4 — миграция ПАДАЕТ, а не проходит тихо: глобальный UNIQUE(name) съедает клоны через
--     INSERT IGNORE, строки связи остаются смотреть в глобальные роли, и составной ключ шага 7
--     отвечает 1452. Деплой встаёт ГРОМКО (на проде — остановка старта и откат образа);
--   * 6 ПЕРЕД 4 — а вот здесь миграция проходит ЗЕЛЁНОЙ и портит словарь МОЛЧА: у проекта в этот
--     момент ролей ещё нет, поэтому затравка ложится всем проектам подряд, а клон одноимённой роли
--     потом гасится парным UNIQUE и теряет свои sort_order и archived_at.
--
-- Обещать «любая перестановка тихо теряет данные» было бы неправдой, а неправда в шапке миграции
-- живёт годами и читается ровно в тот момент, когда её читают под давлением. Порядок:
--
--   1. колонка + внешний ключ на проект (одним оператором, половинчатого состояния нет);
--   2. СНЯТЬ глобальный UNIQUE(name) — ДО клонирования. Пока он жив, клон «исходники» для
--      второго проекта это дубль имени, и INSERT IGNORE шага 4 МОЛЧА его пропустит: миграция
--      зелёная, роли потеряны, ни одного отказа;
--   3. поставить парный UNIQUE(project_topic_id, name) и опорный UNIQUE(project_topic_id, id) —
--      тоже ДО клонирования: на парном держится идемпотентность шага 4;
--   4. клонировать использованные глобальные роли по проектам;
--   5. перевесить строки связи на клон СВОЕГО проекта;
--   6. засеять существующие проекты без единой роли;
--   7. заменить внешний ключ на СОСТАВНОЙ — строго ПОСЛЕ шага 5.
--
-- ПОЧЕМУ СОСТАВНОЙ КЛЮЧ ВООБЩЕ. С владельцем у роли появляется новое рассогласование: строка
-- library_file_topic несёт и topic_id, и role_id, и ничто не мешает строке проекта A получить
-- роль проекта B — молча, с правдоподобной выдачей. Ключ (topic_id, role_id) →
-- file_role (project_topic_id, id) делает это невыразимым. Семантика MATCH SIMPLE MySQL здесь
-- ровно нужная: строка с role_id IS NULL не проверяется вовсе (файл в проекте без роли — норма,
-- это приёмник), строка с ролью обязана указывать в роль СВОЕГО проекта. Заодно легаси-строки с
-- NULL-владельцем становятся недостижимы по построению: NULL не равен ни одному topic_id.
--
-- ADD FOREIGN KEY РЕТРОАКТИВЕН — ОН ПРОВЕРЯЕТ ВСЮ ИСТОРИЮ ТАБЛИЦЫ, ровно как запрещённый здесь
-- CHECK. Допустим он в этом файле ровно потому, что стоит СРАЗУ ПОСЛЕ шага 5, в том же файле:
-- множество нарушителей пусто ПО ПОСТРОЕНИЮ, и это утверждение доказано прогоном на слепке
-- живых данных, а не надеждой. Ретроактивных CHECK в файле нет ни одного.
--
-- ИДЕМПОТЕНТНОСТЬ (CLAUDE.md): MySQL 8 авто-коммитит DDL, поэтому падение посреди файла
-- оставляет схему полуприменённой БЕЗ строки в gorp_migrations, и следующая загрузка прогонит
-- файл с начала. Каждый ALTER, CREATE INDEX, DROP INDEX и внешний ключ — под своим гейтом
-- information_schema отдельным оператором (у CREATE INDEX в MySQL нет IF NOT EXISTS вовсе).
-- PREPARE / EXECUTE / DEALLOCATE — по одному оператору на строку: multiStatements=true в
-- контейнерных тестах маскирует поломку, которая на проде валит старт. DML шагов 4-6
-- идемпотентен по построению (INSERT IGNORE по парному UNIQUE и UPDATE, условие которого после
-- успеха не выполняется ни на одной строке), поэтому гейта не требует.
--
-- ОКНО «СТАРЫЙ БИНАРЬ × НОВАЯ СХЕМА» СУЩЕСТВУЕТ, И ЭТО ЗНАКОМЫЙ ПО 0306 УЗОР. Стор читает роли
-- через `SELECT fr.*` и `SELECT * FROM file_role`, а storeutil сканирует БЕЗ Unsafe — значит для
-- КОДА ДО 0323 лишняя колонка project_topic_id это ошибка сканирования, а не безобидное поле. На
-- платформе старый инстанс продолжает отвечать, пока новый мигрирует, поэтому на это окно экраны
-- словаря ролей и простановка роли отвечают 500. Громко, данные целы, но откат образа на до-0323
-- НЕ лечит: он держит их сломанными до roll-forward. Соответственно, откатывать здесь надо вперёд,
-- а не назад.
--
-- РАЗРУШИТЕЛЬНЫХ ОПЕРАЦИЙ НЕТ. Глобальная роль, заведённая, но нигде не использованная, остаётся
-- лежать мёртвой строкой с NULL-владельцем: она недостижима любым чтением (все читатели
-- ключуются проектом), не мешает никому (из парного UNIQUE выпадает, так как MySQL считает NULL
-- различными) и удаляется — или нет — отдельной волной по решению человека. Тот же выбор, что
-- в 0320.

-- 1. Владелец роли. ADD COLUMN и ADD CONSTRAINT ОДНИМ оператором, чтобы половинчатого состояния
--    «колонка есть, ключа нет» не возникало вовсе (приём 0314:29-40).
--
--    ON DELETE CASCADE, и это осознанно: пустой проект (файлов нет, словарь заведён) обязан
--    удаляться, не спотыкаясь о собственные роли. Последний рубеж при этом не слабеет — RESTRICT
--    от library_file_topic к file_role останавливает каскад, если на роль ещё смотрит хоть одна
--    строка связи, а такое возможно только при уже сломанном инварианте, и громкий отказ здесь
--    лучше тихого успеха.
SET @fr_proj := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'file_role'
      AND COLUMN_NAME = 'project_topic_id');
SET @ddl := IF(@fr_proj = 0,
    'ALTER TABLE file_role
        ADD COLUMN project_topic_id INT NULL
            COMMENT ''проект-владелец роли; NULL только у легаси-строк переноса 0323, недостижимых ни одним экраном'',
        ADD CONSTRAINT fk_file_role_project FOREIGN KEY (project_topic_id)
            REFERENCES file_topic (id) ON DELETE CASCADE',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- 2. Глобальный UNIQUE(name) УМИРАЕТ, И ИМЕННО ЗДЕСЬ. «Исходники» законны в двадцати проектах,
--    поэтому уникальность имени на всю таблицу перестала быть правдой. Снимается ДО клонирования
--    (шаг 4): пока индекс жив, второй клон того же имени — дубль, и INSERT IGNORE пропустит его
--    МОЛЧА. Это ловушка миграции номер один, и стоит она ровно в перестановке двух строк.
SET @fr_uniq_name := (SELECT COUNT(*) FROM information_schema.STATISTICS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'file_role'
      AND INDEX_NAME = 'uniq_file_role_name');
SET @ddl := IF(@fr_uniq_name > 0,
    'DROP INDEX uniq_file_role_name ON file_role',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- 3а. Парный UNIQUE: «исходники» законны в двадцати проектах, но не дважды в одном. Он ОБЯЗАН
--     стоять до шага 4 — на нём держится идемпотентность клонирования. Легаси-строки с
--     NULL-владельцем из пары выпадают (MySQL считает NULL различными), и это правильно: они
--     мёртвые.
SET @fr_uniq_pair := (SELECT COUNT(*) FROM information_schema.STATISTICS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'file_role'
      AND INDEX_NAME = 'uniq_file_role_project_name');
SET @ddl := IF(@fr_uniq_pair = 0,
    'CREATE UNIQUE INDEX uniq_file_role_project_name ON file_role (project_topic_id, name)',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- 3б. Опора составного ключа шага 7. UNIQUE, а не обычный индекс, и это не украшение: ссылка на
--     НЕуникальный ключ — расширение InnoDB, отклонение от стандарта, которое MySQL уже пометил
--     устаревшим. Уникальность здесь даровая — id первичный ключ, поэтому пара (владелец, id)
--     уникальна по построению.
SET @fr_pair_idx := (SELECT COUNT(*) FROM information_schema.STATISTICS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'file_role'
      AND INDEX_NAME = 'uniq_file_role_project_id');
SET @ddl := IF(@fr_pair_idx = 0,
    'CREATE UNIQUE INDEX uniq_file_role_project_id ON file_role (project_topic_id, id)',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- 4. КЛОНИРОВАНИЕ ИСПОЛЬЗОВАННЫХ РОЛЕЙ ПО ПРОЕКТАМ. Глобальная роль, проставленная в трёх
--    проектах, превращается в ТРИ одноимённые строки с разными владельцами; имя, порядок и
--    архивность переезжают как есть, чтобы разбивка страницы проекта осталась той же на вид.
--
--    Идемпотентно через INSERT IGNORE по парному UNIQUE шага 3а: повтор находит клон на месте и
--    не делает ничего. Источник — РАЗЛИЧНЫЕ пары (тема, роль) со строк связи: клонируется только
--    то, что реально проставлено, а не весь словарь в каждый проект.
INSERT IGNORE INTO file_role (project_topic_id, name, sort_order, archived_at)
SELECT used.topic_id, fr.name, fr.sort_order, fr.archived_at
FROM (SELECT DISTINCT lft.topic_id, lft.role_id
      FROM library_file_topic lft
      WHERE lft.role_id IS NOT NULL) used
JOIN file_role fr ON fr.id = used.role_id
WHERE fr.project_topic_id IS NULL;

-- 5. ПЕРЕВЕШИВАНИЕ СТРОК СВЯЗИ НА КЛОН СВОЕГО ПРОЕКТА. После этого ни одна строка связи не
--    смотрит в глобальную роль — утверждение проверяется тестом миграции, а не глазами, и оно же
--    делает ретроактивную проверку шага 7 законной.
--
--    Идемпотентно по построению: условие «роль ещё глобальна» после успеха не выполняется ни на
--    одной строке. Одноимённый клон единственен — за это отвечает парный UNIQUE.
UPDATE library_file_topic lft
JOIN file_role legacy ON legacy.id = lft.role_id AND legacy.project_topic_id IS NULL
JOIN file_role mine ON mine.project_topic_id = lft.topic_id AND mine.name = legacy.name
SET lft.role_id = mine.id;

-- 6. ЗАТРАВКА СУЩЕСТВУЮЩИХ ПРОЕКТОВ. Довод записан ещё в 0312:99-106 и применим дословно:
--    раздел, открывшийся пустым, заставляет придумывать структуру на месте — а придумывать её
--    никто не будет. Тот же набор, что сеет код при повышении темы до проекта (roles.go,
--    defaultProjectRoles); снимок списка в тексте миграции законен — миграции и есть снимки.
--
--    Тот же zero-guard, что в коде: сеется только проекту, у которого НЕТ НИ ОДНОЙ роли. Проект,
--    уже получивший клоны на шаге 4, затравку не получает — иначе поверх выстраданного набора
--    легли бы четыре чужих слова.
--
--    АРХИВНЫЕ ПРОЕКТЫ ЗАТРАВКУ НЕ ПОЛУЧАЮТ, И ЭТО РАСХОЖДЕНИЕ С КОДОМ — НАМЕРЕННОЕ. Архив это
--    «съёмка закончена и убрана с глаз»; словарь, заведённый ей задним числом, всплыл бы при
--    разархивации чужой разбивкой, которой никто не ставил. Код (roles.go, seedProjectRoles) на
--    архивность не смотрит по обратному доводу: там повышение темы до проекта — ЖИВОЕ действие
--    человека прямо сейчас, и отказать ему в словаре значило бы открыть ему пустой раздел.
INSERT IGNORE INTO file_role (project_topic_id, name, sort_order)
SELECT ft.id, seed.name, seed.sort_order
FROM file_topic ft
CROSS JOIN (
    SELECT 'исходники' AS name, 10 AS sort_order
    UNION ALL SELECT 'обработанные', 20
    UNION ALL SELECT 'идея', 30
    UNION ALL SELECT 'планирование', 40) seed
WHERE ft.kind = 'project'
  AND ft.archived_at IS NULL
  AND NOT EXISTS (SELECT 1 FROM file_role fr WHERE fr.project_topic_id = ft.id);

-- 7а. Снять одноколоночный ключ 0320. Служебный индекс, который MySQL завёл под него на
--     (role_id), остаётся жить намеренно: по нему ходит фильтр «роль без проекта» из ListFiles.
SET @lft_fk_old := (SELECT COUNT(*) FROM information_schema.TABLE_CONSTRAINTS
    WHERE CONSTRAINT_SCHEMA = DATABASE() AND TABLE_NAME = 'library_file_topic'
      AND CONSTRAINT_NAME = 'fk_library_file_topic_role' AND CONSTRAINT_TYPE = 'FOREIGN KEY');
SET @ddl := IF(@lft_fk_old > 0,
    'ALTER TABLE library_file_topic DROP FOREIGN KEY fk_library_file_topic_role',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- 7б. Поставить СОСТАВНОЙ. Опорный индекс со стороны ссылающейся таблицы —
--     idx_library_file_topic_role (topic_id, role_id) — стоит с 0320, поэтому служебного индекса
--     MySQL здесь не заводит. Строго ПОСЛЕ шага 5: до перевешивания валидация ключа упала бы о
--     строки, чья роль ещё глобальна, и старт прода встал бы.
SET @lft_fk_new := (SELECT COUNT(*) FROM information_schema.TABLE_CONSTRAINTS
    WHERE CONSTRAINT_SCHEMA = DATABASE() AND TABLE_NAME = 'library_file_topic'
      AND CONSTRAINT_NAME = 'fk_library_file_topic_role_project' AND CONSTRAINT_TYPE = 'FOREIGN KEY');
SET @ddl := IF(@lft_fk_new = 0,
    'ALTER TABLE library_file_topic
        ADD CONSTRAINT fk_library_file_topic_role_project FOREIGN KEY (topic_id, role_id)
            REFERENCES file_role (project_topic_id, id)',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- +migrate Down
--
-- Порядок обратный Up, гейт симметричный: падение посреди отката оставляет состояние, с которого
-- повтор продолжает.
--
-- ДВА ЯВНЫХ DROP INDEX ПЕРЕД СНЯТИЕМ КОЛОНКИ — НЕ ЛИШНИЕ, И ЭТО ГЛАВНАЯ ЛОВУШКА ОТКАТА. MySQL
-- удаляет индекс вместе с колонкой ТОЛЬКО когда снимаются ВСЕ его колонки; из многоколоночного
-- он просто ВЫЧЁРКИВАЕТ снятую. То есть DROP COLUMN project_topic_id превратил бы
-- uniq_file_role_project_name (project_topic_id, name) в UNIQUE(name) — глобальную уникальность,
-- которую откат как раз восстанавливать не должен и которая упала бы 1062 о клоны шага 4, — а
-- uniq_file_role_project_id (project_topic_id, id) в UNIQUE(id), дубликат первичного ключа.
--
-- КЛОНЫ И ПЕРЕВЕШЕННЫЕ СТРОКИ ОТКАТ НЕ РАЗБИРАЕТ, И ЭТО ОСОЗНАННО. Разобрать значило бы гадать,
-- какая из N одноимённых строк была исходной, а исходной могло уже не быть вовсе. Откат снимает
-- СХЕМУ, оставляя данные валидными для прежнего кода: каждая строка связи по-прежнему указывает в
-- существующую роль, одноколоночный ключ это подтверждает. Повторный Up после отката заведёт
-- клоны заново и перевесит на них — ни одна проставленная роль при этом не теряется (проверено
-- прогоном Down → Down → Up), а прежние клоны остаются лежать такими же мёртвыми строками, как
-- легаси-роли после первого Up.
--
-- ГЛОБАЛЬНЫЙ UNIQUE(name) ВОССТАНАВЛИВАЕТСЯ ПОД ГЕЙТОМ ДУБЛЕЙ. На базе, где перенос уже случился,
-- одноимённые роли разных проектов — норма, и восстановить на них глобальную уникальность
-- НЕВОЗМОЖНО в принципе: CREATE UNIQUE INDEX ответил бы 1062 и заклинил бы откат навсегда.
-- Поэтому индекс возвращается ровно тогда, когда возвращаться ему есть куда, а иначе откат честно
-- оставляет схему без него. Down — путь разработчика (на проде и бете миграции идут только
-- вверх), и заклинивший откат стоил бы дороже, чем недовосстановленный индекс.

SET @lft_fk_new_back := (SELECT COUNT(*) FROM information_schema.TABLE_CONSTRAINTS
    WHERE CONSTRAINT_SCHEMA = DATABASE() AND TABLE_NAME = 'library_file_topic'
      AND CONSTRAINT_NAME = 'fk_library_file_topic_role_project' AND CONSTRAINT_TYPE = 'FOREIGN KEY');
SET @ddl := IF(@lft_fk_new_back > 0,
    'ALTER TABLE library_file_topic DROP FOREIGN KEY fk_library_file_topic_role_project',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @lft_fk_old_back := (SELECT COUNT(*) FROM information_schema.TABLE_CONSTRAINTS
    WHERE CONSTRAINT_SCHEMA = DATABASE() AND TABLE_NAME = 'library_file_topic'
      AND CONSTRAINT_NAME = 'fk_library_file_topic_role' AND CONSTRAINT_TYPE = 'FOREIGN KEY');
SET @ddl := IF(@lft_fk_old_back = 0,
    'ALTER TABLE library_file_topic
        ADD CONSTRAINT fk_library_file_topic_role FOREIGN KEY (role_id)
            REFERENCES file_role (id)',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @fr_uniq_pair_back := (SELECT COUNT(*) FROM information_schema.STATISTICS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'file_role'
      AND INDEX_NAME = 'uniq_file_role_project_name');
SET @ddl := IF(@fr_uniq_pair_back > 0,
    'DROP INDEX uniq_file_role_project_name ON file_role',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- Опорный индекс составного ключа снимается ОДНИМ ОПЕРАТОРОМ С КОЛОНКОЙ И ВНЕШНИМ КЛЮЧОМ, и
-- порядок здесь вынужденный, а не эстетический (замерено, MySQL 8.0):
--
--   * пока стоит fk_file_role_project, из двух индексов с project_topic_id слева можно снять
--     ЛЮБОЙ ОДИН — второй продолжает обслуживать ключ; попытка снять последний отвечает 1553
--     «needed in a foreign key constraint». Поэтому парный UNIQUE уходит выше отдельно, а этот —
--     только вместе с ключом;
--   * снять его ПОСЛЕ DROP COLUMN нельзя: колонка уносит с собой лишь те индексы, где она
--     единственная, а из многоколоночного просто вычёркивается — uniq_file_role_project_id
--     превратился бы в UNIQUE(id), дубликат первичного ключа.
--
-- Текст оператора собирается по факту: если предыдущий откат успел упасть между шагами, индекса
-- может уже не быть, и упоминание его в ALTER заклинило бы откат навсегда.
SET @fr_pair_idx_back := (SELECT COUNT(*) FROM information_schema.STATISTICS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'file_role'
      AND INDEX_NAME = 'uniq_file_role_project_id');
SET @fr_proj_back := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'file_role'
      AND COLUMN_NAME = 'project_topic_id');
SET @ddl := IF(@fr_proj_back = 1,
    CONCAT('ALTER TABLE file_role DROP FOREIGN KEY fk_file_role_project',
        IF(@fr_pair_idx_back > 0, ', DROP INDEX uniq_file_role_project_id', ''),
        ', DROP COLUMN project_topic_id'),
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @fr_uniq_name_back := (SELECT COUNT(*) FROM information_schema.STATISTICS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'file_role'
      AND INDEX_NAME = 'uniq_file_role_name');
SET @fr_dupes := (SELECT COUNT(*) FROM (
    SELECT name FROM file_role GROUP BY name HAVING COUNT(*) > 1) d);
SET @ddl := IF(@fr_uniq_name_back = 0 AND @fr_dupes = 0,
    'CREATE UNIQUE INDEX uniq_file_role_name ON file_role (name)',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;
