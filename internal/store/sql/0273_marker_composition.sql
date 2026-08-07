-- +migrate Up

-- СОСТАВ РАСКЛАДКИ ВМЕСТО ОДНОГО РАЗМЕРА (Ф2). Маркер перестаёт быть «один размер × N комплектов»
-- и становится картой «размер → сколько изделий этого размера режет настил».
--
-- ЗАЧЕМ. Смешанный настил укладывается ПЛОТНЕЕ однородного: мелкие детали одного размера садятся в
-- межлекальные выпады другого. Цех кладёт именно такие настилы, а выразить их «размером и
-- комплектами» было нечем — значит каждая снятая норма измерена на настиле, который никогда не
-- режут. Плюс расход на изделие оказывался средним по ОДНОМУ размеру и переносился на весь ряд.
--
-- ПОЧЕМУ ДОЧЕРНЯЯ ТАБЛИЦА, А НЕ JSON-КОЛОНКА НА СТРОКЕ. Три причины, и ни одна не про вкус:
--   * В самом сторе (internal/store/techcard/markers.go, комментарий к markerSummaryColumns) уже
--     висит предупреждение: JSON-колонки, прочитанные через `*`, воскрешают баг закавыченного
--     JSON-скаляра (лечение — entity.UnquoteLegacyComposition). Наступать на эти грабли второй раз
--     в той же таблице — плохая инженерия.
--   * Ф4.5 (покрытие по размерам) и Ф6.2 (годность нормы) обязаны спрашивать «какие размеры режет
--     этот маркер» ЗАПРОСОМ. По таблице это джойн; по JSON — либо JSON_TABLE, либо разбор в Go по
--     всем маркерам карточки.
--   * FK на size(id) даёт то, чего JSON не даст никогда: состав не может сослаться на
--     несуществующий размер.
--
-- ПОЧЕМУ size_id/sets СТАНОВЯТСЯ NULLABLE, А НЕ УДАЛЯЮТСЯ. Удалить нельзя: их шлёт СТАРАЯ ВКЛАДКА
-- админки (SPA со стейлым бандлом), и такой маркер обязан сохраняться как раньше. Записать в size_id
-- «главный размер состава» было бы дешевле и ровно поэтому неправильно: каждый сегодняшний читатель
-- продолжил бы читать это поле как «размер, который нормирует маркер» и получил бы на смешанном
-- настиле правдоподобно выглядящий неверный ответ. NULL — форсирующая функция: в паре со сменой
-- Go-типа на sql.NullInt64 он превращает каждого читателя в ОШИБКУ КОМПИЛЯЦИИ, то есть находит их
-- за нас, а не в проде через «converting NULL to int is unsupported».
--
-- ПОЧЕМУ total_units — КОЛОНКА, А НЕ SUM ПО ДЕТЯМ. Это ДЕЛИТЕЛЬ ДЕНЕГ (consumption_per_unit_cm =
-- used_length_cm / total_units). Делитель-агрегат при потере одной дочерней строки уменьшается
-- молча, то есть молча повышает себестоимость всего, что нормирует этот маркер. Скаляр, записанный
-- в той же транзакции, что и дети, такой потери не переживёт незамеченным (интеграционный тест
-- сверяет равенство), и на него можно повесить CHECK, чего на SUM повесить нельзя.
--
-- ПОЧЕМУ total_units NULLABLE. Есть ровно одно окно, в котором строка появится без него: DO катит
-- контейнеры внахлёст, миграции применяет НОВЫЙ контейнер до начала обслуживания, но СТАРЫЙ ещё жив
-- и может сохранить маркер — его INSERT колонку не перечисляет. NULL честно говорит «не записано», и
-- читатель (entity.TotalUnitsOrLegacy) падает на sets. NOT NULL DEFAULT 1 соврал бы «одно изделие» и
-- завысил бы норму в N раз.
--
-- ПОЧЕМУ БЭКФИЛЛ ДОКАЗУЕМО ПОЛОН. До этой миграции size_id INT NOT NULL (0257:28) и sets INT NOT
-- NULL DEFAULT 1 с CHECK (sets >= 1) (0257:36,50). Значит для КАЖДОЙ существующей строки
-- size_id IS NOT NULL и sets >= 1, шаг 5 отбирает по size_id IS NOT NULL и берёт их все, давая ровно
-- одну дочернюю запись с quantity >= 1, а шаг 6 ставит total_units = sets >= 1. Пустого состава и
-- quantity = 0 после этой миграции не существует.
--
-- ИДЕМПОТЕНТНОСТЬ (CLAUDE.md): MySQL 8 автокоммитит DDL, поэтому падение в середине файла оставляет
-- схему полуприменённой без строки в gorp_migrations, и следующая загрузка перезапускает файл с
-- начала. Каждый шаг под собственной проверкой в information_schema, PREPARE/EXECUTE/DEALLOCATE по
-- одному оператору на строку (иначе multiStatements=true в контейнерных тестах замаскировал бы баг,
-- который прод не переживёт). Шаги 5 и 6 идемпотентны по построению (NOT EXISTS / пересчёт).
--
-- ЯВНЫЙ ALGORITHM/LOCK НА MODIFY НЕ ПИШЕТСЯ СОЗНАТЕЛЬНО. INPLACE/LOCK=NONE на этой таблице MySQL
-- 8.0.46 принимает, но явный алгоритм, ВДРУГ не поддержанный на управляемом инстансе, — это ошибка,
-- то есть остановка старта прода. Строк в таблице десятки; неявный выбор движка тут ничего не стоит,
-- а фейл-режим убирает.

-- 1. size_id / sets → легаси-nullable. MODIFY требует ПОЛНОГО переопределения колонки: не указав
--    COMMENT, его сотрёшь; не указав DEFAULT NULL, оставишь DEFAULT 1 (и тогда INSERT без колонки
--    писал бы 1, а не NULL).
SET @is_nullable := (SELECT IS_NULLABLE FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_marker' AND COLUMN_NAME = 'size_id');
SET @ddl := IF(@is_nullable = 'NO',
    'ALTER TABLE tech_card_marker MODIFY COLUMN size_id INT NULL COMMENT ''ЛЕГАСИ: размер однородного маркера, снятого до Ф2; NULL = маркер с составом, см. tech_card_marker_size'', MODIFY COLUMN sets INT NULL DEFAULT NULL COMMENT ''ЛЕГАСИ: комплектов до Ф2; NULL = маркер с составом, см. total_units''',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- 2. total_units — число ИЗДЕЛИЙ (не деталей), делитель расхода.
SET @col_exists := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_marker' AND COLUMN_NAME = 'total_units');
SET @ddl := IF(@col_exists = 0,
    'ALTER TABLE tech_card_marker ADD COLUMN total_units INT NULL COMMENT ''изделий в раскладке = SUM(tech_card_marker_size.quantity); NULL = не записано (строка от старого бинарника в окне выкатки), читатель падает на sets'' AFTER sets',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- 3. size_key: генерируемая колонка, существующая ТОЛЬКО чтобы уникальность имени пережила NULL.
--    Проверено на MySQL 8.0.46: с size_id IS NULL уникальность uniq_tcm_card_size_name (0257:56)
--    ПРОПАДАЕТ — две строки (card, NULL, 'name') вставляются обе, потому что NULL не равен NULL. Это
--    та же ловушка, которую обошла 0264, и обойти её здесь нечем: колонка обязана быть nullable.
--    IFNULL(size_id, 0) безопасен на живых данных: до этой миграции size_id NOT NULL, значит
--    size_key ≡ size_id для КАЖДОЙ существующей строки и новый индекс ей ровно эквивалентен — упасть
--    на дубликате он не может. (Простой UNIQUE (tech_card_id, name) упал бы: сегодня законно иметь
--    «основная» на S и «основная» на M.)
SET @col_exists := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_marker' AND COLUMN_NAME = 'size_key');
SET @ddl := IF(@col_exists = 0,
    'ALTER TABLE tech_card_marker ADD COLUMN size_key INT AS (IFNULL(size_id, 0)) VIRTUAL COMMENT ''0 = маркер с составом; существует только чтобы уникальность имени пережила NULL в size_id''',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @idx_exists := (SELECT COUNT(*) FROM information_schema.STATISTICS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_marker'
      AND INDEX_NAME = 'uniq_tcm_card_sizekey_name');
SET @ddl := IF(@idx_exists = 0,
    'ALTER TABLE tech_card_marker ADD CONSTRAINT uniq_tcm_card_sizekey_name UNIQUE (tech_card_id, size_key, name)',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- 4. Проекция состава. Полная замена детей на каждое сохранение маркера, в той же транзакции, что и
--    строка. Никакого charset-клауза (прецедент 0252/0257): прод/бета живут на серверном utf8mb3,
--    контейнерные тесты подключаются utf8mb4, и явное указание развело бы их незаметно.
CREATE TABLE IF NOT EXISTS tech_card_marker_size (
    id INT PRIMARY KEY AUTO_INCREMENT,
    marker_id INT NOT NULL,
    size_id INT NOT NULL,
    quantity INT NOT NULL COMMENT 'изделий этого размера в раскладке',
    CONSTRAINT chk_tcms_qty_pos CHECK (quantity >= 1),
    CONSTRAINT uniq_tcms_marker_size UNIQUE (marker_id, size_id),
    CONSTRAINT fk_tcms_marker FOREIGN KEY (marker_id) REFERENCES tech_card_marker (id) ON DELETE CASCADE,
    -- RESTRICT зеркалит fk_tcm_size (0257:58-60): размеры — словарные строки, и маркер, молча
    -- потерявший размер, делает свой расход неатрибутируемым.
    CONSTRAINT fk_tcms_size FOREIGN KEY (size_id) REFERENCES size (id) ON DELETE RESTRICT
) ENGINE=InnoDB COMMENT 'СОСТАВ раскладки: размер → сколько изделий (Ф2). Проекция layout.composition для запросов и сводки';

-- 5. Бэкфилл: каждый снятый до Ф2 маркер = состав из ОДНОГО размера. Идемпотентно по NOT EXISTS.
INSERT INTO tech_card_marker_size (marker_id, size_id, quantity)
SELECT m.id, m.size_id, GREATEST(COALESCE(m.sets, 1), 1)
FROM tech_card_marker m
WHERE m.size_id IS NOT NULL
  AND NOT EXISTS (SELECT 1 FROM tech_card_marker_size c WHERE c.marker_id = m.id);

-- 6. total_units из той же проекции. Идемпотентно (пересчёт того же числа).
UPDATE tech_card_marker m
SET m.total_units = (SELECT SUM(c.quantity) FROM tech_card_marker_size c WHERE c.marker_id = m.id)
WHERE EXISTS (SELECT 1 FROM tech_card_marker_size c WHERE c.marker_id = m.id);

-- 7. CHECK на делитель денег — после бэкфилла, на уже честных данных. Имя явное: домовое правило
--    запрещает дропать авто-именованные <table>_chk_N, значит и заводить их незачем.
SET @chk_exists := (SELECT COUNT(*) FROM information_schema.TABLE_CONSTRAINTS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_marker'
      AND CONSTRAINT_NAME = 'chk_tcm_total_units_pos');
SET @ddl := IF(@chk_exists = 0,
    'ALTER TABLE tech_card_marker ADD CONSTRAINT chk_tcm_total_units_pos CHECK (total_units IS NULL OR total_units >= 1)',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- +migrate Down

-- ГВАРД ИДЁТ ПЕРВЫМ, И ЭТО НЕ КОСМЕТИКА. Раньше Down сначала ронял tech_card_marker_size и
-- total_units и только потом проверял, есть ли строки с составом — а проверка всего лишь ПРОПУСКАЛА
-- последний MODIFY. sql-migrate видел exit 0, УДАЛЯЛ строку из gorp_migrations и рапортовал успех, а
-- маркер со смешанным составом превращался в (size_id NULL, sets NULL, total_units NULL, ноль детей):
-- состояние, которое эта же миграция называет недостижимым. Читатели дают тогда total_units = 1 и
-- отдали бы на провод ВЕСЬ настил как норму на одно изделие. Откат обязан ПАДАТЬ до первого дропа.
--
-- SIGNAL нельзя: «This command is not supported in the prepared statement protocol yet» (проверено на
-- MySQL 8.0.46), а без PREPARE в скрипте нет ветвления. Поэтому отказ — обращение к несуществующей
-- КОЛОНКЕ, чьё имя и есть сообщение: ERROR 1054 с внятным текстом, детерминированно, без единого DDL.
-- Идентификатор режется на 64 символах, поэтому текст короткий и ASCII (иначе обрежет посреди UTF-8).
--
-- Что делать, если он сработал: такие маркеры невыразимы в до-Ф2 схеме, у них нет ОДНОГО размера.
-- Либо пересохраните их однородными, либо удалите. Молча свернуть состав в «размер 0 / 1 комплект»
-- значит потерять данные под видом отката.
SET @blocking := (SELECT COUNT(*) FROM tech_card_marker WHERE size_id IS NULL OR sets IS NULL);
SET @ddl := IF(@blocking = 0, 'SELECT 1', CONCAT('SELECT `0273 Down blocked: ', @blocking, ' markers carry a composition`'));
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @chk_exists := (SELECT COUNT(*) FROM information_schema.TABLE_CONSTRAINTS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_marker'
      AND CONSTRAINT_NAME = 'chk_tcm_total_units_pos');
SET @ddl := IF(@chk_exists > 0,
    'ALTER TABLE tech_card_marker DROP CHECK chk_tcm_total_units_pos',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

DROP TABLE IF EXISTS tech_card_marker_size;

SET @idx_exists := (SELECT COUNT(*) FROM information_schema.STATISTICS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_marker'
      AND INDEX_NAME = 'uniq_tcm_card_sizekey_name');
SET @ddl := IF(@idx_exists > 0,
    'ALTER TABLE tech_card_marker DROP INDEX uniq_tcm_card_sizekey_name',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @col_exists := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_marker' AND COLUMN_NAME = 'size_key');
SET @ddl := IF(@col_exists > 0,
    'ALTER TABLE tech_card_marker DROP COLUMN size_key',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @col_exists := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_marker' AND COLUMN_NAME = 'total_units');
SET @ddl := IF(@col_exists > 0,
    'ALTER TABLE tech_card_marker DROP COLUMN total_units',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- Обратно в NOT NULL. Гвард наверху уже гарантировал, что NULL-строк нет; проверка повторяется здесь,
-- чтобы файл оставался перезапускаемым после частичного отката (MySQL автокоммитит DDL).
SET @nullable_rows := (SELECT COUNT(*) FROM tech_card_marker WHERE size_id IS NULL OR sets IS NULL);
SET @is_nullable := (SELECT IS_NULLABLE FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_marker' AND COLUMN_NAME = 'size_id');
SET @ddl := IF(@is_nullable = 'YES' AND @nullable_rows = 0,
    'ALTER TABLE tech_card_marker MODIFY COLUMN size_id INT NOT NULL COMMENT ''the size whose pieces this marker lays out'', MODIFY COLUMN sets INT NOT NULL DEFAULT 1 COMMENT ''комплектов: how many garments this layout cuts at once''',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;
