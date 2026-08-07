-- +migrate Up

-- ПРЕДЕЛ СТОПКИ (Ф4.8). Предел — САНТИМЕТРЫ, а не число слоёв: 30 слоёв шифона это 2 см, 30 слоёв
-- драпа — 30 см, нож 8" берёт около 15 (00-model.md:41-43). Живёт в доме настроек цеха рядом с
-- длиной стола, ровно в форме 0272: одна типизированная колонка, NULL = не настроено, потолок
-- абсурдности в CHECK. 100 см — не «бывает», а «столько не бывает»: самый длинный нож берёт около
-- 30, поэтому больше сотни это ошибка единицы, а не цех. Один предел на цех — решение Р5: пер-ножевые
-- пределы уточнение, под которое сегодня нет данных.
--
-- ИДЕМПОТЕНТНОСТЬ (CLAUDE.md): каждый шаг под собственной проверкой в information_schema,
-- PREPARE/EXECUTE/DEALLOCATE по одному оператору на строку (прод подключается БЕЗ multiStatements,
-- и контейнерный тест это маскирует). Все CHECK именованы явно — авто-имена <table>_chk_N
-- позиционны и дропать их по имени запрещено домовым правилом.
SET @need := (SELECT COUNT(*) = 0 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'workshop_settings'
      AND COLUMN_NAME = 'max_stack_height_cm');
SET @sql := IF(@need,
    'ALTER TABLE workshop_settings ADD COLUMN max_stack_height_cm DECIMAL(6,2) NULL COMMENT ''максимальная высота стопки, см; NULL = не настроено, и тогда проверки высоты НЕТ — не «ноль» и не «сколько угодно»'' AFTER cutting_table_length_cm',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

SET @need := (SELECT COUNT(*) = 0 FROM information_schema.TABLE_CONSTRAINTS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'workshop_settings'
      AND CONSTRAINT_NAME = 'chk_workshop_settings_stack_height');
SET @sql := IF(@need,
    'ALTER TABLE workshop_settings ADD CONSTRAINT chk_workshop_settings_stack_height CHECK (max_stack_height_cm IS NULL OR (max_stack_height_cm > 0 AND max_stack_height_cm <= 100))',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

-- ТОЛЩИНА ТКАНИ — опциональное поле АРТИКУЛА, по образцу cutting_coefficient (0270:4).
-- «Нет толщины — нет проверки, не догадка» (10-tasks.md:72): NULL здесь означает «не замерено», и
-- любой дефолт («ну примерно 0.5 мм») превратил бы отсутствие данных в вердикт.
SET @need := (SELECT COUNT(*) = 0 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'material' AND COLUMN_NAME = 'fabric_thickness_mm');
SET @sql := IF(@need,
    'ALTER TABLE material ADD COLUMN fabric_thickness_mm DECIMAL(6,3) NULL COMMENT ''толщина полотна в ОДИН слой, мм; NULL = не замерено ⇒ проверки высоты стопки нет'' AFTER cutting_coefficient',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

SET @need := (SELECT COUNT(*) = 0 FROM information_schema.TABLE_CONSTRAINTS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'material'
      AND CONSTRAINT_NAME = 'chk_material_fabric_thickness');
SET @sql := IF(@need,
    'ALTER TABLE material ADD CONSTRAINT chk_material_fabric_thickness CHECK (fabric_thickness_mm IS NULL OR (fabric_thickness_mm > 0 AND fabric_thickness_mm <= 50))',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

-- +migrate Down

-- Симметричные дропы под information_schema. Гварда нет сознательно: обе колонки — НАСТРОЙКИ, их
-- потеря восстанавливается вводом двух чисел, а не пересборкой работы. Гвард нужен там, где откат
-- уничтожает то, чего человек не введёт заново (0281, 0282). CHECK дропается ДО своей колонки:
-- обратный порядок — ошибка 3959 «Check constraint is refencing column».
SET @have := (SELECT COUNT(*) FROM information_schema.TABLE_CONSTRAINTS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'material'
      AND CONSTRAINT_NAME = 'chk_material_fabric_thickness');
SET @sql := IF(@have > 0,
    'ALTER TABLE material DROP CHECK chk_material_fabric_thickness',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

SET @have := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'material' AND COLUMN_NAME = 'fabric_thickness_mm');
SET @sql := IF(@have > 0,
    'ALTER TABLE material DROP COLUMN fabric_thickness_mm',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

SET @have := (SELECT COUNT(*) FROM information_schema.TABLE_CONSTRAINTS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'workshop_settings'
      AND CONSTRAINT_NAME = 'chk_workshop_settings_stack_height');
SET @sql := IF(@have > 0,
    'ALTER TABLE workshop_settings DROP CHECK chk_workshop_settings_stack_height',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

SET @have := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'workshop_settings'
      AND COLUMN_NAME = 'max_stack_height_cm');
SET @sql := IF(@have > 0,
    'ALTER TABLE workshop_settings DROP COLUMN max_stack_height_cm',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;
