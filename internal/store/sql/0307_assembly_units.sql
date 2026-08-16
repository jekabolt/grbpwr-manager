-- Узлы сборки: у шага появляется ВЫХОДНОЙ УЗЕЛ, а входы шага становятся единым упорядоченным
-- списком «деталь ИЛИ узел».
--
-- Узел — именованный результат сборочного шага (SHELL, FRONT-L, GARMENT). Не строка таблицы и
-- не сущность с id: узел определяется операцией, которая его производит, и ссылаются на него
-- только операции ТОЙ ЖЕ карточки, которые пишутся полной заменой одним запросом.
-- Переименование кода — атомарная перезапись потребителей в том же сохранении, поэтому
-- внешней durable-идентичности узлу не нужно, а код при этом читаем в БД, на печати и в QR.
--
-- ПОЧЕМУ ОДНА ТАБЛИЦА ВХОДОВ, А НЕ ВТОРАЯ РЯДОМ С 0199. Контракт объявляет ОДИН упорядоченный
-- список. Разложенный по двум таблицам и склеиваемый на чтении по общему display_order, он
-- теряет ровно то, ради чего упорядочен: UNIQUE через две таблицы невозможен, и база не может
-- запретить одинаковую позицию у детали и у узла — слияние получает неоднозначный tie и
-- решает его молча. Здесь порядок и дедуп становятся ограничениями схемы, а не обещанием Go.
-- Три UNIQUE ниже работают только благодаря тому, что MySQL пропускает множественные NULL в
-- уникальном индексе; на этом всё и держится.
--
-- ЭТА МИГРАЦИЯ ИНЕРТНА. Go её не читает и не пишет (это следующая задача): колонки NULL у всех
-- существующих строк, новая таблица — копия связей 0199. Окно «схема новая, стор старый»
-- безопасно, потому что полная замена операций сносит строки новой таблицы каскадом
-- (fk_op_input_operation ON DELETE CASCADE) ровно так же, как сносит строки 0199.
--
-- ПОЧЕМУ НИ ОДНОГО CHECK. Ключ узла — открытый авторский код, закрытого словаря у него нет и
-- не будет, а ADD CONSTRAINT CHECK прогоняется по всей истории таблицы и способен уронить старт
-- прода. Форму ключа (trim, длина, непустота при заданном имени, уникальность производителя,
-- отсутствие коллизии с line_key детали) проверяет Go рядом с читаемым сообщением: SQL-CHECK
-- стреляет сырым 3819 и называет не ту колонку — довод шапки 0289.

-- +migrate Up

-- 1. Две колонки узла на операции. NULL = шаг ничего не собирает: это ОБРАБОТКА, её входы
--    остаются доступными следующим шагам. Сегодняшнее состояние каждой строки.
--
--    COLLATE utf8mb4_bin на ключе — не педантизм. Контракт объявляет сравнение ключей
--    ПОБАЙТНЫМ («SHELL» и «Shell» — два разных узла), а коллация таблиц здесь
--    utf8mb4_0900_ai_ci, то есть регистронезависимая. Без _bin Go считал бы два ключа разными,
--    а UNIQUE на входах — одинаковыми: технолог получил бы сырой 1062 вместо валидации, и
--    наоборот — резолв входа-узла в Go не нашёл бы того, что база считает совпадением.
--    Тот же довод, по которому 0306 дал _bin ключам профилей оборудования.
--
--    ADD COLUMN ... NULL на MySQL 8 — INSTANT: истории здесь проверять нечего, и мы её не
--    трогаем.
SET @asm_new := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_operation'
      AND COLUMN_NAME = 'output_unit_key');
SET @ddl := IF(@asm_new = 0,
    -- ИМЯ УЗЛА — ЯВНО utf8mb4, а не «как у таблицы». Ключ рядом получает utf8mb4 через свою
    -- коллацию, а имя без явного указания наследовало бы кодировку ТАБЛИЦЫ — на проде это
    -- utf8mb3, и «рукав 🧵» падал бы сырым `ERROR 1366 Incorrect string value`. Воспроизвести это
    -- на бете невозможно: она в utf8mb4, и там всё пишется.
    'ALTER TABLE tech_card_operation
        ADD COLUMN output_unit_key VARCHAR(64) COLLATE utf8mb4_bin NULL COMMENT ''код узла, который производит шаг (SHELL); NULL = обработка, шаг ничего не собирает. _bin: сравнение ключей побайтное'',
        ADD COLUMN output_unit_name VARCHAR(255) CHARACTER SET utf8mb4 NULL COMMENT ''имя узла; живёт на первом производящем шаге, поглощающие могут не повторять''',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- 2. Единый упорядоченный список входов шага. Ровно одна из колонок piece_id / unit_key
--    непуста — взаимоисключение держит Go, единственный писатель этой таблицы, собирающий
--    список из одной канонической формы.
--
--    unit_key — строковая ссылка на output_unit_key более раннего шага ТОЙ ЖЕ карты, без FK:
--    узел не строка, FK на него невозможен физически. Это тот же дом-паттерн, что line_key, и
--    тот же сознательный отказ от FK, что у штампа раскладки (0291).
--
--    Три UNIQUE:
--      (operation_id, display_order) — позиция в ЕДИНОМ списке; недостижимо для схемы из двух
--                                      таблиц, ради этого таблица и одна;
--      (operation_id, piece_id)      — дедуп деталей, наследует uniq_op_piece из 0199;
--      (operation_id, unit_key)      — дедуп узлов.
--    Все три работают вместе только потому, что MySQL допускает множественные NULL в UNIQUE:
--    строки-детали не мешают друг другу по unit_key, строки-узлы — по piece_id.
--
--    FK на операцию CASCADE (связь — часть операции, а операции пишутся полной заменой), на
--    деталь RESTRICT (деталь, на которую ссылается операция, не должна исчезнуть под спекой;
--    путь записи сперва отвязывает) — ровно как в 0199.
CREATE TABLE IF NOT EXISTS tech_card_operation_input (
    id            INT PRIMARY KEY AUTO_INCREMENT,
    operation_id  INT NOT NULL COMMENT 'FK tech_card_operation(id)',
    piece_id      INT NULL COMMENT 'FK tech_card_piece(id); NULL если вход — узел',
    unit_key      VARCHAR(64) COLLATE utf8mb4_bin NULL COMMENT 'output_unit_key более раннего шага этой карты; NULL если вход — деталь',
    display_order INT NOT NULL COMMENT 'позиция в ЕДИНОМ списке входов шага (детали и узлы вместе)',
    created_at    TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT fk_op_input_operation FOREIGN KEY (operation_id) REFERENCES tech_card_operation(id) ON DELETE CASCADE,
    CONSTRAINT fk_op_input_piece     FOREIGN KEY (piece_id)     REFERENCES tech_card_piece(id)     ON DELETE RESTRICT,
    CONSTRAINT uniq_op_input_order UNIQUE (operation_id, display_order),
    CONSTRAINT uniq_op_input_piece UNIQUE (operation_id, piece_id),
    CONSTRAINT uniq_op_input_unit  UNIQUE (operation_id, unit_key),
    INDEX idx_op_input_unit (unit_key)
) ENGINE = InnoDB COMMENT 'единый упорядоченный список входов операции: деталь ИЛИ узел (объединение с 0199)';

-- 3. Перенос существующих связей из 0199. Позиция берётся из пер-табличного display_order: у
--    легаси-карточек входов-узлов нет, поэтому единый порядок вырождается в сегодняшний, и
--    перенумерация в сквозной ряд произойдёт только при первой осведомлённой записи карточки.
--
--    Идемпотентно через NOT EXISTS, а не INSERT IGNORE: IGNORE проглотил бы заодно и настоящие
--    нарушения FK, превратив испорченную связь в молча пропущенную строку.
--    ПОЗИЦИЯ ПЕРЕНУМЕРОВЫВАЕТСЯ, А НЕ КОПИРУЕТСЯ. Источник (0199) объявляет display_order как
--    `INT NOT NULL DEFAULT 0` и уникальности на пару (operation_id, display_order) НЕ ИМЕЕТ — её
--    держит только код приложения. Приёмник такую пару ЗАПРЕЩАЕТ (uniq_op_input_order выше).
--    Дословное копирование поэтому корректно ровно до первой пары строк одного шага с одинаковой
--    позицией: `ERROR 1062 Duplicate entry '2-0'`, строки в gorp_migrations нет, следующий старт
--    повторяет падение — вечный цикл, причём readyz отвечает 200 от старого процесса, и снаружи
--    это выглядит зависанием, а не отказом.
--
--    ROW_NUMBER делает перенос корректным при ЛЮБЫХ данных, а не при удачных: порядок берётся
--    прежний (display_order), ничья разводится по piece_id — детерминированно, поэтому повторный
--    прогон даёт те же номера. Проверять прод запросом больше не нужно.
--
--    Повторный запуск после падения ФАЙЛА (не этого оператора) безопасен: INSERT ... SELECT
--    атомарен, поэтому полу-вставленного состояния не бывает, а NOT EXISTS отсекает уже
--    перенесённое целиком. Новых строк в источнике между прогонами появиться не может: миграции
--    идут на старте процесса, до того как сервер начинает отвечать, так что писателя в этот
--    момент нет вовсе.
INSERT INTO tech_card_operation_input (operation_id, piece_id, unit_key, display_order, created_at)
SELECT p.operation_id, p.piece_id, NULL,
       ROW_NUMBER() OVER (PARTITION BY p.operation_id ORDER BY p.display_order, p.piece_id) - 1,
       p.created_at
FROM tech_card_operation_piece p
WHERE NOT EXISTS (
    SELECT 1 FROM tech_card_operation_input i
    WHERE i.operation_id = p.operation_id AND i.piece_id = p.piece_id
);

-- +migrate Down

DROP TABLE IF EXISTS tech_card_operation_input;

SET @asm_gone := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_operation'
      AND COLUMN_NAME = 'output_unit_key');
SET @ddl := IF(@asm_gone > 0,
    'ALTER TABLE tech_card_operation
        DROP COLUMN output_unit_key,
        DROP COLUMN output_unit_name',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;
