-- Тех-карта / полоса DESIGN (27.09, D-57) — ДВА ИНДЕКСА ПОД ДВЕ ДВЕРИ ОДНОГО ИНВАРИАНТА:
-- на техническом листе карточки нет файла её заменённого кадра.
--
-- 1. tech_card_media (tech_card_id, media_id, category) — idx_tech_card_media_sheet.
-- Перезапись (FlattenEditLayer с replace_picture_id) спрашивает в своей SERIALIZABLE-транзакции,
-- стоит ли файл оригинала на листе карточки (отказ technical_sheet):
--   tech_card_media WHERE tech_card_id = ? AND media_id = ? AND category = 'technical',
-- а сейв карточки там же считает, сколько раз файлы входящего листа уже стоят на сохранённом
-- (переход, а не состояние): те же три колонки, media_id списком. До этой миграции у таблицы были
-- только индексы внешних ключей, (tech_card_id) и (media_id), а category (0092) без индекса вовсе.
-- Правильность держал SERIALIZABLE — next-key замки на том индексе, который выберет план, — но план
-- запирал бы все строки листа карточки или все использования общего файла: лишние дедлоки с сейвом
-- карточки, который переписывает tech_card_media целиком. С индексом равенство по всем трём колонкам
-- запирает ровно ключ (карточка, файл, лист) и промежуток, куда встала бы такая строка. Префикс
-- (tech_card_id) заодно служит DELETE сейва и внешнему ключу на tech_card.
--
-- 2. design_picture (tech_card_id, media_id, id) — idx_design_picture_card_media.
-- Сейв карточки читает заменённые кадры своей карточки по файлам входящего листа:
--   design_picture WHERE tech_card_id = ? AND media_id IN (…) AND replaced_by IS NOT NULL.
-- Кандидатами были (tech_card_id, id) — все кадры карточки — и (media_id) — все использования файла
-- любыми карточками; каждый из них проверяет только одно равенство из двух, и лишний скан под
-- SERIALIZABLE — лишние замки против перезаписей, не касающихся этого листа. С индексом скан — кадры
-- этой карточки с этими файлами. id последним: InnoDB и так дописывает первичный ключ в конец
-- вторичного индекса, здесь он назван, чтобы порядок внутри файла был виден из DDL.
--
-- ⚠ ИНДЕКСЫ ВНЕШНИХ КЛЮЧЕЙ. MySQL вправе молча снять индекс, который он СОЗДАЛ САМ под внешний ключ,
-- когда появляется другой, способный этот ключ обслуживать. У tech_card_media индекс FK на
-- tech_card_id неявный (0067 объявляет FOREIGN KEY без KEY), и индекс 1 способен его заменить —
-- поэтому Down сначала возвращает индекс на tech_card_id, если другого не осталось, и только потом
-- снимает индекс 1: иначе 1553 «needed in a foreign key constraint» (тот же порядок, что в 0300).
-- У design_picture индексы FK объявлены ЯВНО (0340: idx_design_picture_card (tech_card_id, id) под
-- fk_design_picture_card, idx_design_picture_media (media_id) под fk_design_picture_media), а явный
-- индекс MySQL сам не снимает; индекс 2 ничего не заменяет. Down у него держит тот же страховочный
-- шаг — на случай, если явный индекс кто-то снял руками, — и в штатной схеме это no-op.
--
-- ЦЕНА. Оба ADD INDEX — вторичные индексы InnoDB: MySQL 8 строит их INPLACE, с параллельной записью;
-- ALGORITHM/LOCK здесь не пишутся по правилу 0356 (алгоритм — факт, который надо знать, а не
-- команда: сервер, который не смог бы, остановил бы старт вместо того, чтобы построить индекс
-- дороже). Метаданный замок каждый ALTER берёт дважды, коротко, и ждёт открытых транзакций по своей
-- таблице. CHECK не добавляется, данные не трогаются.
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

SET @dp_card_media_idx := (SELECT COUNT(*) FROM information_schema.STATISTICS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'design_picture'
      AND INDEX_NAME = 'idx_design_picture_card_media');
SET @ddl := IF(@dp_card_media_idx = 0,
    'ALTER TABLE design_picture ADD INDEX idx_design_picture_card_media (tech_card_id, media_id, id)',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- +migrate Down
--
-- На каждой таблице сначала покрытие внешнего ключа на tech_card_id, если другого индекса с этой
-- колонкой первой не осталось, — под тем же именем, что было до 0372, чтобы схема вернулась к
-- прежнему виду. Потом сам индекс.

SET @dp_card_idx := (SELECT COUNT(*) FROM information_schema.STATISTICS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'design_picture'
      AND INDEX_NAME <> 'idx_design_picture_card_media'
      AND SEQ_IN_INDEX = 1 AND COLUMN_NAME = 'tech_card_id');
SET @ddl := IF(@dp_card_idx = 0,
    'ALTER TABLE design_picture ADD INDEX idx_design_picture_card (tech_card_id, id)',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @dp_card_media_idx_down := (SELECT COUNT(*) FROM information_schema.STATISTICS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'design_picture'
      AND INDEX_NAME = 'idx_design_picture_card_media');
SET @ddl := IF(@dp_card_media_idx_down > 0,
    'ALTER TABLE design_picture DROP INDEX idx_design_picture_card_media',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

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
