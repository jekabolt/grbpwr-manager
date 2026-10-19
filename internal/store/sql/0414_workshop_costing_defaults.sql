-- +migrate Up

-- ДЕФОЛТЫ СМЕТЫ И «MADE IN» в доме настроек цеха (0272): валюта, ставка минуты, накладные %, брак %,
-- целевая маржа %, страна производства. Читаются ТОЛЬКО для предзаполнения новой сметы и строки
-- «made in» — пересчёта ничего не вызывают. Шесть новых колонок, все NULL (= не настроено).
--
-- Только ADD COLUMN, без CHECK: полосы правдоподобия живут в Go (entity/workshop.go) — на старой
-- таблице ограничения в схему не добавляем. Колонки новые и пустые, ретроактивной проверки нет.
--
-- ИДЕМПОТЕНТНОСТЬ: каждая колонка под проверкой в information_schema, один оператор на строку.

SET @need := (SELECT COUNT(*) = 0 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'workshop_settings'
      AND COLUMN_NAME = 'costing_currency');
SET @sql := IF(@need,
    'ALTER TABLE workshop_settings ADD COLUMN costing_currency VARCHAR(4) NULL COMMENT ''ISO 4217 валюта денежных дефолтов сметы; NULL = не настроено'' AFTER cutting_table_width_cm',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

SET @need := (SELECT COUNT(*) = 0 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'workshop_settings'
      AND COLUMN_NAME = 'labour_rate_per_min');
SET @sql := IF(@need,
    'ALTER TABLE workshop_settings ADD COLUMN labour_rate_per_min DECIMAL(12,4) NULL COMMENT ''стоимость минуты работы (SAM) в costing_currency; NULL = не настроено'' AFTER costing_currency',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

SET @need := (SELECT COUNT(*) = 0 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'workshop_settings'
      AND COLUMN_NAME = 'overhead_pct');
SET @sql := IF(@need,
    'ALTER TABLE workshop_settings ADD COLUMN overhead_pct DECIMAL(6,2) NULL COMMENT ''накладные, % от прямых затрат; NULL = не настроено'' AFTER labour_rate_per_min',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

SET @need := (SELECT COUNT(*) = 0 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'workshop_settings'
      AND COLUMN_NAME = 'defect_percent');
SET @sql := IF(@need,
    'ALTER TABLE workshop_settings ADD COLUMN defect_percent DECIMAL(6,2) NULL COMMENT ''брак / запас, %; NULL = не настроено'' AFTER overhead_pct',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

SET @need := (SELECT COUNT(*) = 0 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'workshop_settings'
      AND COLUMN_NAME = 'target_margin_pct');
SET @sql := IF(@need,
    'ALTER TABLE workshop_settings ADD COLUMN target_margin_pct DECIMAL(6,2) NULL COMMENT ''целевая валовая маржа, %; NULL = не настроено'' AFTER defect_percent',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

SET @need := (SELECT COUNT(*) = 0 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'workshop_settings'
      AND COLUMN_NAME = 'country_of_origin');
SET @sql := IF(@need,
    'ALTER TABLE workshop_settings ADD COLUMN country_of_origin CHAR(2) NULL COMMENT ''ISO 3166-1 alpha-2 страна производства (made in); NULL = не настроено'' AFTER target_margin_pct',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

-- +migrate Down

-- Настройки, вводятся заново; гвард на каждую колонку.

SET @have := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'workshop_settings'
      AND COLUMN_NAME = 'country_of_origin');
SET @sql := IF(@have > 0,
    'ALTER TABLE workshop_settings DROP COLUMN country_of_origin',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

SET @have := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'workshop_settings'
      AND COLUMN_NAME = 'target_margin_pct');
SET @sql := IF(@have > 0,
    'ALTER TABLE workshop_settings DROP COLUMN target_margin_pct',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

SET @have := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'workshop_settings'
      AND COLUMN_NAME = 'defect_percent');
SET @sql := IF(@have > 0,
    'ALTER TABLE workshop_settings DROP COLUMN defect_percent',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

SET @have := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'workshop_settings'
      AND COLUMN_NAME = 'overhead_pct');
SET @sql := IF(@have > 0,
    'ALTER TABLE workshop_settings DROP COLUMN overhead_pct',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

SET @have := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'workshop_settings'
      AND COLUMN_NAME = 'labour_rate_per_min');
SET @sql := IF(@have > 0,
    'ALTER TABLE workshop_settings DROP COLUMN labour_rate_per_min',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

SET @have := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'workshop_settings'
      AND COLUMN_NAME = 'costing_currency');
SET @sql := IF(@have > 0,
    'ALTER TABLE workshop_settings DROP COLUMN costing_currency',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;
