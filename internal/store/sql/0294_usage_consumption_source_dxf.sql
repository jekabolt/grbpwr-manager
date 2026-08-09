-- +migrate Up

-- Третий источник нормы расхода: 'dxf' — площадь деталей выкройки, поделённая на раскройную
-- ширину полотна (width_cm − 2×selvedge_cm). NETTO, и это главное свойство нового значения.
--
-- ЗАЧЕМ ОН НУЖЕН. До сих пор источников было два: 'marker' — снято с сохранённой раскладки
-- (длина уже СОДЕРЖИТ межлекальные отходы, поэтому костинг такую норму не гроссит) и 'manual' —
-- набрано руками. Раскладка появляется не сразу: на уровне стиля, где считается себестоимость
-- (норма базового размера → product.cost_price → маржа) и где живёт потребность до первого
-- прогона, раскладки может не быть вовсе. Там оставался только ручной ввод, то есть число «на
-- глаз», которое затем ещё и умножается на процент раскроя «на глаз» — две оценки перемножаются
-- и уходят в деньги. Выкройки в этот момент уже есть: DXF знает площадь каждой детали.
--
-- ПОЧЕМУ МАТЕМАТИКА ДЕНЕГ НЕ МЕНЯЕТСЯ. entity.wastageApplies() отвечает «гроссить» на всё, что
-- не 'marker' — значит dxf-строка автоматически догроссится процентом раскроя слота
-- (bom_item.wastage_percent), как и ручная. Это ровно то, что нужно: netto из выкроек + честно
-- заявленный процент отходов. Ни одна формула костинга, плана материалов или резерва не трогается
-- этой миграцией; меняется только то, ЧТО ЗНАЧИТ число и можно ли его проверить.
--
-- ЧТО ОСТАЁТСЯ ЗАПРЕЩЁННЫМ для dxf-строки (держится в Go, не в схеме, ровно как у 0291):
-- разложение отходов (waste_selvedge_pct / waste_cut_pct — они про измеренную раскладку) и штамп
-- нормы norm_marker_id. usageProvenance.normalized() чистит и то, и другое у любого не-marker
-- источника, поэтому схема здесь ничего добавлять не должна.
--
-- РЕТРОАКТИВНОСТЬ. ADD CONSTRAINT проверяет ВСЮ существующую историю таблицы, и новый CHECK,
-- который старые данные не проходят, останавливает старт прода. Здесь это безопасно by
-- construction: новое множество — НАДМНОЖЕСТВО старого ('manual','marker' ⊂
-- 'manual','marker','dxf'), любая уже лежащая строка проходит.
--
-- ЗАМЫКАНИЕ РЕГИСТРА СОЗНАТЕЛЬНО НЕ ДОБАВЛЕНО. IN (...) наследует коллацию колонки, а она
-- регистронезависима и на utf8mb3 прода, и на utf8mb4 контейнерных тестов — то есть CHECK принял
-- бы 'MANUAL'. Дыра эта досталась от 0261 и закрыта уровнем выше: entity.ValidConsumptionSources
-- сверяет точное значение, и через API иначе как в нижнем регистре источник не попадает. Добавить
-- сюда STRCMP-замыкание означало бы сделать CHECK НЕ надмножеством старого — и первая же строка
-- в другом регистре (а узнать это можно только на проде) уронила бы деплой. Цена замыкания выше
-- пользы, пока Go остаётся единственной дорогой к колонке.

SET @needs := (
    SELECT COUNT(*) FROM information_schema.CHECK_CONSTRAINTS
    WHERE CONSTRAINT_SCHEMA = DATABASE()
      AND CONSTRAINT_NAME = 'chk_tccu_consumption_source'
      AND CHECK_CLAUSE NOT LIKE '%dxf%'
);
SET @ddl := IF(@needs = 1,
    'ALTER TABLE tech_card_colorway_usage DROP CONSTRAINT chk_tccu_consumption_source, ADD CONSTRAINT chk_tccu_consumption_source CHECK (consumption_source IN (''manual'', ''marker'', ''dxf''))',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- +migrate Down

-- Строки с новым источником не пройдут суженный CHECK, поэтому сначала переводим их в 'manual'
-- (как 0263 клампит проценты перед сужением) — иначе откат объявлен, но не исполним.
--
-- Это не потеря денег: для костинга и плана материалов 'dxf' и 'manual' — одна и та же ветка
-- (гросс процентом раскроя слота), число в consumption остаётся тем же. Теряется только
-- происхождение: netto из выкроек станет неотличимо от набранного руками.
UPDATE tech_card_colorway_usage SET consumption_source = 'manual' WHERE consumption_source = 'dxf';

SET @needs := (
    SELECT COUNT(*) FROM information_schema.CHECK_CONSTRAINTS
    WHERE CONSTRAINT_SCHEMA = DATABASE()
      AND CONSTRAINT_NAME = 'chk_tccu_consumption_source'
      AND CHECK_CLAUSE LIKE '%dxf%'
);
SET @ddl := IF(@needs = 1,
    'ALTER TABLE tech_card_colorway_usage DROP CONSTRAINT chk_tccu_consumption_source, ADD CONSTRAINT chk_tccu_consumption_source CHECK (consumption_source IN (''manual'', ''marker''))',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;
