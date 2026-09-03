-- Полоса DESIGN, круг 15 (J-9): РЕФЕРЕНС РОЛИ `detail` ЗНАЕТ, КАКОЙ ИМЕННО ДЕТАЛИ ОН ПРО.
--
-- ЧТО БЫЛО. Человек выбирает референсу роль `detail`, вписывает имя детали — и клиент делает ДВА
-- НЕСВЯЗАННЫХ ПИСЬМА: ставит роль `detail` в design_reference и заводит ПУСТОЙ слот верстака с
-- этим именем (createDetailSlot принимает минт с picture_id = 0). Строка референса при этом несёт
-- {media_id, role, ordinal, note} и ничего больше, поэтому имя, которое человек только что
-- напечатал, живёт на слоте, а ячейка референса умеет напечатать лишь голое слово `detail`. Две
-- строки об одном жесте — чужие друг другу.
--
-- ПОЧЕМУ ССЫЛКА, А НЕ КОПИЯ ИМЕНИ. Имя детали ПЕРЕИМЕНОВЫВАЕМО, и это его нормальная жизнь
-- (design_bench_slot.detail_name — «presentation only»). Копия имени на строке референса
-- разошлась бы с оригиналом на первом же переименовании и разошлась бы МОЛЧА. Тот же довод уже
-- записан в контракте `DesignRunParams.detail_slot_ids`: «a mutable detail name cannot keep a
-- frozen run's output attached to the same bench address after that detail is renamed, while the
-- slot id can».
--
-- FK ЕСТЬ, И ОН `ON DELETE SET NULL`. Три ветки рассмотрены, выбрана третья:
--   * голый KEY (как у design_reference.media_id) оставил бы указатель в никуда, и ячейка
--     референса печатала бы имя удалённой детали — то есть врала бы уверенно;
--   * CASCADE удалил бы САМ РЕФЕРЕНС вместе со слотом: удаление адреса на верстаке уносило бы
--     картинку из промпта, чего никто не просил;
--   * SET NULL оставляет референс живым и делает его состояние ЧИТАЕМЫМ: роль `detail` без слота —
--     это «деталь была, её адрес удалили», и клиенту есть что сказать вместо выдумки.
-- Довод против голого KEY здесь сильнее, чем у media_id (0347), потому что цель ссылки — ИМЯ, а
-- имя надо ПОКАЗАТЬ человеку; media_id же только упоминается и никогда не рисуется из строки роли.
--
-- ТИП — `INT UNSIGNED`, ПОТОМУ ЧТО design_bench_slot.id ОБЪЯВЛЕН `INT UNSIGNED` (0341). FK в MySQL
-- требует совпадения типа и знаковости; `INT NULL` здесь дал бы 3780 (referencing column
-- incompatible) на пустой базе — то есть падение прогона миграций, а не тихий дефект.
--
-- КАСКАДЫ НЕ ПЕРЕСЕКАЮТСЯ. design_reference уже каскадится от tech_card, и design_bench_slot тоже
-- (0341/0347). Удаление карточки уносит обе таблицы своим собственным каскадом; новый FK лежит
-- ВНУТРИ одной карточки и второго пути к той же строке не создаёт, поэтому 1451 в DeleteTechCard
-- не появляется.
--
-- БЭКФИЛЛА НЕТ, И ЭТО НЕ ПРОПУСК. Связать существующие референсы `detail` со слотами задним числом
-- не по чему: у референса нет ни имени, ни времени минта, ни чего-либо ещё, что назвало бы ОДИН
-- слот из нескольких одноимённых. Старые строки остаются NULL и печатаются как сегодня — голым
-- словом `detail`; человек называет деталь заново, и связь появляется.
--
-- ЦЕНА. ADD COLUMN … NULL в конец — INSTANT (8.0.12+); ADD INDEX и ADD FOREIGN KEY — INPLACE.
-- design_reference — таблица ролей мудборда, десятки строк на карточку.

-- +migrate Up

SET @dr_slot := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'design_reference'
      AND COLUMN_NAME = 'detail_slot_id');
SET @ddl := IF(@dr_slot = 0,
    'ALTER TABLE design_reference
        ADD COLUMN detail_slot_id INT UNSIGNED NULL
            COMMENT ''FK design_bench_slot(id): КАКОЙ ДЕТАЛИ этот референс. Осмысленно только при role=detail; у прочих ролей NULL. NULL при role=detail = слот удалён (SET NULL) либо строка старше этой колонки''',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @dr_slot_idx := (SELECT COUNT(*) FROM information_schema.STATISTICS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'design_reference'
      AND INDEX_NAME = 'idx_design_reference_detail_slot');
SET @ddl := IF(@dr_slot_idx = 0,
    'ALTER TABLE design_reference ADD KEY idx_design_reference_detail_slot (detail_slot_id)',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @dr_slot_fk := (SELECT COUNT(*) FROM information_schema.TABLE_CONSTRAINTS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'design_reference'
      AND CONSTRAINT_NAME = 'fk_design_reference_detail_slot' AND CONSTRAINT_TYPE = 'FOREIGN KEY');
SET @ddl := IF(@dr_slot_fk = 0,
    'ALTER TABLE design_reference
        ADD CONSTRAINT fk_design_reference_detail_slot FOREIGN KEY (detail_slot_id)
            REFERENCES design_bench_slot(id) ON DELETE SET NULL',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- +migrate Down
--
-- Порядок обратный и обязательный: FK, индекс, колонка — FK не живёт без своего индекса, а
-- DROP COLUMN под живым FK отказывает. Каждый шаг под своим гейтом: откат, упавший на середине,
-- обязан доигрываться повтором, а не заклинивать на «Can't DROP; check that it exists».

SET @dr_slot_fk_down := (SELECT COUNT(*) FROM information_schema.TABLE_CONSTRAINTS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'design_reference'
      AND CONSTRAINT_NAME = 'fk_design_reference_detail_slot' AND CONSTRAINT_TYPE = 'FOREIGN KEY');
SET @ddl := IF(@dr_slot_fk_down = 1,
    'ALTER TABLE design_reference DROP FOREIGN KEY fk_design_reference_detail_slot',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @dr_slot_idx_down := (SELECT COUNT(*) FROM information_schema.STATISTICS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'design_reference'
      AND INDEX_NAME = 'idx_design_reference_detail_slot');
SET @ddl := IF(@dr_slot_idx_down = 1,
    'ALTER TABLE design_reference DROP INDEX idx_design_reference_detail_slot',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @dr_slot_down := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'design_reference'
      AND COLUMN_NAME = 'detail_slot_id');
SET @ddl := IF(@dr_slot_down = 1,
    'ALTER TABLE design_reference DROP COLUMN detail_slot_id',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;
