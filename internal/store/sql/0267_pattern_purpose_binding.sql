-- +migrate Up

-- ВЫКРОЙКА И АЛИАС ДЕТАЛИ ПРИВЯЗЫВАЮТСЯ К НАЗНАЧЕНИЮ (0265), А НЕ К СТРОКЕ BOM.
--
-- WHY THE BINDING MOVES UP AN AXIS. На уровне карточки всё, что рисует градалка, — это КЛАСС:
-- «это лекало основной ткани», «эта деталь кроится из подкладки». Конкретная строка BOM важна
-- ровно там, где важен АРТИКУЛ: раскладка (ширина полотна и кромка берутся с артикула, который
-- пинит колорвей — 0259/0264) и производственная партия. Ни то, ни другое здесь не хранится:
-- tech_card_size_pattern.bom_line_key (0260) и tech_card_piece_dxf_block.bom_line_key (0262)
-- называли строку только потому, что назначения тогда ещё не существовало. Оно появилось в 0265 —
-- и «один DXF = одна ткань» стало «один DXF = одно назначение», которое может владеть несколькими
-- строками (основная ткань в двух артикулах — это две строки одного назначения, лекало одно).
--
-- ПЕРЕХОД ЧИСТО АДДИТИВНЫЙ, И ЭТО НЕ ОСТОРОЖНОСТЬ, А ЕДИНСТВЕННЫЙ ВОЗМОЖНЫЙ ВАРИАНТ. У всех
-- существующих карточек назначения НЕТ: 0265 сознательно ничего не бэкфилила, потому что
-- section='fabric' — это ровно то место, где прячутся карманка, контраст и сетка, и любая догадка
-- назвала бы все три «основным материалом» уверенно и неправильно. Значит лист, привязанный к
-- строке L, ПЕРЕЕХАТЬ НЕ МОЖЕТ: у L нет назначения, на которое его можно переписать. Поэтому оба
-- поля живут рядом, а правило разрешения одно и то же везде (entity.ResolveFabricScope):
--
--     сначала назначение, иначе — старая строка.
--
-- Неразобранная карточка продолжает работать без единого действия оператора и мигрирует сама в тот
-- момент, когда её кто-нибудь разложит по назначениям. Ничего не форсируется.
--
-- САМОЕ ОСТРОЕ МЕСТО — УНИКАЛЬНОСТЬ АЛИАСОВ. Сегодня это (карточка, строка, имя блока). Просто
-- подставить сюда назначение НЕЛЬЗЯ: если назначение владеет несколькими строками и в этих строках
-- лежали одноимённые блоки, получается ДУБЛИКАТ, а стор отвергает дубликат, роняя сохранение ВСЕЙ
-- карточки. Поэтому уникальность переезжает на scope_key = COALESCE(fabric_purpose, bom_line_key) —
-- ту же самую формулу разрешения, но материализованную колонкой, чтобы индекс и Go считали её
-- одинаково по построению, а не по договорённости.
--
-- НА МОМЕНТ МИГРАЦИИ КОЛЛИЗИЯ НЕВОЗМОЖНА В ПРИНЦИПЕ: fabric_purpose у всех существующих строк NULL,
-- значит scope_key каждой строки РАВЕН её старому bom_line_key, и новый уникальный индекс
-- воспроизводит сегодняшний ровно, строка в строку. Коллизии становятся возможны ПОЗЖЕ — когда
-- карточку разложат по назначениям, — а это живая правка в интерфейсе, и предупреждать о ней должен
-- клиент ДО сохранения, а не сервер пятисоткой после.
--
-- ПОРЯДОК ШАГОВ ЗНАЧИМ. Новый уникальный индекс создаётся ДО того, как дропается старый: у обоих
-- tech_card_id стоит слева, так что InnoDB перевешивает на новый требование FK fk_tcpdb_card, и
-- DROP не падает с 1553 «Cannot drop index needed in a foreign key constraint».
--
-- ИДЕМПОТЕНТНОСТЬ (CLAUDE.md): MySQL 8 автокоммитит DDL, поэтому падение в середине файла оставляет
-- схему полуприменённой без строки в gorp_migrations, и следующая загрузка перезапускает файл с
-- начала. Каждый шаг — отдельный ALTER под своей проверкой в information_schema, один
-- PREPARE/EXECUTE/DEALLOCATE на строку (0259/0263/0265). Шаги нарочно НЕ слиты в один ALTER: так
-- повторный прогон доезжает ровно с того места, где упал предыдущий.
--
-- REGEXP + STRCMP — ловушка из 0265 дословно: под utf8mb3_general_ci REGEXP регистронезависим, так
-- что 'MAIN' прошёл бы и не попал бы потом ни в одну группу, а `REGEXP BINARY` под utf8mb4_0900_ai_ci
-- (так подключаются контейнерные тесты) отвечает 3995. Побайтовое сравнение с LOWER портируемо между
-- utf8mb3 прода и utf8mb4 тестов.

-- 1. Выкройка: назначение ткани, из которой кроится лист.
SET @col_exists := (
    SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'tech_card_size_pattern'
      AND COLUMN_NAME = 'fabric_purpose'
);
SET @ddl := IF(@col_exists = 0,
    'ALTER TABLE tech_card_size_pattern ADD COLUMN fabric_purpose VARCHAR(24) NULL COMMENT ''назначение (0265) this sheet is cut from — NULL falls back to bom_line_key'' AFTER bom_line_key, ADD CONSTRAINT chk_tcsp_fabric_purpose CHECK (fabric_purpose IS NULL OR (fabric_purpose REGEXP ''^(main|lining|pocketing|interfacing|insulation|contrast|mesh|other)$'' AND STRCMP(CAST(fabric_purpose AS BINARY), CAST(LOWER(fabric_purpose) AS BINARY)) = 0))',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- 2. Алиас блока: то же назначение. bom_line_key ОСТАЁТСЯ NOT NULL и хранит пустую строку у
-- строк, привязанных по назначению, — совместимость пишется туда, когда назначение владеет ровно
-- одной строкой, иначе там пусто.
SET @col_exists := (
    SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'tech_card_piece_dxf_block'
      AND COLUMN_NAME = 'fabric_purpose'
);
SET @ddl := IF(@col_exists = 0,
    'ALTER TABLE tech_card_piece_dxf_block ADD COLUMN fabric_purpose VARCHAR(24) NULL COMMENT ''назначение (0265) this alias is scoped to — NULL falls back to bom_line_key'' AFTER bom_line_key, ADD CONSTRAINT chk_tcpdb_fabric_purpose CHECK (fabric_purpose IS NULL OR (fabric_purpose REGEXP ''^(main|lining|pocketing|interfacing|insulation|contrast|mesh|other)$'' AND STRCMP(CAST(fabric_purpose AS BINARY), CAST(LOWER(fabric_purpose) AS BINARY)) = 0))',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- 3. Сама формула разрешения, материализованная. STORED, а не VIRTUAL: значение участвует в
-- уникальном индексе, и хранимая колонка делает его обычной колонкой индекса, без вычисления на
-- каждое чтение. Ширина по bom_line_key (26): назначения не длиннее 12 символов.
SET @col_exists := (
    SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'tech_card_piece_dxf_block'
      AND COLUMN_NAME = 'scope_key'
);
SET @ddl := IF(@col_exists = 0,
    'ALTER TABLE tech_card_piece_dxf_block ADD COLUMN scope_key VARCHAR(26) GENERATED ALWAYS AS (COALESCE(fabric_purpose, bom_line_key)) STORED COMMENT ''uniqueness scope: назначение when set, else the legacy BOM line''',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- 4. Скоуп обязан быть непустым. Без этого строка без назначения и без строки BOM получила бы
-- scope_key = '' и склеилась бы в один ведёрко уникальности со всеми такими же — то есть разные
-- ткани начали бы отвергать одноимённые блоки друг у друга. DTO этого не пропускает, но инвариант
-- индекса должен держаться самим индексом, а не вежливостью вызывающего.
SET @chk_exists := (
    SELECT COUNT(*) FROM information_schema.TABLE_CONSTRAINTS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'tech_card_piece_dxf_block'
      AND CONSTRAINT_NAME = 'chk_tcpdb_scope_present'
      AND CONSTRAINT_TYPE = 'CHECK'
);
SET @ddl := IF(@chk_exists = 0,
    'ALTER TABLE tech_card_piece_dxf_block ADD CONSTRAINT chk_tcpdb_scope_present CHECK (fabric_purpose IS NOT NULL OR bom_line_key <> '''')',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- 5. Новый уникальный — ДО дропа старого (см. заметку про 1553 выше). На этот момент scope_key
-- каждой строки равен её bom_line_key, так что индекс строится на данных, которые старый индекс уже
-- признал уникальными: сорваться здесь нечему.
SET @idx_exists := (
    SELECT COUNT(*) FROM information_schema.STATISTICS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'tech_card_piece_dxf_block'
      AND INDEX_NAME = 'uniq_tcpdb_card_scope_block'
);
SET @ddl := IF(@idx_exists = 0,
    'ALTER TABLE tech_card_piece_dxf_block ADD CONSTRAINT uniq_tcpdb_card_scope_block UNIQUE (tech_card_id, scope_key, block_name)',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- 6. Старый уникальный уходит. Оставить его было бы не «страховкой», а запретом на сам переход: он
-- продолжал бы требовать уникальности по СТРОКЕ, а после разбора карточки одно назначение
-- сознательно собирает одноимённые блоки нескольких строк в одну запись — и лишняя пара
-- (строка, блок) с пустым bom_line_key упиралась бы в него на ровном месте.
SET @idx_exists := (
    SELECT COUNT(*) FROM information_schema.STATISTICS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'tech_card_piece_dxf_block'
      AND INDEX_NAME = 'uniq_tcpdb_card_slot_block'
);
SET @ddl := IF(@idx_exists > 0,
    'ALTER TABLE tech_card_piece_dxf_block DROP INDEX uniq_tcpdb_card_slot_block',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- +migrate Down

SET @idx_exists := (
    SELECT COUNT(*) FROM information_schema.STATISTICS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'tech_card_piece_dxf_block'
      AND INDEX_NAME = 'uniq_tcpdb_card_slot_block'
);
SET @ddl := IF(@idx_exists = 0,
    'ALTER TABLE tech_card_piece_dxf_block ADD CONSTRAINT uniq_tcpdb_card_slot_block UNIQUE (tech_card_id, bom_line_key, block_name)',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @idx_exists := (
    SELECT COUNT(*) FROM information_schema.STATISTICS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'tech_card_piece_dxf_block'
      AND INDEX_NAME = 'uniq_tcpdb_card_scope_block'
);
SET @ddl := IF(@idx_exists > 0,
    'ALTER TABLE tech_card_piece_dxf_block DROP INDEX uniq_tcpdb_card_scope_block',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @chk_exists := (
    SELECT COUNT(*) FROM information_schema.TABLE_CONSTRAINTS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'tech_card_piece_dxf_block'
      AND CONSTRAINT_NAME = 'chk_tcpdb_scope_present'
      AND CONSTRAINT_TYPE = 'CHECK'
);
SET @ddl := IF(@chk_exists > 0,
    'ALTER TABLE tech_card_piece_dxf_block DROP CONSTRAINT chk_tcpdb_scope_present',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @col_exists := (
    SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'tech_card_piece_dxf_block'
      AND COLUMN_NAME = 'scope_key'
);
SET @ddl := IF(@col_exists > 0,
    'ALTER TABLE tech_card_piece_dxf_block DROP COLUMN scope_key',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @col_exists := (
    SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'tech_card_piece_dxf_block'
      AND COLUMN_NAME = 'fabric_purpose'
);
SET @ddl := IF(@col_exists > 0,
    'ALTER TABLE tech_card_piece_dxf_block DROP CONSTRAINT chk_tcpdb_fabric_purpose, DROP COLUMN fabric_purpose',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @col_exists := (
    SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'tech_card_size_pattern'
      AND COLUMN_NAME = 'fabric_purpose'
);
SET @ddl := IF(@col_exists > 0,
    'ALTER TABLE tech_card_size_pattern DROP CONSTRAINT chk_tcsp_fabric_purpose, DROP COLUMN fabric_purpose',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;
