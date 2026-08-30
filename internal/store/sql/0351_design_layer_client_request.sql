-- 0351 — КЛЮЧ ИДЕМПОТЕНТНОСТИ ВЕКТОРНОГО ИМПОРТА.
--
-- ЧТО БЫЛО НЕВЕРНО. Контракт объявляет ключом ИМЕННО `client_request_id`
-- (`ImportDesignVectorRequest.client_request_id`, дословно: «Idempotency: a retry after a lost
-- response must not file the same SVG as a second layer»), хендлер его ТРЕБУЕТ и кладёт в
-- entity.DesignVectorImport — а стор его не читал вовсе, потому что колонки под него не было ни в
-- 0343, ни в 0350. Дедупликация шла по паре (tech_card_id, source_media_id), и это ДРУГОЕ правило,
-- расходящееся с обещанным ровно в двух случаях:
--
--   * ДРУГОЙ файл под ТЕМ ЖЕ запросом — заводил ВТОРОЙ слой, хотя запрос тот же самый;
--   * ТОТ ЖЕ файл под НОВЫМ запросом — возвращал старый слой, хотя запрос новый.
--
-- То есть система отвечала ровно наоборот тому, что обещала, и «ключ» на проводе был украшением.
--
-- ПОЧЕМУ КОЛОНКА, А НЕ ВЫВОД ИЗ ИМЕЮЩИХСЯ. Идемпотентность — это память о ЧУЖОМ РЕШЕНИИ (клиент
-- назвал запрос), и вывести её из содержимого нельзя ни при каком запросе: два разных намерения с
-- одинаковым содержимым и одно намерение с разным содержимым — разные вещи, и различает их только
-- сам идентификатор.
--
-- ⚠ КОЛОНКА NULLABLE, И ЭТО НЕСУЩЕЕ. Строки, заведённые до этой миграции (и все строки, которые
-- заводит SaveEditLayer — у него своего request-id нет и по смыслу быть не может: он CAS по rev,
-- а не однократная подача), никакого запроса не помнят. NOT NULL потребовал бы выдумать им
-- значение — а выдуманный ключ идемпотентности ХУЖЕ отсутствующего: он однажды совпадёт.
--
-- ⚠ UNIQUE ПО NULL-КОЛОНКЕ РАБОТАЕТ ИМЕННО ТАК, КАК ЗДЕСЬ НУЖНО: MySQL допускает СКОЛЬКО УГОДНО
-- NULL в уникальном индексе, поэтому слои без запроса друг другу не мешают, а два импорта с одним
-- запросом ловятся базой, а не только чтением в транзакции. Пояс нужен: чтение под SERIALIZABLE
-- закрывает гонку двух транзакций, но не закрывает вставку в обход стора.
--
-- КЛЮЧ ГЛОБАЛЬНЫЙ, А НЕ ПАРА С КАРТОЧКОЙ — дословно как у соседей полосы (`uq_design_run_client_request`,
-- `uq_design_batch_client_request`, 0340). Один запрос человека — одна запись во всей системе, и
-- запрос, пришедший со второй карточкой, это не «другая запись», а ошибка клиента, которую стор
-- обязан назвать вслух.
--
-- CHAR(36) — UUID, тот же тип и та же ширина, что у обеих соседних колонок 0340. Ни одного
-- `ADD CONSTRAINT CHECK`: он копирует таблицу целиком и проверяет всю историю, а проверка миграций
-- ограничена пятью минутами в коде.
--
-- `ADD COLUMN IF NOT EXISTS` в MySQL не существует, поэтому каждый ALTER стоит под собственным
-- гейтом по information_schema. `PREPARE` / `EXECUTE` / `DEALLOCATE` — КАЖДЫЙ СВОЕЙ СТРОКОЙ: прод
-- ходит БЕЗ `multiStatements`, драйвер выполняет по одному запросу, и склеенные в одну строку
-- операторы падают ТОЛЬКО на проде — контейнерный тест эту поломку маскирует.

-- +migrate Up

-- 1. Колонка. Живая таблица, nullable в конец — INSTANT, копии нет.
SET @del_req := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'design_edit_layer'
      AND COLUMN_NAME = 'client_request_id');
SET @ddl := IF(@del_req = 0,
    'ALTER TABLE design_edit_layer
        ADD COLUMN client_request_id CHAR(36) NULL COMMENT ''идемпотентность ImportDesignVector: повтор после потерянного ответа обязан вернуть ТОТ ЖЕ слой, а не подшить файл вторым. NULL = слой заведён не импортом (SaveEditLayer) либо до 0351''',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- 2. Уникальный индекс. Отдельным оператором от ADD COLUMN намеренно: падение между ними оставляет
-- колонку без индекса, и повтор миграции доводит дело до конца, вместо того чтобы упереться в
-- «колонка уже есть».
SET @del_req_idx := (SELECT COUNT(*) FROM information_schema.STATISTICS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'design_edit_layer'
      AND INDEX_NAME = 'uq_design_edit_layer_client_request');
SET @ddl := IF(@del_req_idx = 0,
    'ALTER TABLE design_edit_layer
        ADD UNIQUE KEY uq_design_edit_layer_client_request (client_request_id)',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- +migrate Down
--
-- Порядок обратный Up, гейты симметричные: падение посреди отката оставляет состояние, с которого
-- повтор продолжает. Индекс снимается ПЕРЕД колонкой — MySQL не даст выбросить колонку, на которой
-- висит ключ. Откат теряет ключи идемпотентности уже поданных импортов; на проде и бете миграции
-- идут только вверх, Down здесь путь разработчика.

SET @del_req_idx_down := (SELECT COUNT(*) FROM information_schema.STATISTICS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'design_edit_layer'
      AND INDEX_NAME = 'uq_design_edit_layer_client_request');
SET @ddl_down := IF(@del_req_idx_down > 0,
    'ALTER TABLE design_edit_layer DROP INDEX uq_design_edit_layer_client_request',
    'SELECT 1');
PREPARE stmt FROM @ddl_down;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @del_req_down := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'design_edit_layer'
      AND COLUMN_NAME = 'client_request_id');
SET @ddl_down := IF(@del_req_down = 1,
    'ALTER TABLE design_edit_layer DROP COLUMN client_request_id',
    'SELECT 1');
PREPARE stmt FROM @ddl_down;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;
