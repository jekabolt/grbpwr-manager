-- Полоса DESIGN, круг 18 (D-24) — КАДР «ТОЛЬКО ДЛЯ ПОКАЗА».
--
-- Владелец, дословно — «в THE SHEET должна быть возможность добавить отдельно медиа без слотов
-- КОТОРЫЕ НЕ ПОЙДУТ в промпты они нужны только для визуализации в артефактах».
--
-- ЧТО ЭТО ЗА СТРОКА. design_picture, помеченная display_only = 1, видна на листе и в артефактах и
-- НИКОГДА не уезжает ни в один платный вызов. Флаг — утверждение загружающего, замороженное при
-- регистрации пачки (RegisterDesignUpload, DesignUploadItem.display_only). Кроп и флэттен
-- наследуют его у родителя (pictures.go, layer.go) — разрез не делает картинку входом.
--
-- КТО ЕГО ЧИТАЕТ, И ПОЧЕМУ ЧИТАТЕЛЕЙ ЧЕТЫРЕ, А НЕ ОДИН. Три двери ЗАПИСИ в сторе — постановка в
-- слот (bench.go), роль референса (reference.go), разрез «для промпта» (pictures.go) — закрывают
-- все пути, которыми кадр попадает в отбор входов через полосу. Четвёртая — ДЕНЕЖНАЯ дверь прогона
-- (apisrv/admin, designRefuseDisplayOnlyInputs) — спрашивает у самих номеров медиа по всем пяти
-- источникам входа, потому что часть из них (extra_input_media_ids, ткань рецепта, доска черновика)
-- минует полосу целиком, и фильтр в сборке входов молча пропустил бы новый путь.
--
-- ФЛАГ, А НЕ ОТДЕЛЬНЫЙ РОД (kind). Род кадра говорит, ЧТО на картинке (флэт, рендер, 3D, паттерн),
-- и по нему выбирается верстак и раздел экрана; «только для показа» — свойство ПОПЕРЁК родов —
-- рендер для артефактов остаётся рендером и лежит в RENDERS. Новое значение kind заставило бы
-- каждый читатель рода (разделы, верстак, счётчики) выучить пятый член, а флаг читают ровно те
-- четыре двери, которым он нужен.
--
-- ЦЕНА. ADD COLUMN ... NOT NULL DEFAULT 0 в КОНЕЦ таблицы — INSTANT (MySQL 8.0.12+), строки не
-- переписываются. CHECK не заводится по общему правилу волны — ретроактивный ADD CONSTRAINT CHECK
-- копирует таблицу целиком и упирается в пятиминутный потолок старта. Словарь держат Go-писатели.
-- Бэкфилла нет — ни одна существующая строка не была загружена с этим намерением, и ноль у всех
-- есть правда.

-- +migrate Up

SET @dp_display_only := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'design_picture'
      AND COLUMN_NAME = 'display_only');
SET @ddl := IF(@dp_display_only = 0,
    'ALTER TABLE design_picture
        ADD COLUMN display_only TINYINT(1) NOT NULL DEFAULT 0
            COMMENT ''1 = кадр ТОЛЬКО ДЛЯ ПОКАЗА (D-24) — виден на листе и в артефактах, никогда не уезжает в платный вызов, не встаёт в слот, не получает роли референса; наследуется кропом и флэттеном''',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- +migrate Down
--
-- Откат теряет пометку, и это осознанно. Прежний бинарь не знает флага и читает такую картинку как
-- обычную, то есть возвращается к состоянию, где «только для показа» невыразимо.

SET @dp_display_only_down := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'design_picture'
      AND COLUMN_NAME = 'display_only');
SET @ddl := IF(@dp_display_only_down = 1,
    'ALTER TABLE design_picture DROP COLUMN display_only',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;
