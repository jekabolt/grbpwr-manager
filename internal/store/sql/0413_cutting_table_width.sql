-- +migrate Up

-- ШИРИНА СТОЛА РАСКРОЯ — партнёр длины (0272) в доме настроек цеха: модалка раскладки рисует стол в
-- масштабе. Та же форма, что 0283: одна типизированная колонка, NULL = не настроено, потолок
-- абсурдности в именованном CHECK. Колонка НОВАЯ и вся NULL, поэтому CHECK не проверяет никакой
-- истории (NULL проходит), и ретроактивный отказ прода невозможен.
--
-- ИДЕМПОТЕНТНОСТЬ: каждый шаг под проверкой в information_schema, один оператор на PREPARE.
SET @need := (SELECT COUNT(*) = 0 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'workshop_settings'
      AND COLUMN_NAME = 'cutting_table_width_cm');
SET @sql := IF(@need,
    'ALTER TABLE workshop_settings ADD COLUMN cutting_table_width_cm DECIMAL(6,2) NULL COMMENT ''ширина стола раскроя, см; NULL = не настроено'' AFTER cutting_table_length_cm',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

SET @need := (SELECT COUNT(*) = 0 FROM information_schema.TABLE_CONSTRAINTS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'workshop_settings'
      AND CONSTRAINT_NAME = 'chk_workshop_settings_table_width');
SET @sql := IF(@need,
    'ALTER TABLE workshop_settings ADD CONSTRAINT chk_workshop_settings_table_width CHECK (cutting_table_width_cm IS NULL OR (cutting_table_width_cm > 0 AND cutting_table_width_cm <= 1000))',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

-- +migrate Down

-- CHECK дропается ДО своей колонки (ошибка 3959 иначе). Гварда нет: это настройка, вводится заново.
SET @have := (SELECT COUNT(*) FROM information_schema.TABLE_CONSTRAINTS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'workshop_settings'
      AND CONSTRAINT_NAME = 'chk_workshop_settings_table_width');
SET @sql := IF(@have > 0,
    'ALTER TABLE workshop_settings DROP CHECK chk_workshop_settings_table_width',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

SET @have := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'workshop_settings'
      AND COLUMN_NAME = 'cutting_table_width_cm');
SET @sql := IF(@have > 0,
    'ALTER TABLE workshop_settings DROP COLUMN cutting_table_width_cm',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;
