-- Полоса DESIGN, цепочка правок (T28 v2, пункт владельца 28) — UNDO / REDO ПРАВКИ.
--
-- Владелец: правка в LATEST GENERATION или во FLAT SLOTS пропагейтится — на месте картинки стоит
-- только новая, а на ховер есть undo и redo.
--
-- ЧТО ЭТО ЗА КОЛОНКА. Метка ОТМЕНЫ звена цепочки замен (replaced_by, 0369). UndoDesignEdit ставит её на
-- текущую версию цепочки, RedoDesignEdit снимает со звена за текущей версией — каждый одной
-- транзакцией с замком на звенья карточки, CAS по expected_current_id и переездом слота верстака.
-- ТЕКУЩАЯ ВЕРСИЯ = обход replaced_by от корня до первого звена с undone_at, не включая его.
--
-- ПОЧЕМУ НЕ hidden_at. Спрятанность — другой глагол со своими сторожами (in_slot, live_run_input,
-- live_crop_parent) и своими читателями (пикеры, роли референсов, разрез, перезапись). «Отменено» в
-- hidden_at смешало бы два смысла: un-hide поднимал бы отменённую правку, а спрятанный преемник
-- освобождал бы место кадра. undo/redo hidden_at не трогают.
--
-- DATETIME(6) NULL — по образцу hidden_at. CHECK нет (ретроактивный ADD CONSTRAINT CHECK копирует
-- таблицу целиком), индекса нет (колонка читается у строк, найденных по tech_card_id), бэкфилла нет:
-- до этой миграции отменить правку было нечем, и NULL у всех есть правда.
--
-- ЦЕНА. ADD COLUMN ... NULL в КОНЕЦ таблицы — INSTANT (MySQL 8.0.12+), строки не переписываются.
--
-- ОКНО «СТАРЫЙ БИНАРЬ × НОВАЯ СХЕМА» БЕЗОПАСНО: хендл стора — `d.Unsafe()` (store.go), лишняя
-- колонка в SELECT * читается в никуда, а INSERT'ы старого бинаря её не называют и оставляют NULL.

-- +migrate Up

SET @dp_undone_at := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'design_picture'
      AND COLUMN_NAME = 'undone_at');
SET @ddl := IF(@dp_undone_at = 0,
    'ALTER TABLE design_picture
        ADD COLUMN undone_at DATETIME(6) NULL
            COMMENT ''правка отменена (T28 v2, undo); текущая версия цепочки replaced_by — до первого отменённого звена. Не hidden_at: undo/redo видимость не трогают''',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- +migrate Down
--
-- Откат теряет метки отмены: каждая цепочка снова читается до хвоста. Слоты при этом стоят там, куда
-- их поставил последний undo/redo, — это обычные плиты, и прежний бинарь читает их как есть.

SET @dp_undone_at_down := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'design_picture'
      AND COLUMN_NAME = 'undone_at');
SET @ddl := IF(@dp_undone_at_down = 1,
    'ALTER TABLE design_picture DROP COLUMN undone_at',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;
