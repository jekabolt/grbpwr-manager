-- Полоса DESIGN, круг 12 (L-2/L-3): ОСЬ КОЛОРВЕЯ у рендеров.
--
-- МОДЕЛЬ ВЛАДЕЛЬЦА, дословно: «флеты одна разметка … у фабрик рендера должно быть так 1 колорвей
-- там должно быть мультивью которое мы генерим + из его нарезаем сплитом стороны размеченные и на
-- каждый колорвей так и потом мы в 3д рендере уже выбираем колорвей который будем рендерить».
-- То есть: флэт колорвея НЕ ИМЕЕТ ПО СУЩЕСТВУ (чертёж изделия один на все цвета), фабрик-рендер —
-- НА КОЛОРВЕЙ, и 3D выбирает, ЧЕЙ верстак рендерить.
--
-- ЧТО ДОБАВЛЯЕТСЯ: NULLable `colorway_id` у картинки, у слота верстака и у прогона. Колорвей после
-- 0151 — это строка `product` (fk_tccu_colorway_product и соседи уже ссылаются так же), поэтому
-- FK здесь идут в product(id), знаковым INT — ровно тем типом, что у product.id (0001).
--
-- ПОЧЕМУ NULL, А НЕ NOT NULL DEFAULT 0. У NULL здесь ДВА честных смысла, и оба нужны:
--   * у флэта колорвея НЕТ ПО СУЩЕСТВУ — и Go-сторожа не дают флэту получить значение вовсе
--     (состояние «флэт с колорвеем» невыразимо через все двери записи);
--   * у рендера, записанного ДО этой оси, колорвей НЕ АТРИБУТИРОВАН. Дозаписать его «первым
--     колорвеем карточки» значило бы вписать в летопись догадку: те рендеры генерились из
--     РЕЦЕПТА цвета (params.colour: код|hex|слова|фото), который может не совпадать ни с одной
--     строкой колорвеев. Ложная, но правдоподобная атрибуция — ровно тот класс дефекта, от
--     которого полоса уже дважды отказывалась (история — свидетельство, а не заявление).
--   Читатель различает эти два смысла ПАРОЙ (kind, colorway_id): NULL при kind='flat' — «нет по
--   существу», NULL при kind='render'|'threed' — «до оси / не атрибутирован».
--
-- ЭКСКЛЮЗИВНОСТЬ ВЕРСТАКА НЕ ПЕРЕСТРАИВАЕТСЯ, и это решение, а не экономия. Ключ
-- uq_design_bench_view (tech_card_id, kind, exclusive_key) остаётся как есть; колорвей входит в
-- САМ exclusive_key У СИЛУЭТНЫХ СТОРОН (entity.DesignBenchExclusiveKey: `front` для флэта и
-- легаси, `front@cw:<id>` для колорвейного слота).
--
-- ⚠ У ДЕТАЛЕЙ — НЕТ, И ЭТО НЕ ИСКЛЮЧЕНИЕ ИЗ ПРАВИЛА, А ЕГО ПРИМЕНЕНИЕ. Ключ детали остаётся
-- минтованным `detail:<uuid>` (createDetailSlot), потому что uuid ЭКСКЛЮЗИВЕН САМ ПО СЕБЕ:
-- колорвей ему для единственности не нужен, а две детали, названные человеком одинаково, обязаны
-- остаться двумя слотами. Колорвей у детали живёт ТОЛЬКО в колонке — то есть у деталей колонка не
-- «читаемая половина того же факта», а единственный носитель. Сказано здесь потому, что шапка
-- переживает код, а bench.go признаёт это лишь у самого места. Три довода за сам приём:
--   * NULLable колонка в UNIQUE-ключе НЕ ограничивает ничего: MySQL считает NULL != NULL, и все
--     флэтовые слоты (colorway NULL) потеряли бы единственность адреса разом;
--   * exclusive_key И ЗАДУМАН как строка, называющая домен эксклюзивности («что ровно одно на
--     карточке», 0341) — деталь уже кодирует туда свой uuid; колорвей — тот же приём;
--   * ключ не перестраивается вовсе, значит нет ни окна рассогласования, ни INPLACE-цены, и
--     разбор 1062 по имени ключа (bench.go/mysqlDupKey) не трогается.
--   Колонка colorway_id при этом ХРАНИТСЯ РЯДОМ — как view_key хранится рядом со своим же
--   exclusive_key: строковый ключ адресует, колонка отвечает на вопросы чтения. Оба пишутся одним
--   писателем из одного значения в одном INSERT и после рождения строки не меняются.
--
-- FK-ПОЛИТИКА (в терминах шапки 0340):
--   * design_picture.colorway_id → product(id) ON DELETE SET NULL: картинка — оплаченный артефакт
--     и колорвей не переживать не обязана; после удаления колорвея она честно становится
--     неатрибутированной. RESTRICT сделал бы полосу причиной, по которой колорвей нельзя удалить;
--     CASCADE стёр бы оплаченную историю.
--   * design_bench_slot.colorway_id → product(id) ON DELETE CASCADE: слот — АДРЕС, а не артефакт.
--     SET NULL оставил бы строку с ключом вида `front@cw:5`, недостижимую ни одним адресом
--     (colorway 5 больше нет) — вечный призрак в выдаче. Плиту слот при этом не уносит:
--     design_picture от слота не зависит.
--   * design_run.colorway_id → product(id) ON DELETE SET NULL: строка истории не удаляется
--     никогда; замороженные params при этом навсегда помнят, с каким id её запускали.
--   product от tech_card НЕ каскадится (0093: SET NULL), поэтому ни один из трёх FK не ложится
--   на путь DeleteTechCard и 1451 в единственном глаголе удаления карточки не появляется.
--   В реестр GetMediaUsage эти колонки не входят: они ссылаются на product(id), не на media(id).
--
-- ЦЕНА. ADD COLUMN … NULL в конец таблицы — INSTANT (8.0.12+). ADD INDEX — INPLACE.
--
-- ⚠ ADD FOREIGN KEY — ЭТО КОПИЯ ТАБЛИЦЫ ЦЕЛИКОМ, А НЕ СКАН, и первая редакция этой строки
-- называла его сканом («валидирует существующие строки скана таблицы»). При включённых
-- foreign_key_checks — а дефолт раннера именно такой, sql-migrate их не выключает — MySQL 8 ведёт
-- ADD FOREIGN KEY алгоритмом COPY; INPLACE он даёт только при выключенных проверках. Это уже
-- записано своими словами в шапке 0330 и обязано читаться одинаково во всех трёх файлах. Разница
-- не академическая: «скан» звучит как обещание, которое можно процитировать на живой таблице, а
-- копия таблицы под метаданным замком таким обещанием не является НИКОГДА.
--
-- ЭТОТ ФАЙЛ КОПИРУЕТ ТРИ ТАБЛИЦЫ (design_picture, design_bench_slot, design_run). Бесплатно это
-- по РАЗМЕРУ, а не по алгоритму: на бете все три исчисляются десятками строк, на проде их нет
-- вовсе (прод стоит до 0340), и валидировать копии нечего — колонка рождается NULL на каждой
-- строке. Пятиминутный потолок прогона (store.go) не рядом. Предел, за которым это перестаёт быть
-- верным, — в R2 ниже.
--
-- ЯВНОГО ALGORITHM/LOCK ЗДЕСЬ НЕТ, И ЭТО РЕШЕНИЕ, А НЕ ПРОПУСК. `ALGORITHM=INPLACE` рядом с
-- ADD FOREIGN KEY при включённых проверках не ускоряет оператор, а ОТКЛОНЯЕТ его
-- (ER_ALTER_OPERATION_NOT_SUPPORTED) — то есть превращает рабочую миграцию в жёсткий стоп деплоя.
-- `ALGORITHM=INSTANT` у ADD COLUMN был бы честным утверждением, но у раннера нет отката на
-- медленный путь: сервер, который INSTANT не даёт, остановил бы старт вместо того, чтобы применить
-- колонку дороже. Алгоритм здесь — факт, который надо ЗНАТЬ, а не команда, которую надо ОТДАТЬ.
--
-- Колонка, индекс и FK идут ОТДЕЛЬНЫМИ ALTER: смешение INSTANT и INPLACE в одном операторе теряет
-- INSTANT (урок 0349).
--
-- ── R2. ШЕСТЬ ALTER × ТРИ ТАБЛИЦЫ, И У КАЖДОГО СВОЙ МЕТАДАННЫЙ ЗАМОК ──
--
-- Этот файл берёт метаданный замок ДЕВЯТЬ раз (колонка + индекс + FK на каждой из трёх таблиц), и
-- каждый из них ждёт завершения ВСЕХ открытых транзакций, читающих или пишущих эту таблицу.
-- Потолок у прогона миграций один на весь файл — 5 минут, захардкожено в store.go (context с
-- таймаутом вокруг MigrateWithContext), — и он НЕ на оператор, а на всё вместе, включая соседей
-- по очереди. Ждущий метаданный замок при этом БЛОКИРУЕТ И НОВЫХ читателей, то есть очередь
-- растёт быстрее, чем рассасывается.
--
-- СЕГОДНЯ ЭТО БЕСПЛАТНО, И ЭТО ФАКТ О ДАННЫХ, А НЕ О КОДЕ: на бете десятки строк (порядка восьми
-- слотов верстака), на проде трёх таблиц нет вовсе, приложение при старте единственное и полосу
-- никто не читает. Валидация FK — скан таблицы целиком; на десятках строк он неизмерим.
--
-- КОГДА ПЕРЕСТАНЕТ БЫТЬ БЕСПЛАТНЫМ — два независимых предела, и достаточно любого:
--   * ОБЪЁМ. Валидация FK и построение индекса линейны по строкам. Пока design_picture держит
--     десятки тысяч строк, каждый ALTER — секунды, и девять таких в 5 минут укладываются с
--     запасом. На СОТНЯХ ТЫСЯЧ строк (карточка с историей генераций живёт годами и растёт
--     монотонно — кадры не удаляются, а прячутся) счёт идёт на десятки секунд за оператор, и
--     файл целиком подходит к потолку. Порог, за которым эту миграцию надо резать на отдельные
--     файлы по таблице: ~200k строк в design_picture. Считать так:
--         SELECT COUNT(*) FROM design_picture;
--   * ЖИВОЙ ЧИТАТЕЛЬ. Как только полосу читает работающее приложение (несколько инстансов, либо
--     деплой без окна), метаданный замок начинает ЖДАТЬ чужую транзакцию, и предел определяется
--     уже не числом строк, а самым долгим открытым запросом. Признак того, что порог перейдён:
--     на бете больше одного инстанса, либо у прода появилась вторая реплика.
-- Ни того ни другого сегодня нет. Когда появится — резать на файлы по одной таблице (каждый файл
-- получает СВОИ 5 минут) и ставить деплой в окно; ускорить сам ALTER нечем.
--
-- НИ ОДНОГО ADD CONSTRAINT … CHECK — общее правило полосы: поздний CHECK копирует таблицу и
-- проверяет всю историю; словарь и взаимные запреты стерегут Go-сторожа, называющие карточку и
-- значение.
--
-- ── R1. ОКНО КАТЯЩЕГОСЯ ДЕПЛОЯ И ОТКАТА: СТАРЫЙ БИНАРЬ НА НОВОЙ СХЕМЕ ──
--
-- Схема едет ВПЕРЁД БИНАРЯ по построению (automigrate на старте), и между «колонка есть» и «весь
-- трафик обслуживает новый код» существует окно. В нём строки пишет СТАРЫЙ бинарь, чей INSERT
-- колонки colorway_id не называет ВОВСЕ, — и MySQL кладёт NULL. Точно то же окно открывает откат:
-- миграция вниз не запускается (DO катит предыдущий образ на ТОЙ ЖЕ схеме), старый бинарь
-- продолжает писать по колонке, которой не знает.
--
-- ЧТО ИМЕННО ЗАПИСАЛОСЬ БЫ. Не мусор и не ложь: NULL — законное, вечное состояние обеих колонок,
-- читаемое как «не атрибутирован» (см. довод про NULL выше). То есть кадры и слоты, рождённые в
-- окне, окажутся на БЕЗКОЛОРВЕЙНОМ верстаке — ровно там, где живёт вся история до этой оси. Ни
-- один сторож при этом не обходится: невыразимого состояния («флэт с колорвеем») старый бинарь
-- создать не может, потому что он не пишет колорвей ни у чего.
--
-- ПОЧЕМУ ОКНО СЕГОДНЯ ПУСТОЕ, И ЭТО ФАКТ О СРЕДЕ, А НЕ О КОДЕ:
--   * НА ПРОДЕ ЭТИХ ТАБЛИЦ НЕТ ВОВСЕ — прод стоит до 0340, полоса DESIGN туда не уезжала. Писать
--     в окне физически нечему.
--   * БЕТА — ОДИН ИНСТАНС. Старый и новый бинарь там не сосуществуют: DO поднимает новый,
--     дожидается readyz и гасит старый; пересечение — секунды, и в них старый бинарь уже не
--     принимает трафик.
--   * ПОЛОСУ НА БЕТЕ ПИШЕТ ОДИН ЧЕЛОВЕК РУКАМИ — новых СТРОК ПРОГОНА в окне не появляется.
--
-- ⚠ НО «ОКНО ПУСТОЕ» — СЛИШКОМ СИЛЬНО, И ВОТ ЧЕМ ИМЕННО. Воркер действительно не заводит прогоны,
-- зато он ИХ ЗАКРЫВАЕТ, а закрытие РОЖДАЕТ строки design_picture. Прогон, заведённый НОВЫМ
-- бинарём с колорвеем 5 и подхваченный СТАРЫМ воркером (лизинг переживает рестарт, а на бете
-- воркер и API — один процесс лишь потому, что инстанс один), будет закрыт INSERT'ом, который
-- колонку colorway_id не называет: кадры оплаченного колорвейного прогона родятся
-- НЕАТРИБУТИРОВАННЫМИ, лягут на безколорвейный верстак, и отличить их потом не по чему — строка
-- прогона будет говорить «колорвей 5», а её кадры «ничей». Сегодня этого не случается только
-- потому, что инстанс ОДИН и старого воркера в окне не существует; как только их станет два, этот
-- путь откроется РАНЬШЕ всех прочих, потому что ему не нужен ни новый жест человека, ни фоновый
-- писатель — достаточно уже висящего задания.
--
-- ЧТО СДЕЛАЕТ ОКНО ОПАСНЫМ ПОЗЖЕ — любое из трёх, и каждое наступает независимо:
--   * ВТОРОЙ ИНСТАНС (беты или прода). Тогда старый и новый код пишут ОДНОВРЕМЕННО и минутами, и
--     часть колорвейных рендеров тихо родится неатрибутированной — то есть попадёт в чужой
--     (безколорвейный) верстак и в чужой 3D-прогон, за который заплачено. Молча: строка законна.
--   * ФОНОВЫЙ ПИСАТЕЛЬ полосы (импорт, сидер, автогенерация по расписанию) — он не ждёт готовности
--     нового бинаря и наполняет окно строками сам.
--   * ОТКАТ НА ДОЛГО. Откат на до-0356 образ оставляет схему новой, и пока он стоит, КАЖДАЯ новая
--     строка неатрибутирована. Разобрать их потом нельзя ничем: у кадра нет признака, по которому
--     «не назвали» отличается от «родился в окне» (эта миграция намеренно не бэкфиллит колорвей —
--     догадка в летописи хуже пробела).
-- Когда любое из трёх появится — правильный приём тот же, что у остальных двухфазных изменений:
-- выкатить читающий код РАНЬШЕ пишущего, и только следующим релизом начать писать колонку.
--
-- ИДЕМПОТЕНТНОСТЬ. MySQL коммитит DDL пооператорно; файл, упавший в середине, не получает строки
-- в gorp_migrations и на следующем старте идёт с начала. Каждый шаг гейтится по
-- information_schema (COLUMNS для колонок, STATISTICS для индексов, TABLE_CONSTRAINTS для FK).
-- PREPARE / EXECUTE / DEALLOCATE — каждый своей строкой: прод ходит без multiStatements, а
-- контейнерный тест эту поломку маскирует.

-- +migrate Up

-- ── 1. design_picture.colorway_id ──────────────────────────────────────────────────────────────

SET @dp_cw := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'design_picture'
      AND COLUMN_NAME = 'colorway_id');
SET @ddl := IF(@dp_cw = 0,
    'ALTER TABLE design_picture
        ADD COLUMN colorway_id INT NULL
            COMMENT ''FK product(id), колорвей после 0151. NULL у флэта = колорвея нет по существу (Go не даёт флэту значение); NULL у рендера/3D = кадр до оси либо колорвей удалён''',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @dp_cw_idx := (SELECT COUNT(*) FROM information_schema.STATISTICS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'design_picture'
      AND INDEX_NAME = 'idx_design_picture_colorway');
SET @ddl := IF(@dp_cw_idx = 0,
    'ALTER TABLE design_picture ADD KEY idx_design_picture_colorway (colorway_id)',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @dp_cw_fk := (SELECT COUNT(*) FROM information_schema.TABLE_CONSTRAINTS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'design_picture'
      AND CONSTRAINT_NAME = 'fk_design_picture_colorway' AND CONSTRAINT_TYPE = 'FOREIGN KEY');
SET @ddl := IF(@dp_cw_fk = 0,
    'ALTER TABLE design_picture
        ADD CONSTRAINT fk_design_picture_colorway FOREIGN KEY (colorway_id)
            REFERENCES product(id) ON DELETE SET NULL',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- ── 2. design_bench_slot.colorway_id ───────────────────────────────────────────────────────────

SET @dbs_cw := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'design_bench_slot'
      AND COLUMN_NAME = 'colorway_id');
SET @ddl := IF(@dbs_cw = 0,
    'ALTER TABLE design_bench_slot
        ADD COLUMN colorway_id INT NULL
            COMMENT ''FK product(id). ЧЕЙ верстак: NULL = флэтовый либо неатрибутированный легаси-рендер. В адрес входит через exclusive_key (front@cw:<id>), эта колонка — читаемая половина того же факта, оба пишутся одним INSERT''',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @dbs_cw_idx := (SELECT COUNT(*) FROM information_schema.STATISTICS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'design_bench_slot'
      AND INDEX_NAME = 'idx_design_bench_slot_colorway');
SET @ddl := IF(@dbs_cw_idx = 0,
    'ALTER TABLE design_bench_slot ADD KEY idx_design_bench_slot_colorway (colorway_id)',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @dbs_cw_fk := (SELECT COUNT(*) FROM information_schema.TABLE_CONSTRAINTS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'design_bench_slot'
      AND CONSTRAINT_NAME = 'fk_design_bench_slot_colorway' AND CONSTRAINT_TYPE = 'FOREIGN KEY');
SET @ddl := IF(@dbs_cw_fk = 0,
    'ALTER TABLE design_bench_slot
        ADD CONSTRAINT fk_design_bench_slot_colorway FOREIGN KEY (colorway_id)
            REFERENCES product(id) ON DELETE CASCADE',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- ── 3. design_run.colorway_id ──────────────────────────────────────────────────────────────────

SET @dr_cw := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'design_run'
      AND COLUMN_NAME = 'colorway_id');
SET @ddl := IF(@dr_cw = 0,
    'ALTER TABLE design_run
        ADD COLUMN colorway_id INT NULL
            COMMENT ''FK product(id). ДЛЯ КАКОГО КОЛОРВЕЯ прогон: render/recolor генерят его мультивью, threed рендерит его верстак. NULL = роды без оси (flat/vector/pattern/draft_idea) либо прогон до оси''',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @dr_cw_idx := (SELECT COUNT(*) FROM information_schema.STATISTICS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'design_run'
      AND INDEX_NAME = 'idx_design_run_colorway');
SET @ddl := IF(@dr_cw_idx = 0,
    'ALTER TABLE design_run ADD KEY idx_design_run_colorway (colorway_id)',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @dr_cw_fk := (SELECT COUNT(*) FROM information_schema.TABLE_CONSTRAINTS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'design_run'
      AND CONSTRAINT_NAME = 'fk_design_run_colorway' AND CONSTRAINT_TYPE = 'FOREIGN KEY');
SET @ddl := IF(@dr_cw_fk = 0,
    'ALTER TABLE design_run
        ADD CONSTRAINT fk_design_run_colorway FOREIGN KEY (colorway_id)
            REFERENCES product(id) ON DELETE SET NULL',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- +migrate Down
--
-- DOWN ТЕРЯЕТ АТРИБУЦИЮ, И ЭТО ОСОЗНАННО: одноосная схема не выражает «рендер колорвея 5», и
-- колонки снимаются вместе со своим смыслом. Слоты колорвейных верстаков УДАЛЯЮТСЯ ПЕРВЫМИ —
-- их exclusive_key (`front@cw:5`) в одноосном мире не адресуем ничем и остался бы призраком в
-- каждой выдаче. Картинки и прогоны не удаляются: артефакт и история переживают откат, теряя
-- только колонку. Порядок внутри таблицы: FK, индекс, колонка — FK не живёт без индекса.
-- DELETE тоже под гейтом: он именует колонку, которую этот же файл ниже снимает, и повтор
-- отката, упавшего после DROP COLUMN, заклинил бы на «Unknown column».

SET @dbs_cw_down := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'design_bench_slot'
      AND COLUMN_NAME = 'colorway_id');
SET @dml := IF(@dbs_cw_down = 1,
    'DELETE FROM design_bench_slot WHERE colorway_id IS NOT NULL',
    'SELECT 1');
PREPARE stmt FROM @dml;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @dbs_fk_down := (SELECT COUNT(*) FROM information_schema.TABLE_CONSTRAINTS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'design_bench_slot'
      AND CONSTRAINT_NAME = 'fk_design_bench_slot_colorway' AND CONSTRAINT_TYPE = 'FOREIGN KEY');
SET @ddl := IF(@dbs_fk_down = 1,
    'ALTER TABLE design_bench_slot DROP FOREIGN KEY fk_design_bench_slot_colorway',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @dbs_idx_down := (SELECT COUNT(*) FROM information_schema.STATISTICS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'design_bench_slot'
      AND INDEX_NAME = 'idx_design_bench_slot_colorway');
SET @ddl := IF(@dbs_idx_down = 1,
    'ALTER TABLE design_bench_slot DROP INDEX idx_design_bench_slot_colorway',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @dbs_cw_down2 := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'design_bench_slot'
      AND COLUMN_NAME = 'colorway_id');
SET @ddl := IF(@dbs_cw_down2 = 1,
    'ALTER TABLE design_bench_slot DROP COLUMN colorway_id',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @dp_fk_down := (SELECT COUNT(*) FROM information_schema.TABLE_CONSTRAINTS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'design_picture'
      AND CONSTRAINT_NAME = 'fk_design_picture_colorway' AND CONSTRAINT_TYPE = 'FOREIGN KEY');
SET @ddl := IF(@dp_fk_down = 1,
    'ALTER TABLE design_picture DROP FOREIGN KEY fk_design_picture_colorway',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @dp_idx_down := (SELECT COUNT(*) FROM information_schema.STATISTICS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'design_picture'
      AND INDEX_NAME = 'idx_design_picture_colorway');
SET @ddl := IF(@dp_idx_down = 1,
    'ALTER TABLE design_picture DROP INDEX idx_design_picture_colorway',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @dp_cw_down := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'design_picture'
      AND COLUMN_NAME = 'colorway_id');
SET @ddl := IF(@dp_cw_down = 1,
    'ALTER TABLE design_picture DROP COLUMN colorway_id',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @dr_fk_down := (SELECT COUNT(*) FROM information_schema.TABLE_CONSTRAINTS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'design_run'
      AND CONSTRAINT_NAME = 'fk_design_run_colorway' AND CONSTRAINT_TYPE = 'FOREIGN KEY');
SET @ddl := IF(@dr_fk_down = 1,
    'ALTER TABLE design_run DROP FOREIGN KEY fk_design_run_colorway',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @dr_idx_down := (SELECT COUNT(*) FROM information_schema.STATISTICS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'design_run'
      AND INDEX_NAME = 'idx_design_run_colorway');
SET @ddl := IF(@dr_idx_down = 1,
    'ALTER TABLE design_run DROP INDEX idx_design_run_colorway',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @dr_cw_down := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'design_run'
      AND COLUMN_NAME = 'colorway_id');
SET @ddl := IF(@dr_cw_down = 1,
    'ALTER TABLE design_run DROP COLUMN colorway_id',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;
