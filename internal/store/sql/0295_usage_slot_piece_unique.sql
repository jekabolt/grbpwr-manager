-- +migrate Up

-- T4 (роли слоёв у детали кроя): защитный UNIQUE на связь «колорвей × слот BOM × деталь».
--
-- Модель: одна деталь может ссылаться на НЕСКОЛЬКО материалов в одном колорвее, если это СЛОИ
-- (основной / подклад / дублерин) — по детальной строке рецепта на слой. Роль слоя НЕ хранится:
-- она выводится из строки BOM (section + purpose, entity.DerivePieceLayerRole). Этой миграции
-- поэтому нечего добавлять в колонки; она лишь закрепляет в схеме app-инвариант, который
-- UpdateColorwayRecipe уже держит («duplicate_slot»): одна строка на пару (слот, деталь) в
-- колорвее. Два одинаковых слоя одной детали — это не два слоя, это дубль записи.
--
-- КОРТЕЖИ С NULL ПОД СОСТАВНОЙ UNIQUE НЕ ПОДПАДАЮТ (MySQL): строки «на изделие» (piece_id NULL)
-- законно повторяют слот с разными placement («пуговицы — планка» / «пуговицы — манжета»), а
-- legacy-строки без разрешённого bom_item_id хэшируются в один (NULL, NULL) слот. Это ровно
-- нынешние carve-out'ы app-инварианта — индекс их не трогает by construction.
--
-- ДЕДУП ПЕРЕД ИНДЕКСОМ (правило 0059): ADD UNIQUE проверяет ВСЮ историю таблицы, и точный дубль
-- в старых данных остановил бы старт прода. По снятым данным беты дублей ноль и шаг — no-op, но
-- файл обязан быть безопасен на ЛЮБЫХ данных. Из дублей выживает строка с МАКСИМАЛЬНЫМ id:
-- рецепт пишется full-replace, последняя запись — истина. Пер-размерные нормы удаляемых строк
-- уходят каскадом (fk usage_id ON DELETE CASCADE, 0079). Самосоединение по равенству само
-- исключает NULL-кортежи; WHERE дублирует это явно, чтобы намерение читалось без справочника.
DELETE u FROM tech_card_colorway_usage u
JOIN tech_card_colorway_usage d
  ON d.colorway_id = u.colorway_id
 AND d.bom_item_id = u.bom_item_id
 AND d.piece_id = u.piece_id
 AND d.id > u.id
WHERE u.piece_id IS NOT NULL
  AND u.bom_item_id IS NOT NULL;

-- Гард по information_schema: повторный прогон файла (микс-ап DDL auto-commit + падение ниже по
-- файлу) не должен споткнуться о уже созданный индекс. PREPARE/EXECUTE/DEALLOCATE по одному
-- оператору на строку — прод ходит без multiStatements.
SET @needs := (
    SELECT COUNT(*) FROM information_schema.STATISTICS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'tech_card_colorway_usage'
      AND INDEX_NAME = 'uniq_usage_slot_piece'
);
SET @ddl := IF(@needs = 0,
    'ALTER TABLE tech_card_colorway_usage ADD UNIQUE KEY uniq_usage_slot_piece (colorway_id, bom_item_id, piece_id)',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- +migrate Down

-- Индекс чисто защитный, данных он не менял (дедуп Down не воскрешает — удалённые дубли были
-- мусором full-replace-семантики). Старый бинарь при откате DO пишет рецепт тем же full-replace
-- и дубли отбивает app-side, так что и оставшийся индекс ему бы не мешал; Down симметричен для
-- порядка, а не по необходимости.
--
-- Гард сравнивает с НУЛЁМ, а не с единицей: STATISTICS держит по строке НА КАЖДЫЙ СТОЛБЕЦ
-- индекса, у трёхколоночного uniq_usage_slot_piece их три, и условие `= 1` никогда не срабатывало
-- бы — откат молча оставлял бы индекс на месте. (Up этим не задет: отсутствующий индекс — ноль
-- строк независимо от числа столбцов.)
SET @needs := (
    SELECT COUNT(*) FROM information_schema.STATISTICS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'tech_card_colorway_usage'
      AND INDEX_NAME = 'uniq_usage_slot_piece'
);
SET @ddl := IF(@needs > 0,
    'ALTER TABLE tech_card_colorway_usage DROP INDEX uniq_usage_slot_piece',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;
