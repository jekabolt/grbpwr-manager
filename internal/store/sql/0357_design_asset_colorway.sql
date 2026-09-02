-- Полоса DESIGN, круг 12, волна B (G-15): ТКАНЬ КОЛОРВЕЯ.
--
-- МОДЕЛЬ ВЛАДЕЛЬЦА, дословно: «паттерн — это бесшовная плитка, а бесшовная плитка это ткань».
-- Сделали один раз, она лежит в библиотеке карточки, и в рендере/3D выбирается КАК ТКАНЬ ЭТОГО
-- КОЛОРВЕЯ. То есть колорвей носит «цвет ИЛИ паттерн».
--
-- ХРАНИТСЯ ТОЛЬКО ВТОРАЯ ПОЛОВИНА, И ЭТО РЕШЕНИЕ. «Какого цвета колорвей» карточка УЖЕ отвечает:
-- строка product несёт dev_hex, pantone, dev_name, color_code и свотч, и карточка отдаёт их
-- через techCard.colorways. Завести рядом второе поле под цвет значило бы поставить
-- КОНКУРИРУЮЩИЙ ответ на вопрос, у которого ответ уже есть, — а два ответа на один вопрос
-- расходятся молча (ложное расщепление, за которое эта система платила уже дважды). Дома не
-- хватало ровно факту «колорвей N носит ткань X»: одна NULLable колонка на полке.
--
-- ПОЧЕМУ НА design_asset, А НЕ НА product. Полка карточки — уже построенный дом: kind
-- fabric|pattern|hardware, медиа, раппорт (repeat_mm), имя, цвет; «keep as cloth» уже кладёт туда
-- плитку прогона, промпт уже цитирует ассет по имени и шлёт его текстуру картинкой. Не хватало
-- одного ребра — «чей». Поле же на product было бы FK из домена ТОВАРА в домен полосы и писалось
-- бы через UpdateColorway под оптимистичным замком стиля: каждый жест «примерь паттерн» двигал бы
-- lock_version карточки и конфликтовал с конструктором. Дорого, чужой путь записи и неверно по
-- смыслу: ткань для РЕНДЕРА — рабочий материал студии, а подписанный факт стиля — это рецепт BOM
-- с пином артикула, и он существует отдельно и не про это.
--
-- ПОЧЕМУ КОЛОНКА, А НЕ «ВЫБОР В ПРОГОНЕ». Замороженные params прогона всё помнят, и клиентское
-- решение без бэкенда не соврало бы. Но «колорвей носит паттерн» — заявление о КАРТОЧКЕ, а у него
-- не было бы дома: назначь сегодня, открой карточку завтра — и связь живёт только в хвосте ленты
-- прогонов (страница!), то есть её нельзя ни показать на вкладке паттернов, ни надёжно
-- преднаполнить. Сам запрос «сохранять паттерны и пробрасывать в колорвеи» остался бы жестом без
-- памяти.
--
-- ОДНА ТКАНЬ НА КОЛОРВЕЙ — И ЭТО ДЕРЖИТ КЛЮЧ, А НЕ ТОЛЬКО GO.
--
-- ⚠ ПЕРВАЯ РЕДАКЦИЯ ЭТОЙ ШАПКИ ОТКАЗЫВАЛАСЬ ОТ КЛЮЧА, И ОБА ЕЁ ДОВОДА НЕ ПЕРЕЖИЛИ ЧТЕНИЯ.
-- Записаны они здесь целиком, потому что «довод, переживший свою причину» уже защищал в этой
-- системе дефект, и вычеркнуть его молча значило бы дать ему второй заход:
--   * «NULL не равен NULL, ключ не ограничит ничьи ассеты» — ЭТО РОВНО ТО, ЧТО НУЖНО. Ничьих
--     ассетов на карточке сколько угодно, и ограничивать их нечем и незачем; единственность
--     требуется у НАЗВАННЫХ, а по ним ключ работает в полную силу. Свойство было названо пороком,
--     будучи ровно требованием.
--   * «клик по соседнему чипу стал бы 1062» — НЕВЕРНО ПРИ СОБСТВЕННОМ ЖЕ ПОРЯДКЕ ОПЕРАЦИЙ.
--     SetAssetColorway СНАЧАЛА снимает колорвей со всех прочих ассетов карточки и ТОЛЬКО ПОТОМ
--     ставит его цели; к моменту второго UPDATE ни одна строка этот колорвей не держит, и
--     столкнуться не с чем. Довод описывал порядок, которого в коде нет.
--
-- Поэтому ключ ЕСТЬ, и он — пояс, а не соперник кражи. Кража остаётся механизмом (она выражает
-- намерение «теперь ткань N — вот эта» и обязана быть в той же транзакции), а
-- uq_design_asset_colorway делает состояние «две ткани у одного колорвея» НЕВЫРАЗИМЫМ на уровне
-- схемы — то есть переводит инвариант из разряда «проверяется пробой» в разряд «не может быть
-- записан». Гонки двух транзакций он не чинит (её и нет: пишущие транзакции идут SERIALIZABLE,
-- диапазонные блокировки плюс повтор после дедлока их сериализуют, последний писатель побеждает) —
-- он страхует БУДУЩЕГО писателя, который про кражу не будет знать.
--
-- Никаких ADD CONSTRAINT … CHECK — общее правило полосы: поздний CHECK копирует таблицу целиком и
-- проверяет всю историю.
--
-- FK-ПОЛИТИКА: ON DELETE SET NULL, ровно как у design_picture.colorway_id (0356) и по тому же
-- доводу: ассет — АРТЕФАКТ карточки, а не собственность колорвея. Удалили колорвей — плитка
-- остаётся тканью карточки, просто ничьей. RESTRICT сделал бы полку причиной, по которой колорвей
-- нельзя удалить; CASCADE снёс бы ткань, которой пользуются другие прогоны и другие цвета.
--
-- ⚠ И ЭТО ЧЕТВЁРТАЯ ТАБЛИЦА ПОЛОСЫ, ССЫЛАЮЩАЯСЯ НА product(id). Урок 0356 применён сразу:
-- вердикт удаления колорвея (readColorwayDeletionFacts / ClassifyColorwayDeletion) СЧИТАЕТ и эту
-- колонку и называет её оператору отдельной строкой сирот. Сетка безопасности по MySQL 1451
-- поймать её не может по построению — она видит только RESTRICT.
--
-- ЦЕНА. ADD COLUMN … NULL в конец — INSTANT (8.0.12+), ADD INDEX и ADD UNIQUE — INPLACE.
--
-- ⚠ ADD FOREIGN KEY — ЭТО КОПИЯ ТАБЛИЦЫ, А НЕ СКАН, и первая редакция этой строки называла его
-- сканом. При включённых foreign_key_checks (дефолт раннера — sql-migrate их не выключает) MySQL 8
-- ведёт этот ALTER алгоритмом COPY; INPLACE он даёт ему только при выключенных проверках. Это уже
-- записано в шапке 0330 и обязано читаться здесь так же — иначе фраза «валидирует сканом»
-- переживёт этот релиз и будет процитирована как общее обещание безопасности на живой таблице,
-- которого она не даёт.
--
-- ЗДЕСЬ ЭТО БЕСПЛАТНО ПО РАЗМЕРУ, А НЕ ПО АЛГОРИТМУ: design_asset на бете — единицы строк, на
-- проде таблицы нет вовсе (прод стоит до 0340), копия одноразовая и миллисекундная, а валидировать
-- ей нечего — колонка рождается NULL на каждой строке.
--
-- ЯВНОГО ALGORITHM/LOCK ЗДЕСЬ НЕТ, И ЭТО РЕШЕНИЕ. `ALGORITHM=INPLACE` рядом с ADD FOREIGN KEY при
-- включённых проверках не ускорит оператор, а ОТКЛОНИТ его (ER_ALTER_OPERATION_NOT_SUPPORTED), то
-- есть превратит рабочую миграцию в жёсткий стоп деплоя; `ALGORITHM=INSTANT` у ADD COLUMN был бы
-- честным утверждением, но у раннера нет отката на медленный путь, и сервер, который INSTANT не
-- даёт, остановил бы старт вместо того, чтобы применить колонку дороже. Алгоритм здесь — факт,
-- который надо ЗНАТЬ, а не команда, которую надо ОТДАТЬ.
--
-- Четыре ALTER'а вместо одного: смешение INSTANT и INPLACE в одном операторе теряет INSTANT
-- (урок 0349). Про метаданные замки и предел, за которым это перестаёт быть бесплатным, — см.
-- шапку 0356 (R2).
--
-- ОКНО КАТЯЩЕГОСЯ ДЕПЛОЯ — то же, что у 0356 (R1), и по той же причине пустое: старый бинарь
-- колонку не называет, MySQL кладёт NULL, а NULL здесь значит «ничья ткань» — законное вечное
-- состояние, в котором рождается каждая существующая строка полки. Опасным окно станет ровно
-- тогда же: второй инстанс, фоновый писатель полки либо долгий откат.
--
-- ИДЕМПОТЕНТНОСТЬ: каждый шаг гейтится по information_schema, PREPARE / EXECUTE / DEALLOCATE —
-- каждый своей строкой (прод ходит без multiStatements, а контейнерный тест эту поломку
-- маскирует).

-- +migrate Up

SET @da_cw := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'design_asset'
      AND COLUMN_NAME = 'colorway_id');
SET @ddl := IF(@da_cw = 0,
    'ALTER TABLE design_asset
        ADD COLUMN colorway_id INT NULL
            COMMENT ''FK product(id): ЧЬЯ это ткань. NULL = ничья (ткань карточки без носителя). Пишется ТОЛЬКО глаголом SetDesignAssetColorway; UpsertAsset колонку не трогает''',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @da_cw_idx := (SELECT COUNT(*) FROM information_schema.STATISTICS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'design_asset'
      AND INDEX_NAME = 'idx_design_asset_colorway');
SET @ddl := IF(@da_cw_idx = 0,
    'ALTER TABLE design_asset ADD KEY idx_design_asset_colorway (colorway_id)',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @da_cw_uq := (SELECT COUNT(*) FROM information_schema.STATISTICS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'design_asset'
      AND INDEX_NAME = 'uq_design_asset_colorway');
SET @ddl := IF(@da_cw_uq = 0,
    'ALTER TABLE design_asset ADD UNIQUE KEY uq_design_asset_colorway (tech_card_id, colorway_id)',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @da_cw_fk := (SELECT COUNT(*) FROM information_schema.TABLE_CONSTRAINTS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'design_asset'
      AND CONSTRAINT_NAME = 'fk_design_asset_colorway' AND CONSTRAINT_TYPE = 'FOREIGN KEY');
SET @ddl := IF(@da_cw_fk = 0,
    'ALTER TABLE design_asset
        ADD CONSTRAINT fk_design_asset_colorway FOREIGN KEY (colorway_id)
            REFERENCES product(id) ON DELETE SET NULL',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- +migrate Down
--
-- НАЗНАЧЕНИЯ ТЕРЯЮТСЯ ВМЕСТЕ СО СВОИМ СМЫСЛОМ, и это осознанно: одноосная полка не выражает
-- «ткань колорвея N». САМИ АССЕТЫ НЕ УДАЛЯЮТСЯ — плитка с раппортом и именем остаётся полноценной
-- тканью карточки, ровно той, какой была до этой волны. Порядок: FK, индекс, колонка — FK не
-- живёт без индекса. Каждый шаг под гейтом, чтобы повтор отката, упавшего после DROP COLUMN, не
-- заклинил на «Unknown column».

SET @da_fk_down := (SELECT COUNT(*) FROM information_schema.TABLE_CONSTRAINTS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'design_asset'
      AND CONSTRAINT_NAME = 'fk_design_asset_colorway' AND CONSTRAINT_TYPE = 'FOREIGN KEY');
SET @ddl := IF(@da_fk_down = 1,
    'ALTER TABLE design_asset DROP FOREIGN KEY fk_design_asset_colorway',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @da_uq_down := (SELECT COUNT(*) FROM information_schema.STATISTICS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'design_asset'
      AND INDEX_NAME = 'uq_design_asset_colorway');
SET @ddl := IF(@da_uq_down = 1,
    'ALTER TABLE design_asset DROP INDEX uq_design_asset_colorway',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @da_idx_down := (SELECT COUNT(*) FROM information_schema.STATISTICS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'design_asset'
      AND INDEX_NAME = 'idx_design_asset_colorway');
SET @ddl := IF(@da_idx_down = 1,
    'ALTER TABLE design_asset DROP INDEX idx_design_asset_colorway',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @da_cw_down := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'design_asset'
      AND COLUMN_NAME = 'colorway_id');
SET @ddl := IF(@da_cw_down = 1,
    'ALTER TABLE design_asset DROP COLUMN colorway_id',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;
