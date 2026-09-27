-- Тех-карта / полоса DESIGN (27.09, D-57) — ИНДЕКС ПОД ЧТЕНИЕ ТЕХНИЧЕСКОГО ЛИСТА.
--
-- ЧТО ЧИТАЕТ. Перезапись (FlattenEditLayer с replace_picture_id) спрашивает в своей SERIALIZABLE-
-- транзакции, стоит ли файл оригинала на техническом листе карточки (отказ technical_sheet):
--   tech_card_media WHERE tech_card_id = ? AND media_id = ? AND category = 'technical'.
-- До этой миграции у таблицы были только индексы внешних ключей, (tech_card_id) и (media_id), а
-- category (0092) без индекса вовсе. Правильность держал SERIALIZABLE — next-key замки на том
-- индексе, который выберет план, — но план запирал бы ВСЕ строки листа карточки или все
-- использования общего файла: лишние дедлоки с сейвом карточки, который переписывает
-- tech_card_media целиком.
--
-- ЧТО ДАЁТ ИНДЕКС. Равенство по всем трём колонкам: чтение запирает ровно ключ (карточка, файл,
-- лист) и промежуток, куда встала бы такая строка, и сейв, ставящий этот файл на этот лист, ждёт
-- ровно его. Префикс (tech_card_id) заодно служит DELETE сейва и внешнему ключу на tech_card.
--
-- ⚠ ИНДЕКС ВНЕШНЕГО КЛЮЧА. MySQL вправе молча снять неявный индекс FK (tech_card_id), когда
-- появляется другой индекс, способный его обслуживать, а этот способен. Up от этого не страдает.
-- Down обязан сначала вернуть индекс на tech_card_id, если другого не осталось, и только потом
-- снимать этот — иначе 1553 «needed in a foreign key constraint». Тот же порядок, что в 0300.
--
-- ЦЕНА. ADD INDEX — INPLACE, без копии таблицы и без блокировки записи, на проде в таблице
-- десятки строк (замер в 0346). CHECK не добавляется, данные не трогаются.
--
-- ИДЕМПОТЕНТНОСТЬ. Каждый DDL под гейтом information_schema.STATISTICS: MySQL автокоммитит DDL, и
-- повторный прогон после падения ниже по файлу обязан быть no-op. PREPARE/EXECUTE/DEALLOCATE — по
-- одному оператору на строку: прод ходит без multiStatements.

-- +migrate Up

SET @tcm_sheet_idx := (SELECT COUNT(*) FROM information_schema.STATISTICS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_media'
      AND INDEX_NAME = 'idx_tech_card_media_sheet');
SET @ddl := IF(@tcm_sheet_idx = 0,
    'ALTER TABLE tech_card_media ADD INDEX idx_tech_card_media_sheet (tech_card_id, media_id, category)',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- +migrate Down
--
-- Сначала покрытие внешнего ключа на tech_card_id, если MySQL снял неявный индекс при Up, — под
-- тем же именем, что было у неявного, чтобы схема вернулась к виду до 0372. Потом сам индекс.

SET @tcm_card_idx := (SELECT COUNT(*) FROM information_schema.STATISTICS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_media'
      AND INDEX_NAME <> 'idx_tech_card_media_sheet'
      AND SEQ_IN_INDEX = 1 AND COLUMN_NAME = 'tech_card_id');
SET @ddl := IF(@tcm_card_idx = 0,
    'ALTER TABLE tech_card_media ADD INDEX tech_card_id (tech_card_id)',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @tcm_sheet_idx_down := (SELECT COUNT(*) FROM information_schema.STATISTICS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_media'
      AND INDEX_NAME = 'idx_tech_card_media_sheet');
SET @ddl := IF(@tcm_sheet_idx_down > 0,
    'ALTER TABLE tech_card_media DROP INDEX idx_tech_card_media_sheet',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;
