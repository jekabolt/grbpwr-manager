-- +migrate Up

-- T7 волна 2 (процент раскроя по факту): штамп netto на настиле + провенанс процента на строке BOM.
--
-- ДВА РАЗНЫХ ЧИСЛА, И ЭТА МИГРАЦИЯ ОБСЛУЖИВАЕТ ПЕРВОЕ. bom_item.wastage_percent гроссит NETTO-норму
-- (netto = площадь деталей ÷ раскройная ширина) и оплачивает межлекальные выпады + концы настила +
-- усадку/пороки. material.cutting_coefficient (0270) гроссит ИЗМЕРЕННУЮ длину раскладки и оплачивает
-- ТОЛЬКО усадку/пороки. У них разные знаменатели, и калибруются они разными расчётами: коэффициент —
-- медианой факт/план-геометрия (Ф5б.3), процент — медианой факт/netto (этот файл и есть её опора).
--
-- production_run_lay.netto_qty — ШТАМП ЗНАМЕНАТЕЛЯ, взятый В МОМЕНТ записи факта replace-путём
-- SaveLay: netto настила = Σ по секциям (слои × Σ по составу раскладки (кол-во размера × dxf-норма
-- размера)). Штамповать обязательно: когда карточка переходит на marker-норму (целевое состояние
-- системы), её dxf-норма перезаписывается — и прошлые настилы теряют netto, корпус фактов испаряется
-- ровно по мере правильного использования системы. NULL = «на момент замера netto вычислить было не
-- из чего» (нет dxf-нормы слота, состав раскладки неизвестен, единицы несводимы) — расчёт предложения
-- тогда пробует живую dxf-норму, а её отсутствие называет причиной, не нулём.
-- Единица значения — единица замера (actual_uom): сервер штампует ТОЛЬКО когда единицы сходятся,
-- поэтому actual_qty / netto_qty делится без перевода.
SET @has_col := (
    SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'production_run_lay' AND COLUMN_NAME = 'netto_qty');
SET @sql := IF(@has_col = 0,
    'ALTER TABLE production_run_lay ADD COLUMN netto_qty DECIMAL(12,3) NULL AFTER actual_at',
    'SELECT 1');
PREPARE stmt FROM @sql;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- БАЗИС ШТАМПА: копия факта (величина + единица), НАД которым штамп считался. Штамп самопроверяем
-- (правки ревью T7в2, MAJOR 4): при откате DO старый бинарь правит actual_qty/actual_uom, не зная
-- про netto_qty, — и после повторного выката новый код обязан УВИДЕТЬ, что факт с тех пор менялся,
-- и отбросить штамп с причиной, а не поверить. Базис и есть это свидетельство: netto верят только
-- когда базис совпадает с сегодняшним фактом (entity.TrustedNettoStamp — единственный читатель).
SET @has_col := (
    SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'production_run_lay' AND COLUMN_NAME = 'netto_basis_qty');
SET @sql := IF(@has_col = 0,
    'ALTER TABLE production_run_lay ADD COLUMN netto_basis_qty DECIMAL(12,3) NULL AFTER netto_qty',
    'SELECT 1');
PREPARE stmt FROM @sql;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @has_col := (
    SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'production_run_lay' AND COLUMN_NAME = 'netto_basis_uom');
SET @sql := IF(@has_col = 0,
    'ALTER TABLE production_run_lay ADD COLUMN netto_basis_uom VARCHAR(16) NULL AFTER netto_basis_qty',
    'SELECT 1');
PREPARE stmt FROM @sql;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- Именованный CHECK (правило 0079: никаких позиционных <table>_chk_N). Ретроактивного риска нет:
-- колонки новые, вся история NULL и удовлетворяет тривиально; старый бинарь при откате DO их не
-- пишет — NULL проходит.
SET @has_chk := (
    SELECT COUNT(*) FROM information_schema.TABLE_CONSTRAINTS
    WHERE CONSTRAINT_SCHEMA = DATABASE() AND TABLE_NAME = 'production_run_lay'
      AND CONSTRAINT_NAME = 'chk_prlay_netto_qty');
SET @sql := IF(@has_chk = 0,
    'ALTER TABLE production_run_lay ADD CONSTRAINT chk_prlay_netto_qty CHECK (netto_qty IS NULL OR netto_qty > 0)',
    'SELECT 1');
PREPARE stmt FROM @sql;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- Штамп без базиса — не штамп: новый код пишет все три колонки одним оператором, и строка с netto
-- без базиса могла возникнуть только вне этого пути. CHECK держит инвариант на уровне схемы.
SET @has_chk := (
    SELECT COUNT(*) FROM information_schema.TABLE_CONSTRAINTS
    WHERE CONSTRAINT_SCHEMA = DATABASE() AND TABLE_NAME = 'production_run_lay'
      AND CONSTRAINT_NAME = 'chk_prlay_netto_basis');
SET @sql := IF(@has_chk = 0,
    'ALTER TABLE production_run_lay ADD CONSTRAINT chk_prlay_netto_basis CHECK (netto_qty IS NULL OR (netto_basis_qty IS NOT NULL AND netto_basis_uom IS NOT NULL))',
    'SELECT 1');
PREPARE stmt FROM @sql;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- tech_card_bom_item.wastage_source — ОТКУДА ВЗЯЛОСЬ ЧИСЛО в wastage_percent: 'manual' (введено
-- руками — умолчание, и оно честно описывает всю историю колонки) или 'lays' (применено из
-- предложения «медиана по фактам настилов», GetBomWastageSuggestion). Тот же приём, что
-- consumption_source (0261) и price_source: провенанс — метаданные о значении, В ДАЙДЖЕСТ ПОДПИСИ
-- НЕ ВХОДИТ (само значение wastage_percent в подписи уже есть).
SET @has_col := (
    SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_bom_item' AND COLUMN_NAME = 'wastage_source');
SET @sql := IF(@has_col = 0,
    'ALTER TABLE tech_card_bom_item ADD COLUMN wastage_source VARCHAR(16) NOT NULL DEFAULT ''manual'' AFTER wastage_percent',
    'SELECT 1');
PREPARE stmt FROM @sql;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- Ретроактивно безопасен: колонка только что получила DEFAULT 'manual' на всей истории.
SET @has_chk := (
    SELECT COUNT(*) FROM information_schema.TABLE_CONSTRAINTS
    WHERE CONSTRAINT_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_bom_item'
      AND CONSTRAINT_NAME = 'chk_bom_item_wastage_source');
SET @sql := IF(@has_chk = 0,
    'ALTER TABLE tech_card_bom_item ADD CONSTRAINT chk_bom_item_wastage_source CHECK (wastage_source IN (''manual'', ''lays''))',
    'SELECT 1');
PREPARE stmt FROM @sql;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- Штамп применения: ПО СКОЛЬКИМ настилам стояла медиана в момент применения (аудит «медиана по N
-- раскроям», который обязан пережить рост корпуса фактов) и КОГДА сервер её проштамповал. Оба NULL
-- на 'manual' — ручное число не притворяется посчитанным.
SET @has_col := (
    SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_bom_item' AND COLUMN_NAME = 'wastage_lay_count');
SET @sql := IF(@has_col = 0,
    'ALTER TABLE tech_card_bom_item ADD COLUMN wastage_lay_count INT NULL AFTER wastage_source',
    'SELECT 1');
PREPARE stmt FROM @sql;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @has_col := (
    SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_bom_item' AND COLUMN_NAME = 'wastage_applied_at');
SET @sql := IF(@has_col = 0,
    'ALTER TABLE tech_card_bom_item ADD COLUMN wastage_applied_at TIMESTAMP NULL DEFAULT NULL AFTER wastage_lay_count',
    'SELECT 1');
PREPARE stmt FROM @sql;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- Самопроверка бейджа (правки ревью T7в2, MAJOR 4): копия wastage_percent В МОМЕНТ применения
-- медианы. Старый бинарь при откате DO правит wastage_percent, не зная про провенанс, — бейдж
-- 'lays' остался бы на числе, которого калибровка не давала. Читатель верит 'lays' только когда
-- applied_percent совпадает с wastage_percent (entity.EffectiveBomWastageProvenance), иначе строка
-- читается как manual. NULL на 'manual' и на строках старше 0296.
SET @has_col := (
    SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_bom_item' AND COLUMN_NAME = 'wastage_applied_percent');
SET @sql := IF(@has_col = 0,
    'ALTER TABLE tech_card_bom_item ADD COLUMN wastage_applied_percent DECIMAL(5,2) NULL AFTER wastage_applied_at',
    'SELECT 1');
PREPARE stmt FROM @sql;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- +migrate Down

SET @has_chk := (
    SELECT COUNT(*) FROM information_schema.TABLE_CONSTRAINTS
    WHERE CONSTRAINT_SCHEMA = DATABASE() AND TABLE_NAME = 'production_run_lay'
      AND CONSTRAINT_NAME = 'chk_prlay_netto_basis');
SET @sql := IF(@has_chk > 0, 'ALTER TABLE production_run_lay DROP CHECK chk_prlay_netto_basis', 'SELECT 1');
PREPARE stmt FROM @sql;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @has_chk := (
    SELECT COUNT(*) FROM information_schema.TABLE_CONSTRAINTS
    WHERE CONSTRAINT_SCHEMA = DATABASE() AND TABLE_NAME = 'production_run_lay'
      AND CONSTRAINT_NAME = 'chk_prlay_netto_qty');
SET @sql := IF(@has_chk > 0, 'ALTER TABLE production_run_lay DROP CHECK chk_prlay_netto_qty', 'SELECT 1');
PREPARE stmt FROM @sql;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @has_col := (
    SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'production_run_lay' AND COLUMN_NAME = 'netto_basis_uom');
SET @sql := IF(@has_col > 0, 'ALTER TABLE production_run_lay DROP COLUMN netto_basis_uom', 'SELECT 1');
PREPARE stmt FROM @sql;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @has_col := (
    SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'production_run_lay' AND COLUMN_NAME = 'netto_basis_qty');
SET @sql := IF(@has_col > 0, 'ALTER TABLE production_run_lay DROP COLUMN netto_basis_qty', 'SELECT 1');
PREPARE stmt FROM @sql;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @has_col := (
    SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'production_run_lay' AND COLUMN_NAME = 'netto_qty');
SET @sql := IF(@has_col > 0, 'ALTER TABLE production_run_lay DROP COLUMN netto_qty', 'SELECT 1');
PREPARE stmt FROM @sql;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @has_col := (
    SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_bom_item' AND COLUMN_NAME = 'wastage_applied_percent');
SET @sql := IF(@has_col > 0, 'ALTER TABLE tech_card_bom_item DROP COLUMN wastage_applied_percent', 'SELECT 1');
PREPARE stmt FROM @sql;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @has_chk := (
    SELECT COUNT(*) FROM information_schema.TABLE_CONSTRAINTS
    WHERE CONSTRAINT_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_bom_item'
      AND CONSTRAINT_NAME = 'chk_bom_item_wastage_source');
SET @sql := IF(@has_chk > 0, 'ALTER TABLE tech_card_bom_item DROP CHECK chk_bom_item_wastage_source', 'SELECT 1');
PREPARE stmt FROM @sql;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @has_col := (
    SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_bom_item' AND COLUMN_NAME = 'wastage_applied_at');
SET @sql := IF(@has_col > 0, 'ALTER TABLE tech_card_bom_item DROP COLUMN wastage_applied_at', 'SELECT 1');
PREPARE stmt FROM @sql;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @has_col := (
    SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_bom_item' AND COLUMN_NAME = 'wastage_lay_count');
SET @sql := IF(@has_col > 0, 'ALTER TABLE tech_card_bom_item DROP COLUMN wastage_lay_count', 'SELECT 1');
PREPARE stmt FROM @sql;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @has_col := (
    SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_bom_item' AND COLUMN_NAME = 'wastage_source');
SET @sql := IF(@has_col > 0, 'ALTER TABLE tech_card_bom_item DROP COLUMN wastage_source', 'SELECT 1');
PREPARE stmt FROM @sql;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;
