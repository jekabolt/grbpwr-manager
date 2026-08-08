-- +migrate Up

-- Ф5б.1 + Ф5б.2. ЛОТ И ФАКТ НА НАСТИЛЕ. Настил (0281) до сих пор описывал только ПЛАН: маркеры,
-- слои, концевые потери. Две вещи, которые цех знает и которым негде было лечь:
--
--   lot_id / lot_code — С КАКОГО РУЛОНА настилали. Нужно двум читателям сразу: сверке ширины
--     маркера с ИЗМЕРЕННОЙ шириной лота (0269 measured_width_cm, проверка lay_lot_width) и резерву
--     под перекрой, который обязан браться ИЗ ТОГО ЖЕ лота — перекрой через месяц из другой партии
--     виден на изделии как дефект.
--   actual_* — сколько ткани РЕАЛЬНО ушло, замерено рулоном до/после либо взвешиванием.
--
-- ПОЧЕМУ ФАКТ НА НАСТИЛЕ, А НЕ НА ПРОГОНЕ. production_run.actual_wastage_percent уже существует и
-- НЕ трогается: оно живёт на ПРОГОНЕ и обслуживает немаркерные строки материального плана
-- (маркерные нормы его игнорируют по построению). Факт же измеряется на НАСТИЛЕ — это один
-- конкретный рулон, разложенный в один конкретный настил. Переозначить старое поле значило бы
-- заставить одно число отвечать на два разных вопроса.
--
-- ЗАЧЕМ ФАКТ ВООБЩЕ. Коэффициент раскроя артикула (0270) до первого факта — догадка в одежде числа.
-- Калибровка сравнивает факт с ПЛАНОМ НАСТИЛА, и круга здесь нет: решение Ф4 постановило, что
-- коэффициент на пути настилов НЕ применяется, поэтому план настила — чистая геометрия (длина ×
-- слои + концевые потери), а дрейф факт/план — ровно та величина, которую коэффициент обязан
-- покрыть (усадка, обход пороков, сращивание). Применяйся коэффициент к настилу, калибровка стала
-- бы круговой.
--
-- ПОЧЕМУ lot_id — SET NULL, И ЧЕМ ЭТО ОПЛАЧЕНО. Та же развилка, что у bom_item_id в 0281:32-38.
-- RESTRICT означал бы, что лот НАВСЕГДА нельзя удалить, если с него хоть раз настилали. Цена
-- SET NULL — настил, потерявший рулон; она оплачена NOT NULL-снимком lot_code: настил всегда может
-- НАЗВАТЬ пропавший лот и никогда не молчит. Снимок не является ключом поиска и ни с чем не
-- джойнится — он существует только чтобы произнести имя.
--
-- ИНВАРИАНТ «ФАКТ ЦЕЛИКОМ ИЛИ НИКАК» (chk_prlay_actual_complete). actual_qty задан ⇒ actual_uom и
-- actual_method заданы тоже. Это CHECK, а не соглашение: факт без единицы — число, которое нечем
-- сложить, а факт без метода замера — число, о точности которого нечего сказать (рулон до/после и
-- взвешивание врут по-разному). Импликация ОДНОСТОРОННЯЯ: единица, выбранная в форме раньше
-- количества, — это наполовину заполненная форма, а не ложь, и запрещать её незачем.
--
-- ЕДИНИЦА ФАКТА. Колонка остаётся свободным текстом с проверкой на непустоту и нижний регистр, а
-- ЗАКРЫТОСТЬ словаря (перечень Ф5а.3, entity.MaterialUnit) держится ПРОВОДОМ: на Insert едет
-- перечисление MaterialUnit, то есть клиент физически не может прислать «метры». Так же устроен
-- каталожный material.unit (0271): хранение свободное, нормализация на чтении. Третьей копии
-- словаря в SQL сознательно не заводится — 0271 и entity уже приходится держать синхронными
-- отдельным линтом, и каждая новая копия — ещё один шов, который однажды разойдётся.
--
-- Идемпотентность (CLAUDE.md): каждый DDL под собственной проверкой в information_schema,
-- PREPARE/EXECUTE/DEALLOCATE по одному оператору на строку. Индекс заводится ДО внешнего ключа,
-- чтобы MySQL переиспользовал его вместо автосозданного, и Down знал стабильное имя.

SET @need_col := (SELECT COUNT(*) = 0 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'production_run_lay' AND COLUMN_NAME = 'lot_id');
SET @sql := IF(@need_col,
    'ALTER TABLE production_run_lay ADD COLUMN lot_id INT NULL AFTER bom_line_key',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

SET @need_col := (SELECT COUNT(*) = 0 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'production_run_lay' AND COLUMN_NAME = 'lot_code');
SET @sql := IF(@need_col,
    'ALTER TABLE production_run_lay ADD COLUMN lot_code VARCHAR(64) NOT NULL DEFAULT '''' AFTER lot_id',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

SET @need_col := (SELECT COUNT(*) = 0 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'production_run_lay' AND COLUMN_NAME = 'actual_qty');
SET @sql := IF(@need_col,
    'ALTER TABLE production_run_lay ADD COLUMN actual_qty DECIMAL(12,3) NULL AFTER lot_code',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

SET @need_col := (SELECT COUNT(*) = 0 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'production_run_lay' AND COLUMN_NAME = 'actual_uom');
SET @sql := IF(@need_col,
    'ALTER TABLE production_run_lay ADD COLUMN actual_uom VARCHAR(16) NULL AFTER actual_qty',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

SET @need_col := (SELECT COUNT(*) = 0 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'production_run_lay' AND COLUMN_NAME = 'actual_method');
SET @sql := IF(@need_col,
    'ALTER TABLE production_run_lay ADD COLUMN actual_method VARCHAR(24) NULL AFTER actual_uom',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

SET @need_col := (SELECT COUNT(*) = 0 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'production_run_lay' AND COLUMN_NAME = 'actual_by');
SET @sql := IF(@need_col,
    'ALTER TABLE production_run_lay ADD COLUMN actual_by VARCHAR(255) NOT NULL DEFAULT '''' AFTER actual_method',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

SET @need_col := (SELECT COUNT(*) = 0 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'production_run_lay' AND COLUMN_NAME = 'actual_at');
SET @sql := IF(@need_col,
    'ALTER TABLE production_run_lay ADD COLUMN actual_at TIMESTAMP NULL AFTER actual_by',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

SET @need_idx := (SELECT COUNT(*) = 0 FROM information_schema.STATISTICS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'production_run_lay' AND INDEX_NAME = 'idx_prlay_lot');
SET @sql := IF(@need_idx,
    'ALTER TABLE production_run_lay ADD INDEX idx_prlay_lot (lot_id)',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

SET @need_fk := (SELECT COUNT(*) = 0 FROM information_schema.TABLE_CONSTRAINTS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'production_run_lay'
      AND CONSTRAINT_NAME = 'fk_prlay_lot');
SET @sql := IF(@need_fk,
    'ALTER TABLE production_run_lay ADD CONSTRAINT fk_prlay_lot FOREIGN KEY (lot_id) REFERENCES material_lot (id) ON DELETE SET NULL',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

SET @need_chk := (SELECT COUNT(*) = 0 FROM information_schema.TABLE_CONSTRAINTS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'production_run_lay'
      AND CONSTRAINT_NAME = 'chk_prlay_actual_qty');
SET @sql := IF(@need_chk,
    'ALTER TABLE production_run_lay ADD CONSTRAINT chk_prlay_actual_qty CHECK (actual_qty IS NULL OR actual_qty > 0)',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

SET @need_chk := (SELECT COUNT(*) = 0 FROM information_schema.TABLE_CONSTRAINTS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'production_run_lay'
      AND CONSTRAINT_NAME = 'chk_prlay_actual_complete');
SET @sql := IF(@need_chk,
    'ALTER TABLE production_run_lay ADD CONSTRAINT chk_prlay_actual_complete CHECK (actual_qty IS NULL OR (actual_uom IS NOT NULL AND actual_method IS NOT NULL))',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

SET @need_chk := (SELECT COUNT(*) = 0 FROM information_schema.TABLE_CONSTRAINTS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'production_run_lay'
      AND CONSTRAINT_NAME = 'chk_prlay_actual_uom');
SET @sql := IF(@need_chk,
    'ALTER TABLE production_run_lay ADD CONSTRAINT chk_prlay_actual_uom CHECK (actual_uom IS NULL OR (actual_uom <> '''' AND STRCMP(CAST(actual_uom AS BINARY), CAST(LOWER(actual_uom) AS BINARY)) = 0))',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

SET @need_chk := (SELECT COUNT(*) = 0 FROM information_schema.TABLE_CONSTRAINTS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'production_run_lay'
      AND CONSTRAINT_NAME = 'chk_prlay_actual_method');
SET @sql := IF(@need_chk,
    'ALTER TABLE production_run_lay ADD CONSTRAINT chk_prlay_actual_method CHECK (actual_method IS NULL OR (actual_method REGEXP ''^(roll_before_after|weighed)$'' AND STRCMP(CAST(actual_method AS BINARY), CAST(LOWER(actual_method) AS BINARY)) = 0))',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

-- +migrate Down

-- Аддитивно и обратимо, порядок вынужденный: CHECK'и и внешний ключ читают колонки и обязаны уйти
-- раньше них. Каждый шаг под своей проверкой, чтобы недооткаченный файл перезапускался начисто.
--
-- ГВАРДА НА ДАННЫЕ ЗДЕСЬ СОЗНАТЕЛЬНО НЕТ, в отличие от 0286 и 0287. Ближайший прецедент — 0269,
-- добавившая ровно такой же класс данных (measured_width_cm — замер человека на складе) и роняющая
-- колонку без вопросов. Гварда защищает от потери ТАБЛИЦЫ рукописного плана (0281) или от отката,
-- который оставит схему заведомо противоречивой (0286: NOT NULL упал бы на строках, чей владелец —
-- прогон). Здесь ни того, ни другого: настилы целы, схема после отката согласована, теряется
-- добавленный этой же миграцией замер. Запирать аварийный откат ради этого — своя цена.

SET @has_chk := (SELECT COUNT(*) > 0 FROM information_schema.TABLE_CONSTRAINTS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'production_run_lay'
      AND CONSTRAINT_NAME = 'chk_prlay_actual_method');
SET @sql := IF(@has_chk,
    'ALTER TABLE production_run_lay DROP CHECK chk_prlay_actual_method',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

SET @has_chk := (SELECT COUNT(*) > 0 FROM information_schema.TABLE_CONSTRAINTS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'production_run_lay'
      AND CONSTRAINT_NAME = 'chk_prlay_actual_uom');
SET @sql := IF(@has_chk,
    'ALTER TABLE production_run_lay DROP CHECK chk_prlay_actual_uom',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

SET @has_chk := (SELECT COUNT(*) > 0 FROM information_schema.TABLE_CONSTRAINTS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'production_run_lay'
      AND CONSTRAINT_NAME = 'chk_prlay_actual_complete');
SET @sql := IF(@has_chk,
    'ALTER TABLE production_run_lay DROP CHECK chk_prlay_actual_complete',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

SET @has_chk := (SELECT COUNT(*) > 0 FROM information_schema.TABLE_CONSTRAINTS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'production_run_lay'
      AND CONSTRAINT_NAME = 'chk_prlay_actual_qty');
SET @sql := IF(@has_chk,
    'ALTER TABLE production_run_lay DROP CHECK chk_prlay_actual_qty',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

SET @has_fk := (SELECT COUNT(*) > 0 FROM information_schema.TABLE_CONSTRAINTS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'production_run_lay'
      AND CONSTRAINT_NAME = 'fk_prlay_lot');
SET @sql := IF(@has_fk,
    'ALTER TABLE production_run_lay DROP FOREIGN KEY fk_prlay_lot',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

SET @has_idx := (SELECT COUNT(*) > 0 FROM information_schema.STATISTICS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'production_run_lay' AND INDEX_NAME = 'idx_prlay_lot');
SET @sql := IF(@has_idx,
    'ALTER TABLE production_run_lay DROP INDEX idx_prlay_lot',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

SET @has_col := (SELECT COUNT(*) > 0 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'production_run_lay' AND COLUMN_NAME = 'actual_at');
SET @sql := IF(@has_col, 'ALTER TABLE production_run_lay DROP COLUMN actual_at', 'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

SET @has_col := (SELECT COUNT(*) > 0 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'production_run_lay' AND COLUMN_NAME = 'actual_by');
SET @sql := IF(@has_col, 'ALTER TABLE production_run_lay DROP COLUMN actual_by', 'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

SET @has_col := (SELECT COUNT(*) > 0 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'production_run_lay' AND COLUMN_NAME = 'actual_method');
SET @sql := IF(@has_col, 'ALTER TABLE production_run_lay DROP COLUMN actual_method', 'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

SET @has_col := (SELECT COUNT(*) > 0 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'production_run_lay' AND COLUMN_NAME = 'actual_uom');
SET @sql := IF(@has_col, 'ALTER TABLE production_run_lay DROP COLUMN actual_uom', 'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

SET @has_col := (SELECT COUNT(*) > 0 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'production_run_lay' AND COLUMN_NAME = 'actual_qty');
SET @sql := IF(@has_col, 'ALTER TABLE production_run_lay DROP COLUMN actual_qty', 'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

SET @has_col := (SELECT COUNT(*) > 0 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'production_run_lay' AND COLUMN_NAME = 'lot_code');
SET @sql := IF(@has_col, 'ALTER TABLE production_run_lay DROP COLUMN lot_code', 'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

SET @has_col := (SELECT COUNT(*) > 0 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'production_run_lay' AND COLUMN_NAME = 'lot_id');
SET @sql := IF(@has_col, 'ALTER TABLE production_run_lay DROP COLUMN lot_id', 'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;
