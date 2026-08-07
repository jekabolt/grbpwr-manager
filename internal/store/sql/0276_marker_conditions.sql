-- +migrate Up

-- УСЛОВИЯ СЪЁМКИ РАСКЛАДКИ (Ф3). Маркер несёт ширину, кромку, зазор, отступ и эффективность, но не
-- несёт того, ЧТО ИМЕННО раскладывали и НА КАКИХ ПРАВИЛАХ: припуск лежит СТРОКОЙ предупреждения
-- внутри блоба, слой контура и слой долевой не покидают модалку вовсе, политика переворота нигде не
-- записана. Маркер, снятый по линии шва, и маркер по линии кроя выглядят в базе одинаково, а расход
-- у них отличается на припуск по всему периметру каждой детали.
--
-- ПОЧЕМУ КОЛОНКИ, А НЕ БЛОБ. Стор блоб не парсит принципиально (0257, повторено в 0268), и это не
-- стилистика: путь чтения СОЗНАТЕЛЬНО переживает нечитаемый блоб (GetTechCardMarker отдаёт сводку и
-- предупреждение). Гейт годности (Ф6) обязан спрашивать «какой у этой раскладки припуск» ЗАПРОСОМ, а
-- не разбором JSON — иначе одна битая строка гасит проверку для всей карточки. Сводка при этом ездит
-- БЕЗ блоба, то есть список раскладок обязан показывать условия, не скачивая геометрию.
--
-- ПОЧЕМУ ВЕЗДЕ NULL, А НЕ 0. NULL значит «не записано», ноль — записанное значение «не добавляли
-- припуск». Схлопнуть их значит объявить каждый снятый до сегодня маркер раскладкой С НУЛЕВЫМ
-- припуском, то есть выдать уверенный неверный ответ там, где верный — «неизвестно». Ту же дисциплину
-- 0272 завела для длины стола, и по той же причине.
--
-- БЭКФИЛЛА НЕТ, И ЭТО РЕШЕНИЕ. «Старая норма» — категория ПРОИЗВОДНАЯ, а не флаг: она читается как
-- seam_allowance_cm IS NULL. Миграциям нечего помечать — непомеченное само остаётся старым.
-- Единственный возможный бэкфилл (припуск 1.00 см, дефолт модалки) был бы ВЫДУМКОЙ: число в модалке
-- менялось, и записать общий дефолт значило бы объявить измеренным то, чего никто не измерял.
--
-- ВЕРСИЯ БЛОБА НЕ ДВИГАЕТСЯ. Ф3 не добавляет в TechCardMarkerLayout ни одного поля — все семь условий
-- живут колонками этой строки. Номер схемы в этом коде существует ради решений о ПОДДЕЛКЕ и
-- ДЕДОВЩИНЕ (0268), а Ф3 не даёт освобождений по версии и не вводит подделываемого поля блоба:
-- признак «условия не записаны» читается прямым и неподделываемым seam_allowance_cm IS NULL.
--
-- ИДЕМПОТЕНТНОСТЬ (CLAUDE.md): MySQL 8 автокоммитит DDL, поэтому падение в середине файла оставляет
-- схему полуприменённой без строки в gorp_migrations, и следующая загрузка перезапускает файл с
-- начала. Каждый шаг под собственной проверкой в information_schema, PREPARE/EXECUTE/DEALLOCATE по
-- одному оператору на строку (multiStatements=true в контейнерных тестах замаскировал бы баг,
-- который прод не переживёт). Ни один CHECK не дропается по автоимени <table>_chk_N — все имена явные.
--
-- ЯВНЫЙ ALGORITHM/LOCK НЕ ПИШЕТСЯ (аргумент 0273): явный алгоритм, ВДРУГ не поддержанный на
-- управляемом инстансе, — это ошибка, то есть остановка старта прода. Строк в таблице десятки.

-- 1. Пять условий съёмки одним ALTER: в MySQL 8 отдельный DDL-оператор атомарен (транзакционный
--    словарь данных), поэтому проверки по ПЕРВОЙ колонке достаточно — частично применённого ALTER,
--    в котором есть seam_allowance_cm и нет grain_layer, не бывает. Одна перестройка вместо пяти.
SET @col := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_marker'
      AND COLUMN_NAME = 'seam_allowance_cm');
SET @ddl := IF(@col = 0,
    'ALTER TABLE tech_card_marker
       ADD COLUMN seam_allowance_cm DECIMAL(6,2) NULL COMMENT ''припуск, ДОБАВЛЕННЫЙ раздутием контура наружу, см; 0 = не добавляли; NULL = не записано (старая норма)'' AFTER edge_margin_cm,
       ADD COLUMN contour_allowance_cm DECIMAL(6,2) NULL COMMENT ''припуск, УЖЕ содержащийся в разложенном контуре, замерен по файлу, см; 0 = разложена линия шва; >0 = разложена линия кроя; NULL = замерить было нечем'' AFTER seam_allowance_cm,
       ADD COLUMN contour_layer VARCHAR(64) NULL COMMENT ''слой DXF, с которого взят разложенный контур; NULL = не записано'' AFTER contour_allowance_cm,
       ADD COLUMN grain_layer VARCHAR(64) NULL COMMENT ''слой DXF долевой; ПУСТАЯ СТРОКА = не разворачивали (значимо!); NULL = не записано'' AFTER contour_layer,
       ADD COLUMN allow_flip BOOLEAN NULL COMMENT ''разрешался ли переворот детали при поиске (политика, под которой СНЯТ маркер, не направление ткани сегодня); NULL = не записано'' AFTER allow_cross_grain',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- 2. Признак НОРМИРОВОЧНОЙ раскладки. NOT NULL DEFAULT FALSE — в отличие от условий выше, «не норма»
--    это не неизвестность, а факт: норму НАЗНАЧАЮТ отдельным действием, и неназначенная не назначена.
--    Эксклюзивность (одна норма на (карточка, bom_item_id)) держит ТРАНЗАКЦИЯ, а не уникальный
--    индекс — см. комментарий у idx_tcm_card_norm ниже.
SET @col := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_marker' AND COLUMN_NAME = 'is_norm');
SET @ddl := IF(@col = 0,
    'ALTER TABLE tech_card_marker ADD COLUMN is_norm BOOLEAN NOT NULL DEFAULT FALSE COMMENT ''нормировочная раскладка карточки для своей ткани; одна на (tech_card_id, bom_item_id), эксклюзивность держит транзакция SetMarkerNorm''',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- 3. Отпечаток набора деталей КАРТОЧКИ на момент съёмки. NULL = не записан ЛИБО несчитаем; читатель
--    обязан отдать «неизвестно», НЕ «изменился» — иначе бейдж «набор изменился» встал бы разом на
--    каждый маркер, снятый до Ф3, то есть выдал бы шум там, где нужен сигнал.
SET @col := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_marker' AND COLUMN_NAME = 'piece_set_fp');
SET @ddl := IF(@col = 0,
    'ALTER TABLE tech_card_marker ADD COLUMN piece_set_fp CHAR(64) NULL COMMENT ''sha256 набора деталей карточки (line_key + pieces_per_garment) на момент съёмки; NULL = неизвестно, НЕ «изменился»''',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- 4. Оба припуска неотрицательны. Одно имя на оба поля — это одна величина, разложенная надвое:
--    эффективный припуск раскладки = contour_allowance_cm + seam_allowance_cm.
SET @chk := (SELECT COUNT(*) FROM information_schema.TABLE_CONSTRAINTS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_marker'
      AND CONSTRAINT_NAME = 'chk_tcm_allowance_nonneg');
SET @ddl := IF(@chk = 0,
    'ALTER TABLE tech_card_marker ADD CONSTRAINT chk_tcm_allowance_nonneg CHECK (
       (seam_allowance_cm IS NULL OR seam_allowance_cm >= 0) AND
       (contour_allowance_cm IS NULL OR contour_allowance_cm >= 0))',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- 5. ДВОЙНОЙ ПРИПУСК — ИНВАРИАНТ СХЕМЫ, а не привычка приложения. Раскладка не может одновременно
--    лежать по линии кроя (contour_allowance_cm > 0) И быть раздутой офсетом (seam_allowance_cm > 0):
--    припуск оказался бы посчитан дважды, а длина настила — завышена по всему периметру каждой детали.
--    Оператор получает это ровно одним способом: перебрав контурный слой руками на слой кроя и не
--    убрав офсет.
--    NULL проходит НАМЕРЕННО: UNKNOWN у MySQL не нарушает CHECK, и это и есть честная ветка «замерить
--    было нечем» — отсутствие доказательства не есть доказательство нарушения.
--    Сервер отказывает РАНЬШЕ и словами (entity.MarkerAllowanceRefusal); этот CHECK — сеть, а не
--    сообщение, и 3819, дошедшая до API-слоя, есть баг сервера и корректно падает в Internal.
SET @chk := (SELECT COUNT(*) FROM information_schema.TABLE_CONSTRAINTS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_marker'
      AND CONSTRAINT_NAME = 'chk_tcm_no_double_allowance');
SET @ddl := IF(@chk = 0,
    'ALTER TABLE tech_card_marker ADD CONSTRAINT chk_tcm_no_double_allowance CHECK (
       seam_allowance_cm IS NULL OR contour_allowance_cm IS NULL
       OR seam_allowance_cm = 0 OR contour_allowance_cm = 0)',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- 6. Индекс под чтение нормы. ОБЫЧНЫЙ, НЕ УНИКАЛЬНЫЙ, и это решение, а не экономия.
--
--    Уникальный кандидат работает — генерируемая колонка IF(is_norm, IFNULL(bom_item_id,0), NULL) плюс
--    UNIQUE (tech_card_id, norm_key): две нормы разных тканей сосуществуют, вторая норма той же ткани
--    даёт 1062. Но fk_tcm_bom объявлен ON DELETE SET NULL (0257), значит удаление строки BOM переводит
--    её норму в скоуп «без ткани», и если там уже есть норма — УДАЛЕНИЕ ПАДАЕТ:
--
--      ERROR 1761 (23000): Foreign key constraint for table 'tech_card_bom_item', record '2'
--      would lead to a duplicate entry in table 'tech_card_marker', key 'uniq_tcm_card_norm'
--
--    А BOM диффится ВНУТРИ UpdateTechCard. То есть уникальный индекс возвращает ровно ту поломку, ради
--    устранения которой 0257 и выбрала SET NULL («чтобы RESTRICT не валил сохранение всей карточки,
--    стоит оператору удалить слот ткани, который маркер когда-то измерил»), и возвращает её в самом
--    используемом пути продукта — с сообщением, в котором не упомянуты ни норма, ни раскладка.
--
--    Отсюда: инвариант держит SERIALIZABLE-транзакция SetMarkerNorm (единственный писатель колонки), а
--    цена отсутствия индекса гасится тем, что КАЖДЫЙ читатель берёт норму детерминированно
--    (entity.SelectNormMarker: updated_at DESC, id DESC) и ДОКЛАДЫВАЕТ, если норм в скоупе больше
--    одной, а не молча берёт первую.
SET @idx := (SELECT COUNT(*) FROM information_schema.STATISTICS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_marker' AND INDEX_NAME = 'idx_tcm_card_norm');
SET @ddl := IF(@idx = 0,
    'ALTER TABLE tech_card_marker ADD INDEX idx_tcm_card_norm (tech_card_id, is_norm)',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- +migrate Down

-- ГВАРД ИДЁТ ПЕРВЫМ (идиома 0273). Откат дропает колонки, а значит стирает две вещи, которых в
-- до-Ф3 схеме выразить нечем и восстановить неоткуда: ЗАМЕРЫ (припуски, слои, политику переворота) и
-- РЕШЕНИЯ ЧЕЛОВЕКА (какая раскладка нормировочная). Пока ни того ни другого нет — а это ровно то
-- состояние, в котором откат и нужен, сразу после неудачной выкатки, — откат проходит свободно.
-- Как только оператор что-то записал, молчаливое стирание под видом отката запрещено.
--
-- SIGNAL нельзя: «This command is not supported in the prepared statement protocol yet», а без
-- PREPARE в скрипте нет ветвления. Поэтому отказ — обращение к несуществующей КОЛОНКЕ, чьё имя и
-- есть сообщение: ERROR 1054 с внятным текстом, детерминированно, без единого DDL. Идентификатор
-- режется на 64 символах, поэтому текст короткий и ASCII (иначе обрежет посреди UTF-8).
--
-- Что делать, если он сработал: выпишите строки
--   SELECT id, tech_card_id, name, seam_allowance_cm, contour_allowance_cm, is_norm
--   FROM tech_card_marker WHERE seam_allowance_cm IS NOT NULL OR is_norm;
-- и решите сознательно — снять нормы и очистить условия, либо не откатываться.
--
-- САМ ГВАРД ОБЯЗАН ПЕРЕЖИВАТЬ ЧАСТИЧНЫЙ ОТКАТ, и это не мелочь: он ссылается на колонки, которые
-- этот же файл дропает, так что написанный в лоб он падал бы с 1054 на ПОВТОРНОМ прогоне и намертво
-- заклинивал откат. Отсюда проверка наличия семи колонок и запрос, собранный PREPARE'ом: пока все
-- семь на месте — считаем строки, как только их меньше — защищать уже нечего, разрушение произошло.
SET @blocking := 0;
SET @have := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_marker'
      AND COLUMN_NAME IN ('seam_allowance_cm', 'contour_allowance_cm', 'contour_layer',
                          'grain_layer', 'allow_flip', 'piece_set_fp', 'is_norm'));
SET @ddl := IF(@have < 7, 'SELECT 0 INTO @blocking',
    'SELECT COUNT(*) INTO @blocking FROM tech_card_marker
       WHERE seam_allowance_cm IS NOT NULL OR contour_allowance_cm IS NOT NULL
          OR contour_layer IS NOT NULL OR grain_layer IS NOT NULL
          OR allow_flip IS NOT NULL OR piece_set_fp IS NOT NULL OR is_norm');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @ddl := IF(@blocking = 0, 'SELECT 1',
    CONCAT('SELECT `0276 Down blocked: ', @blocking, ' markers carry F3 conditions or a norm`'));
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @idx := (SELECT COUNT(*) FROM information_schema.STATISTICS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_marker' AND INDEX_NAME = 'idx_tcm_card_norm');
SET @ddl := IF(@idx > 0,
    'ALTER TABLE tech_card_marker DROP INDEX idx_tcm_card_norm',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @chk := (SELECT COUNT(*) FROM information_schema.TABLE_CONSTRAINTS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_marker'
      AND CONSTRAINT_NAME = 'chk_tcm_no_double_allowance');
SET @ddl := IF(@chk > 0,
    'ALTER TABLE tech_card_marker DROP CHECK chk_tcm_no_double_allowance',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @chk := (SELECT COUNT(*) FROM information_schema.TABLE_CONSTRAINTS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_marker'
      AND CONSTRAINT_NAME = 'chk_tcm_allowance_nonneg');
SET @ddl := IF(@chk > 0,
    'ALTER TABLE tech_card_marker DROP CHECK chk_tcm_allowance_nonneg',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- ВСЕ СЕМЬ КОЛОНОК ОДНИМ ALTER, и это ровно то, что делает гвард выше осмысленным: отдельный DDL в
-- MySQL 8 атомарен, значит между «гвард видит все семь» и «колонок нет» нет промежуточного состояния,
-- в котором часть замеров уже стёрта, а часть ещё нет. Проверка по @have, а не по одной колонке,
-- чтобы шаг оставался перезапускаемым.
SET @have := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_marker'
      AND COLUMN_NAME IN ('seam_allowance_cm', 'contour_allowance_cm', 'contour_layer',
                          'grain_layer', 'allow_flip', 'piece_set_fp', 'is_norm'));
SET @ddl := IF(@have = 7,
    'ALTER TABLE tech_card_marker
       DROP COLUMN piece_set_fp,
       DROP COLUMN is_norm,
       DROP COLUMN allow_flip,
       DROP COLUMN grain_layer,
       DROP COLUMN contour_layer,
       DROP COLUMN contour_allowance_cm,
       DROP COLUMN seam_allowance_cm',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;
