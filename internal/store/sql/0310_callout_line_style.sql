-- Пунктир, штриховка и несколько деталей на одном указании.
--
-- Три факта, добавленных к указанию 0309, и все три — про то, ЧТО оно говорит, а не как выглядит:
--
--   dashed — сплошная линия на чертеже означает «шов, который делают», пунктир — построение,
--            припуск, линию под слоем. Это разные указания цеху, а не два оформления одного.
--   filled — контур говорит «вот эта граница», штриховка — «вот эта площадь». На дублировании
--            клеевой и на пороке ткани разница принципиальна.
--   parts  — узел законно собирает несколько деталей сразу («втачать рукав в пройму» это и рукав,
--            и полочка, и спинка). Одиночное `part` требовало выбрать из них главную, а у шва
--            главной нет.
--
-- `part` НЕ СНИМАЕТСЯ. На нём стоит связь «деталь ↔ выноска» (деталь ссылается на выноску НОМЕРОМ,
-- сверка идёт по имени), его печатает тех-пак и хранит архив релиза. Список ЖИВЁТ РЯДОМ и по
-- правилу «первый элемент равен part»; в колонку он пишется только когда деталей больше одной,
-- поэтому у сегодняшних карточек колонка остаётся NULL и второго места для одной и той же строки
-- не заводится.
--
-- ПОЧЕМУ TINYINT(1) С ДЕФОЛТОМ, А НЕ NULL: «не пунктирная» — это факт каждой существующей линии, а
-- не отсутствие сведений. NULL здесь означал бы третье состояние, которого у пунктира нет.
--
-- CHECK на согласованность (пунктир только у линии, штриховка только у полигона) намеренно НЕ
-- ставится: ретроактивный CHECK проверяет ВСЮ историю и роняет старт прода, а само правило живёт в
-- Go, где несогласованный флаг приводится к false с объяснением — см. calloutGeometryFromPb.
--
-- Идемпотентно: одним охраняемым ALTER, охрана по первой из трёх колонок — добавляются они вместе
-- или не добавляются вовсе.

-- +migrate Up

SET @callout_style := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_callout'
      AND COLUMN_NAME = 'dashed');
SET @ddl := IF(@callout_style = 0,
    'ALTER TABLE tech_card_callout
        ADD COLUMN dashed TINYINT(1) NOT NULL DEFAULT 0 COMMENT ''пунктирная линия вместо сплошной; у pin всегда 0'',
        ADD COLUMN filled TINYINT(1) NOT NULL DEFAULT 0 COMMENT ''штриховка области; только у polygon, у прочих всегда 0'',
        ADD COLUMN parts JSON NULL COMMENT ''имена деталей указания; NULL = одна деталь и она в part''',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- +migrate Down

SET @callout_style_down := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_callout'
      AND COLUMN_NAME = 'dashed');
SET @ddl_down := IF(@callout_style_down = 1,
    'ALTER TABLE tech_card_callout DROP COLUMN parts, DROP COLUMN filled, DROP COLUMN dashed',
    'SELECT 1');
PREPARE stmt FROM @ddl_down;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;
