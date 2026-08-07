-- +migrate Up

-- НАСТИЛ (Ф4). Упорядоченный список секций (маркер, слои) на пару (колорвей, слот BOM), плюс поля,
-- общие для настила: режим настилания, концевые потери, снимок количеств.
--
-- НОМЕР. Спека Ф4 планировала 0290-0292, оставив запас на случай, что Ф3 и Ф6 дорастут на файл-другой.
-- Они доехали и не доросли: последняя применённая миграция — 0280_tech_card_pattern_size_index.sql,
-- поэтому Ф4 берёт первые по-настоящему свободные номера 0281-0283. sql-migrate ключует миграцию
-- ПОЛНЫМ ИМЕНЕМ ФАЙЛА, значит переименовать этот файл после применения будет уже нельзя (память
-- проекта: «unknown migration in database» вешает старт) — номер выбирается один раз и навсегда.
--
-- ПОЧЕМУ ЭТО НЕ ПОВТОР 0119. Та таблица умерла (0243:3-9) от четырёх причин, и три из них лечатся
-- не схемой: источник геометрии (наш движок вместо импорта из чужого САПР), существование
-- редактора (Ф4.3, без него эту миграцию не заводить) и отдельный RPC. Схема отвечает за
-- четвёртую: у строк есть ПРОВОДНАЯ ИДЕНТИЧНОСТЬ (lay_key / section_key), поэтому сохранение
-- диффится по ключу, а не заменяет всё подряд. Идентичность — client-minted ULID, а НЕ
-- (colorway_id, bom_item_id): у прогона законно два настила на одну пару (разные размерные блоки),
-- и оба поля — редактируемые АТРИБУТЫ строки. Ровно та же развилка, что 0230:14-20.
--
-- ПОЧЕМУ colorway_id — RESTRICT, А НЕ SET NULL. 0264:26-32 выбрал SET NULL для МАРКЕРА и честно
-- назвал это сделкой: удаление колорвея молча превращает специфичный маркер в общий. Маркер —
-- измерение, и потеря атрибуции стоит атрибуции. Настил — ПОТРЕБНОСТЬ: колорвей входит в его
-- идентичность, и настил, «принадлежащий всем цветам», не недоатрибутирован, а неверен. FK без
-- ON DELETE (то есть RESTRICT) зеркалит fk_prl_product на строке прогона (0110:28) — прогон уже
-- сегодня запрещает удалить продукт, который он планирует.
--
-- ПОЧЕМУ bom_item_id — SET NULL, И ЧЕМ ЭТО ОПЛАЧЕНО. RESTRICT здесь означал бы, что карточка
-- НАВСЕГДА теряет право удалить строку BOM, с которой хоть раз кроили: BOM апсерт-диффится ВНУТРИ
-- UpdateTechCard, и отказ ронял бы всё сохранение карточки (0257:17-21 — тот же довод, там он
-- решил ту же развилку так же). Цена SET NULL — настил, потерявший слот. Она оплачена
-- НЕ-NULL-снимком bom_line_key: настил всегда может НАЗВАТЬ пропавший слот, помечается как
-- BROKEN, выпадает из потребности и покрытия с явной находкой и никогда не молчит. Тихое
-- расширение, которым опасен SET NULL у 0264, здесь невозможно.
--
-- ПОЧЕМУ qty_snapshot — JSON, ХОТЯ 0273 ВЫБРАЛА ДОЧЕРНЮЮ ТАБЛИЦУ. Довод 0273 (0273:19-26) состоял
-- в том, что Ф4.5/Ф6.2 обязаны ДЖОЙНИТЬСЯ к составу. Снимок не джойнится ни разу: его целиком
-- читают и целиком сравнивают с текущими строками прогона в Go. Заводить ради этого таблицу с FK
-- на size — платить за индексы и каскады, которыми никто не пользуется. Ловушка закавыченного
-- JSON-скаляра (UnquoteLegacyComposition) сюда не приезжает: колонку читают только явным списком
-- колонок, SELECT * в этом сторе запрещён (markers.go:31-33).
--
-- ИДЕМПОТЕНТНОСТЬ (CLAUDE.md): CREATE TABLE IF NOT EXISTS со всеми ограничениями inline и
-- ИМЕНОВАННЫМИ. Никакого CHARSET-клауза (прецедент 0252/0257): прод и бета живут на серверном
-- utf8mb3, контейнерные тесты подключаются utf8mb4, явное указание развело бы их незаметно.

CREATE TABLE IF NOT EXISTS production_run_lay (
    id              INT PRIMARY KEY AUTO_INCREMENT,
    run_id          INT NOT NULL,
    lay_key         CHAR(26) NOT NULL COMMENT 'stable client-minted ULID identity; the keyed diff matches on it',
    colorway_id     INT NOT NULL COMMENT 'product(id) — тот же идентификатор колорвея, что у строки прогона и у маркера (0151)',
    bom_item_id     INT NULL COMMENT 'слот ткани; NULL = слот удалён из BOM (SET NULL), настил BROKEN — см. bom_line_key',
    bom_line_key    CHAR(26) NOT NULL COMMENT 'СНИМОК line_key слота на момент создания; существует только чтобы НАЗВАТЬ пропавший слот в сообщении, ключом поиска не является',
    name            VARCHAR(191) NOT NULL DEFAULT '' COMMENT 'как цех называет настил; пусто = безымянный',
    mode            VARCHAR(16) NOT NULL COMMENT 'режим настилания: face_up | face_to_face',
    end_loss_cm     DECIMAL(6,2) NOT NULL DEFAULT 0 COMMENT 'концевые потери на ОДИН КОНЕЦ ОДНОГО СЛОЯ, см; полные потери = 2 × end_loss_cm × Σ слоёв',
    qty_snapshot    JSON NOT NULL COMMENT 'снимок [{size_id, qty}] строк прогона этого колорвея на момент ПОСТРОЕНИЯ; расхождение с текущими = бейдж «устарел». Пишет только сервер',
    note            VARCHAR(512) NULL,
    display_order   INT NOT NULL DEFAULT 0,
    lock_version    INT NOT NULL DEFAULT 0 COMMENT 'оптимистическая блокировка настила; каждое сохранение +1',
    created_by      VARCHAR(255) NOT NULL DEFAULT '',
    updated_by      VARCHAR(255) NOT NULL DEFAULT '',
    created_at      TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at      TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    -- Словарь закрыт И по написанию, И по регистру — прецедент 0275 (REGEXP в MySQL нечувствителен
    -- к регистру, поэтому 'Face_Up' прошёл бы и развёл бы Go-сравнение со строкой).
    CONSTRAINT chk_prlay_mode CHECK (
        mode REGEXP '^(face_up|face_to_face)$'
        AND STRCMP(CAST(mode AS BINARY), CAST(LOWER(mode) AS BINARY)) = 0),
    CONSTRAINT chk_prlay_end_loss CHECK (end_loss_cm >= 0 AND end_loss_cm <= 100),
    CONSTRAINT uniq_prlay_key UNIQUE (run_id, lay_key),
    CONSTRAINT fk_prlay_run FOREIGN KEY (run_id) REFERENCES production_run (id) ON DELETE CASCADE,
    CONSTRAINT fk_prlay_colorway FOREIGN KEY (colorway_id) REFERENCES product (id),
    CONSTRAINT fk_prlay_bom FOREIGN KEY (bom_item_id) REFERENCES tech_card_bom_item (id) ON DELETE SET NULL,
    INDEX idx_prlay_pair (run_id, colorway_id, bom_item_id)
) ENGINE=InnoDB COMMENT 'НАСТИЛ (Ф4): секции × слои на пару (колорвей, слот BOM) одного прогона';

CREATE TABLE IF NOT EXISTS production_run_lay_section (
    id           INT PRIMARY KEY AUTO_INCREMENT,
    lay_id       INT NOT NULL,
    section_key  CHAR(26) NOT NULL COMMENT 'stable client-minted ULID identity внутри настила; диф матчится на нём, а НЕ на position — порядок это редактируемый атрибут',
    marker_id    INT NOT NULL COMMENT 'РАСКРОЙНЫЙ маркер этого прогона (tech_card_marker.run_id = настилов run_id); карточный маркер секцией быть не может — см. 0282',
    plies        INT NOT NULL COMMENT 'слоёв в секции',
    position     INT NOT NULL DEFAULT 0 COMMENT 'порядок секции в настиле',
    CONSTRAINT chk_prlays_plies CHECK (plies >= 1 AND plies <= 500),
    CONSTRAINT uniq_prlays_key UNIQUE (lay_id, section_key),
    CONSTRAINT fk_prlays_lay FOREIGN KEY (lay_id) REFERENCES production_run_lay (id) ON DELETE CASCADE,
    -- CASCADE, а не RESTRICT, И ЭТО ВЫНУЖДЕННО. DeleteProductionRun не транзакционен и удаляет одну
    -- строку, полагаясь на каскады (productionrun.go:390-433). Порядок обхода каскадов InnoDB не
    -- специфицирован, а маркер прогона тоже каскадится от прогона (0282) — RESTRICT здесь сделал бы
    -- удаление прогона падающим или нет в зависимости от того, что движок обошёл раньше. Молчаливое
    -- усыхание настила от удаления маркера ловится ПРИЛОЖЕНИЕМ: DeleteTechCardMarker отказывает,
    -- если маркер занят секцией. Тот же размен, что Ф3 сделала для эксклюзивности нормы: инвариант
    -- держит транзакция, а не индекс, потому что индекс переносит отказ на того, кто ничего не может
    -- сделать.
    CONSTRAINT fk_prlays_marker FOREIGN KEY (marker_id) REFERENCES tech_card_marker (id) ON DELETE CASCADE,
    INDEX idx_prlays_marker (marker_id)
) ENGINE=InnoDB COMMENT 'Секция настила: маркер × слои (Ф4). Простой настил = одна секция, ступенчатый = несколько';

-- +migrate Down

-- ГВАРД ПЕРВЫМ, ДО ЛЮБОГО DROP (прецедент 0273 Down). Настилы — рукописный план цеха, и снести их
-- откатом молча значит потерять работу человека под видом миграции. SIGNAL непреобразуем в
-- prepared-протоколе (проверено на 8.0.46), поэтому отказ — обращение к НЕСУЩЕСТВУЮЩЕЙ КОЛОНКЕ,
-- чьё имя и есть сообщение: ERROR 1054, детерминированно, без единого DDL. Идентификатор режется
-- на 64 символах, поэтому текст короткий и ASCII.
--
-- Проверка существования таблицы обязательна: файл должен быть перезапускаем после частичного
-- отката, а COUNT(*) по уже дропнутой таблице — это 1146, то есть отказ по неверной причине.
SET @have := (SELECT COUNT(*) FROM information_schema.TABLES
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'production_run_lay');
SET @ddl := IF(@have = 0, 'SELECT 0 INTO @blocking',
    'SELECT COUNT(*) INTO @blocking FROM production_run_lay');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @ddl := IF(@blocking = 0, 'SELECT 1',
    CONCAT('SELECT `0281 Down blocked: ', @blocking, ' lays would be destroyed`'));
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

DROP TABLE IF EXISTS production_run_lay_section;
DROP TABLE IF EXISTS production_run_lay;
