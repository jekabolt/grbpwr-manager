-- +migrate Up

-- РАЗРЫВ ОПЕРАЦИЙ. Операция теряет тринадцать колонок и получает семь; блок дефолтов карточки
-- теряет пять и получает три. Ни одного UPDATE во всём файле — и это не упущение, а факт: операции
-- на проде не заполняли ни разу, поэтому переносить нечего, и любая «на всякий случай» конверсия
-- писала бы данные, которых не было.
--
-- ПОЧЕМУ ЭТИ ТРИНАДЦАТЬ УХОДЯТ, четырьмя разными группами — потому что лечатся они по-разному, и
-- через полгода это важнее списка (одиннадцать «полей» плюс пара bom_item_id / bom_item_index,
-- которые считаются за одну причину — группа (д) в конце):
--
--   (а) ИХ ПИСАЛ КОД, А НЕ ЧЕЛОВЕК. `machine` и `stitches_per_cm` заполнялись пресетом типа
--       операции, `thread` — именем привязанной строки BOM, `placement` — склейкой имён деталей.
--       Каждое затем ХРАНИЛОСЬ как факт и хешировалось в подписанный дайджест, а печатная форма
--       была вынуждена ВЫЧИТАТЬ `thread` из списка материалов, чтобы одна и та же строка не
--       печаталась дважды. Ремонт симптома дублирования владельца — ровно то, от чего
--       предостерегает шапка 0278.
--   (б) ИХ НИКТО НЕ ЧИТАЛ. `needle` дублировал material_thread_attr.needle_reco, который уже несёт
--       артикул нитки; во всём клиенте копия на операции встречалась только в редакторе и в печати.
--   (в) ОНИ ЗАДАВАЛИ ВОПРОС БЕЗ ОДНОГО ОТВЕТА. `node` («узел / что», ОБЯЗАТЕЛЬНОЕ) предлагал список,
--       где вперемешку швы, участки изделия, детали и материалы, и повторял глагол из соседнего
--       поля. Действующий конструктор не смог его заполнить и спросил, нужно ли оно вообще. Замены
--       нет и не будет: необязательное поле с тем же неотвечаемым вопросом просто заполняли бы
--       по-разному на каждой карточке. Заголовок шага СОБИРАЕТСЯ из типа, зоны и деталей.
--   (г) `time_norm` — легаси-дубль `smv`. Два поля времени в одной форме без правила, какое
--       заполнять, гарантируют, что половина карточек пронормирована в одной колонке, половина в
--       другой. Остаётся smv.
--
-- ЗОНА ТЕПЕРЬ ОТВЕЧАЕТ «ГДЕ», А НЕ «КАКОЙ СЛОЙ», и словарь у неё ОБЩИЙ с зоной замечаний примерки
-- (0256, chk_fcr_zone_v2). Восемнадцать токенов здесь обязаны совпадать с тем CHECK'ом байт в байт:
-- в Go источник один (entity.GarmentZoneTokens), и две схемные копии — единственное место, где они
-- ещё могут разойтись. Именно поэтому `placement` уходит: зона его поглощает.
--
-- ЧЕГО ФАЙЛ НЕ ТРОГАЕТ: tech_card_operation_piece (0199) и tech_card_operation_bom (0200). Это и
-- есть правильные ссылки — многие-ко-многим, по стабильным line_key. Одиночные bom_item_id /
-- bom_item_index / bom_line_key, которые уходят, были вторым ответом на тот же вопрос.
--
-- ИДЕМПОТЕНТНОСТЬ (CLAUDE.md): MySQL 8 автокоммитит DDL, поэтому падение в середине оставляет схему
-- полуприменённой без строки в gorp_migrations, и следующая загрузка перезапускает файл с начала.
-- Каждый шаг — под собственной проверкой information_schema, PREPARE/EXECUTE/DEALLOCATE по одному
-- оператору на строку (иначе multiStatements=true в контейнерных тестах маскирует поломку, которая
-- на проде валит старт).
--
-- CHECK'И ИМЕНОВАНЫ ЯВНО: авто-имя <table>_chk_<n> позиционно и дрейфует по истории схемы.
-- REGEXP наследует коллацию столбца, а она на проде (utf8mb3_general_ci) регистронезависима, так что
-- голый шаблон принял бы 'OUTER'; байты сравниваются через STRCMP с LOWER — `REGEXP BINARY` под
-- utf8mb4_0900_ai_ci отвечает 3995.

-- 1. Снять внешний ключ на строку BOM ДО удаления колонки, которая его держит.
SET @fk := (SELECT COUNT(*) FROM information_schema.TABLE_CONSTRAINTS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_operation'
      AND CONSTRAINT_NAME = 'fk_op_bom');
SET @ddl := IF(@fk = 1, 'ALTER TABLE tech_card_operation DROP FOREIGN KEY fk_op_bom', 'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- 2. Старая проверка зоны. Она пришла из 0076 безымянной (tech_card_operation_chk_2 в порядке
--    создания), поэтому дропается ПО ФАКТИЧЕСКОМУ ИМЕНИ, вычитанному из information_schema, а не по
--    угаданному: позиционные авто-имена и есть та ловушка, ради которой дом требует именовать.
SET @zone_chk := (SELECT c.CONSTRAINT_NAME FROM information_schema.CHECK_CONSTRAINTS c
    JOIN information_schema.TABLE_CONSTRAINTS t
      ON t.CONSTRAINT_SCHEMA = c.CONSTRAINT_SCHEMA AND t.CONSTRAINT_NAME = c.CONSTRAINT_NAME
    WHERE c.CONSTRAINT_SCHEMA = DATABASE() AND t.TABLE_NAME = 'tech_card_operation'
      AND c.CHECK_CLAUSE LIKE '%interlining%' AND c.CHECK_CLAUSE NOT LIKE '%sleeve%'
    LIMIT 1);
SET @ddl := IF(@zone_chk IS NULL, 'SELECT 1',
    CONCAT('ALTER TABLE tech_card_operation DROP CHECK ', @zone_chk));
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- 3. Одиннадцать колонок долой. Один ALTER: MySQL 8 применяет DDL атомарно, поэтому повтор после
--    успеха невозможен, а повтор после падения видит @gone = 0 и проходит целиком заново.
SET @gone := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_operation' AND COLUMN_NAME = 'node');
SET @ddl := IF(@gone = 1,
    'ALTER TABLE tech_card_operation
        DROP COLUMN node,
        DROP COLUMN description,
        DROP COLUMN seam_type,
        DROP COLUMN machine,
        DROP COLUMN topstitch_width,
        DROP COLUMN seam_allowance,
        DROP COLUMN thread,
        DROP COLUMN needle,
        DROP COLUMN attachment,
        DROP COLUMN time_norm,
        DROP COLUMN placement,
        DROP COLUMN bom_item_id,
        DROP COLUMN bom_item_index',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- 4. Семь новых колонок и их проверки.
--
--    ПОЧЕМУ ВЕЗДЕ NULL, А НЕ 0 — тот же довод, что в 0274 и 0278: NULL значит «наследуется от
--    карточки», и это НЕ ноль. У припуска ноль — законное и осмысленное значение («выкройки несут
--    линию кроя»), поэтому спутать «не задан» с «ноль» здесь значит объявить, что шаг требует кроя
--    по линии, тогда как он не требует ничего.
--
--    topstitch_width_mm без topstitch_mode='width' — теневое значение: «в край» ширины не имеет.
--    Пару проверяет Go рядом с читаемым сообщением (двухколоночный CHECK выстрелил бы сырым 3819 и
--    назвал бы колонку, которой оператор не касался, — довод 0278), а схема закрывает словари.
SET @new := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_operation'
      AND COLUMN_NAME = 'seam_class');
SET @ddl := IF(@new = 0,
    'ALTER TABLE tech_card_operation
        ADD COLUMN seam_class VARCHAR(16) NULL COMMENT ''класс шва ISO 4916; NULL = наследуется от tech_card_construction.default_seam_class'' AFTER zone,
        ADD COLUMN seam_allowance_mm DECIMAL(6,1) NULL COMMENT ''припуск ЭТОГО шва, мм; NULL = наследуется от tech_card.required_seam_allowance_mm; 0 ЗАКОНЕН (кроим по линии как нарисована)'' AFTER seam_class,
        ADD COLUMN topstitch_mode VARCHAR(8) NULL COMMENT ''edge = в край, width = на заданной ширине; NULL = отстрочки нет'' AFTER seam_allowance_mm,
        ADD COLUMN topstitch_width_mm DECIMAL(6,1) NULL COMMENT ''ширина отстрочки, мм; законна ТОЛЬКО при topstitch_mode = width'' AFTER topstitch_mode,
        ADD COLUMN topstitch_rows TINYINT NULL COMMENT ''число строчек отстрочки, 1..4; NULL = не задано'' AFTER topstitch_width_mm,
        ADD COLUMN attachment_kind VARCHAR(24) NULL COMMENT ''приспособление (лапка/направитель/окантовыватель); NULL = без приспособления либо не указано — различать их для лапки незачем'' AFTER topstitch_rows,
        ADD COLUMN attachment_size_mm DECIMAL(6,1) NULL COMMENT ''размер приспособления, мм (окантовыватель 32/8); без attachment_kind бессмыслен'' AFTER attachment_kind,
        ADD CONSTRAINT chk_op_seam_class CHECK (seam_class IS NULL OR (seam_class REGEXP ''^(ss_plain|ss_french|ls_lapped|ls_flat_felled|ef_hem_raw|ef_hem_turned|ef_faced|bs_bound|fs_flat|os_topstitch|other)$'' AND STRCMP(CAST(seam_class AS BINARY), CAST(LOWER(seam_class) AS BINARY)) = 0)),
        ADD CONSTRAINT chk_op_topstitch_mode CHECK (topstitch_mode IS NULL OR (topstitch_mode REGEXP ''^(edge|width)$'' AND STRCMP(CAST(topstitch_mode AS BINARY), CAST(LOWER(topstitch_mode) AS BINARY)) = 0)),
        ADD CONSTRAINT chk_op_attachment_kind CHECK (attachment_kind IS NULL OR (attachment_kind REGEXP ''^(binder|hemmer_folder|scroll_foot|zipper_foot|invisible_zipper_foot|edge_guide|piping_foot|elastic_attachment|other)$'' AND STRCMP(CAST(attachment_kind AS BINARY), CAST(LOWER(attachment_kind) AS BINARY)) = 0)),
        ADD CONSTRAINT chk_op_seam_allowance_mm CHECK (seam_allowance_mm IS NULL OR (seam_allowance_mm >= 0 AND seam_allowance_mm <= 100)),
        ADD CONSTRAINT chk_op_topstitch_width_mm CHECK (topstitch_width_mm IS NULL OR (topstitch_width_mm >= 0 AND topstitch_width_mm <= 100)),
        ADD CONSTRAINT chk_op_attachment_size_mm CHECK (attachment_size_mm IS NULL OR (attachment_size_mm >= 0 AND attachment_size_mm <= 100)),
        ADD CONSTRAINT chk_op_topstitch_rows CHECK (topstitch_rows IS NULL OR (topstitch_rows >= 1 AND topstitch_rows <= 4))',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- 5. Новая проверка зоны — восемнадцать токенов, байт в байт как chk_fcr_zone_v2 (0256).
SET @zone_v2 := (SELECT COUNT(*) FROM information_schema.TABLE_CONSTRAINTS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_operation'
      AND CONSTRAINT_NAME = 'chk_op_zone');
SET @ddl := IF(@zone_v2 = 0,
    'ALTER TABLE tech_card_operation ADD CONSTRAINT chk_op_zone CHECK (zone REGEXP ''^(unknown|outer|lining|interlining|sleeve|collar|neckline|armhole|shoulder|chest|waist|hip|hem|pocket|closure|back|front|other)$'' AND STRCMP(CAST(zone AS BINARY), CAST(LOWER(zone) AS BINARY)) = 0)',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- 6. Блок дефолтов карточки. Пять свободных строк уходят, три типизированных приходят.
--
--    ПОЧЕМУ `overlock_thread_count`, А НЕ `overlock_threads` С ДРУГИМ ТИПОМ: имя, переиспользованное
--    под новым типом, — тот самый молчаливый дрейф, ради которого дом держит removed-registry.
--    Номер и имя уходят навсегда.
--
--    ПОЧЕМУ ЗДЕСЬ НЕТ ПРИПУСКА: эталон карточки уже существует как tech_card.required_seam_allowance
--    (0277 положила его на КАРТОЧКУ намеренно — поле в проекции дайджеста секции помечает каждую
--    утверждённую подпись этой секции протухшей в момент добавления). Второй карточный припуск был
--    бы вторым ответом на решённый вопрос.
--
--    ПОЧЕМУ НЕТ `main_stitch_type`: тип стежка (301/504/602) — факт ШАГА, его несёт
--    tech_card_operation.operation_type, и общекарточного значения у него не бывает.
SET @cons := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_construction'
      AND COLUMN_NAME = 'main_stitch_type');
SET @ddl := IF(@cons = 1,
    'ALTER TABLE tech_card_construction
        DROP COLUMN main_stitch_type,
        DROP COLUMN stitch_density,
        DROP COLUMN overlock_threads,
        DROP COLUMN seam_allowances,
        DROP COLUMN machine_class,
        ADD COLUMN default_seam_class VARCHAR(16) NULL COMMENT ''класс шва ISO 4916 по умолчанию; наследуется шагами, у которых seam_class IS NULL'',
        ADD COLUMN default_stitches_per_cm DECIMAL(5,2) NULL COMMENT ''плотность строчки по умолчанию, стежков на САНТИМЕТР (не миллиметр — плотность везде считают на см)'',
        ADD COLUMN overlock_thread_count TINYINT NULL COMMENT ''ниток в оверлоке: 3, 4 или 5; NULL = не задано'',
        ADD CONSTRAINT chk_construction_seam_class CHECK (default_seam_class IS NULL OR (default_seam_class REGEXP ''^(ss_plain|ss_french|ls_lapped|ls_flat_felled|ef_hem_raw|ef_hem_turned|ef_faced|bs_bound|fs_flat|os_topstitch|other)$'' AND STRCMP(CAST(default_seam_class AS BINARY), CAST(LOWER(default_seam_class) AS BINARY)) = 0)),
        ADD CONSTRAINT chk_construction_stitches CHECK (default_stitches_per_cm IS NULL OR (default_stitches_per_cm > 0 AND default_stitches_per_cm <= 20)),
        ADD CONSTRAINT chk_construction_overlock_threads CHECK (overlock_thread_count IS NULL OR (overlock_thread_count >= 3 AND overlock_thread_count <= 5))',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- +migrate Down
--
-- Откат возвращает КОЛОНКИ, но не содержимое: снятые строки не сохранялись нигде, потому что их не
-- было. Это честный откат схемы, а не машина времени.
SET @back := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_operation' AND COLUMN_NAME = 'seam_class');
SET @ddl := IF(@back = 1,
    'ALTER TABLE tech_card_operation
        DROP CHECK chk_op_zone,
        DROP CHECK chk_op_topstitch_rows,
        DROP CHECK chk_op_attachment_size_mm,
        DROP CHECK chk_op_topstitch_width_mm,
        DROP CHECK chk_op_seam_allowance_mm,
        DROP CHECK chk_op_attachment_kind,
        DROP CHECK chk_op_topstitch_mode,
        DROP CHECK chk_op_seam_class,
        DROP COLUMN attachment_size_mm,
        DROP COLUMN attachment_kind,
        DROP COLUMN topstitch_rows,
        DROP COLUMN topstitch_width_mm,
        DROP COLUMN topstitch_mode,
        DROP COLUMN seam_allowance_mm,
        DROP COLUMN seam_class,
        ADD COLUMN node VARCHAR(255) NOT NULL DEFAULT '''' COMMENT ''узел / операция'',
        ADD COLUMN description TEXT NULL,
        ADD COLUMN seam_type VARCHAR(255) NULL,
        ADD COLUMN machine VARCHAR(64) NULL,
        ADD COLUMN topstitch_width VARCHAR(64) NULL,
        ADD COLUMN seam_allowance VARCHAR(64) NULL,
        ADD COLUMN thread VARCHAR(255) NULL,
        ADD COLUMN needle VARCHAR(64) NULL,
        ADD COLUMN attachment VARCHAR(64) NULL,
        ADD COLUMN time_norm DECIMAL(7,3) NULL,
        ADD COLUMN placement VARCHAR(255) NULL,
        ADD COLUMN bom_item_id INT NULL,
        ADD COLUMN bom_item_index INT NULL',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @cons_back := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_construction'
      AND COLUMN_NAME = 'default_seam_class');
SET @ddl := IF(@cons_back = 1,
    'ALTER TABLE tech_card_construction
        DROP CHECK chk_construction_overlock_threads,
        DROP CHECK chk_construction_stitches,
        DROP CHECK chk_construction_seam_class,
        DROP COLUMN overlock_thread_count,
        DROP COLUMN default_stitches_per_cm,
        DROP COLUMN default_seam_class,
        ADD COLUMN main_stitch_type VARCHAR(255) NULL,
        ADD COLUMN stitch_density VARCHAR(64) NULL,
        ADD COLUMN overlock_threads VARCHAR(32) NULL,
        ADD COLUMN seam_allowances VARCHAR(255) NULL,
        ADD COLUMN machine_class VARCHAR(255) NULL',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;
