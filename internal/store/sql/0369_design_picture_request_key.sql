-- Полоса DESIGN, воркбенч FLAT (O-53 review) — КЛЮЧ ИДЕМПОТЕНТНОСТИ ФЛЭТТЕНА.
--
-- ЧТО БЫЛО НЕВЕРНО. Перезапись (0368) — это флэттен с replace_picture_id, и у флэттена не было ключа
-- повтора. Транзакция коммитила правку, ответ терялся, и повтор того же запроса получал
-- already_replaced — отказ на собственный успех, неотличимый от чужой перезаписи; повтор «save as
-- new» молча подавал второго сиблинга. Запрос получил `client_request_id`
-- (FlattenDesignEditLayerRequest, поле 6), и эта колонка — память о нём: кадр, поданный под ключом,
-- и есть ответ повтору.
--
-- ПОЧЕМУ КОЛОНКА НА design_picture, А НЕ ОТДЕЛЬНЫЙ ЖУРНАЛ ЗАПРОСОВ. Результат флэттена — ровно одна
-- строка этой таблицы, и ключ на ней отвечает на вопрос повтора одним чтением в той же транзакции.
-- Журнал был бы второй записью того же факта, способной разойтись с первой.
--
-- ⚠ ИНДЕКС НЕ УНИКАЛЬНЫЙ, И ЭТО НЕСУЩЕЕ, А НЕ ЛЕНЬ. SplitDesignPictureRequest.client_request_id
-- объявлен контрактом и пока не читается (бэклог, TODO в SplitPicture); когда разрез его прочтёт, ОДИН
-- ключ подпишет ВСЕ куски одного разреза, и уникальный ключ запретил бы ровно это. Флэттен
-- подписывает ключом один кадр сам: повтор находит его чтением ДО вставки, а два конкурентных повтора
-- под SERIALIZABLE читают один пробел этого индекса — вставку получает один, второй ловит дедлок, и
-- повтор транзакции находит кадр первого. Пара начинается с tech_card_id: ключ живёт в пределах
-- карточки, и чтение повтора — точечный поиск по префиксу.
--
-- utf8mb4_bin — СРАВНЕНИЕ ПОБАЙТНОЕ, КАК В GO (прецедент — operation_work.token, 0329). Сортировка
-- таблицы (utf8mb4_unicode_ci) склеила бы «Abc» с «abc», и чужой ключ, отличающийся регистром,
-- отвечал бы на повтор. Хвостовые пробелы сортировка _bin тоже не различает — поэтому стор срезает
-- пробелы по краям ключа до чтения и до записи.
--
-- VARCHAR(64) — ширина под UUID (36) с запасом под формат клиента; длиннее стор отказывает до
-- транзакции (entity.DesignRequestKeyMaxRunes). NULL — кадр подан без ключа (всё, что до 0369, и
-- всё, что подано не флэттеном) — законное состояние, и выдумывать ключ старым строкам нельзя:
-- выдуманный ключ однажды совпадёт.
--
-- ЦЕНА. ADD COLUMN ... NULL в конец таблицы — INSTANT (MySQL 8.0.12+), строки не переписываются.
-- ADD KEY — INPLACE, без блокировки записи. Отдельными операторами под своими гейтами, как в 0351:
-- смешивать INSTANT-колонку и индекс в одном ALTER нельзя (MySQL выбрал бы копию), а падение между
-- ними оставляет колонку без индекса, и повтор миграции доводит дело до конца.
--
-- ОКНО «СТАРЫЙ БИНАРЬ × НОВАЯ СХЕМА» БЕЗОПАСНО: хендл стора — `d.Unsafe()`, лишняя колонка в
-- SELECT * читается в никуда, а INSERT'ы старого бинаря её не называют и оставляют NULL.
--
-- `ADD COLUMN IF NOT EXISTS` в MySQL не существует, поэтому каждый ALTER стоит под собственным
-- гейтом по information_schema. `PREPARE` / `EXECUTE` / `DEALLOCATE` — КАЖДЫЙ СВОЕЙ СТРОКОЙ: прод
-- ходит БЕЗ `multiStatements`.

-- +migrate Up

-- 1. Колонка.
SET @dp_request_key := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'design_picture'
      AND COLUMN_NAME = 'request_key');
SET @ddl := IF(@dp_request_key = 0,
    'ALTER TABLE design_picture
        ADD COLUMN request_key VARCHAR(64) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NULL
            COMMENT ''client_request_id жеста, подавшего кадр (FlattenDesignEditLayer, 0369): повтор после потерянного ответа находит по нему кадр первой попытки. NULL = подан без ключа. Не уникален намеренно: разрез однажды подпишет одним ключом все свои куски''',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- 2. Индекс чтения повтора: (карточка, ключ). Обычный, не UNIQUE — см. шапку.
SET @dp_request_key_idx := (SELECT COUNT(*) FROM information_schema.STATISTICS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'design_picture'
      AND INDEX_NAME = 'idx_design_picture_request_key');
SET @ddl := IF(@dp_request_key_idx = 0,
    'ALTER TABLE design_picture
        ADD KEY idx_design_picture_request_key (tech_card_id, request_key)',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- +migrate Down
--
-- Порядок обратный Up, гейты симметричные: индекс снимается ПЕРЕД колонкой — MySQL не даст выбросить
-- колонку, на которой висит ключ. Откат теряет ключи уже поданных флэттенов: повтор после отката
-- ведёт себя как до 0369 (already_replaced либо второй сиблинг). На проде и бете миграции идут только
-- вверх, Down здесь путь разработчика.

SET @dp_request_key_idx_down := (SELECT COUNT(*) FROM information_schema.STATISTICS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'design_picture'
      AND INDEX_NAME = 'idx_design_picture_request_key');
SET @ddl_down := IF(@dp_request_key_idx_down > 0,
    'ALTER TABLE design_picture DROP INDEX idx_design_picture_request_key',
    'SELECT 1');
PREPARE stmt FROM @ddl_down;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @dp_request_key_down := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'design_picture'
      AND COLUMN_NAME = 'request_key');
SET @ddl_down := IF(@dp_request_key_down = 1,
    'ALTER TABLE design_picture DROP COLUMN request_key',
    'SELECT 1');
PREPARE stmt FROM @ddl_down;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;
