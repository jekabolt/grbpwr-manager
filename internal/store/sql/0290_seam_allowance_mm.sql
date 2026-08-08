-- +migrate Up

-- ПРИПУСК ПЕРЕЕЗЖАЕТ В МИЛЛИМЕТРЫ. Четыре колонки, во всех значение УМНОЖАЕТСЯ на 10, а имя меняет
-- суффикс вместе с единицей.
--
-- ЗАЧЕМ. Цепочка припуска состоит из трёх звеньев — цех, карточка, шаг, — и разрыв операций (0289)
-- добавил третье. Если звенья говорят в разных единицах, между стандартом и шагом, который обязан
-- его соблюсти, поселяется конверсия; а конверсия, размноженная по двум местам, даёт десятикратную
-- ошибку в норме ткани по всему периметру каждой детали. Одна единица на всю цепочку убирает саму
-- возможность.
--
-- ГРАНИЦА РЕШЕНИЯ, и она НЕ «всё в миллиметры». Припуск и зазор шва — миллиметры; размер полотна,
-- длина раскройного стола (cutting_table_length_cm, 0272), кромка (selvedge_cm) и предел высоты
-- стопки (max_stack_height_cm) остаются САНТИМЕТРАМИ: стол в 12 метров читается как 1200 см, а не
-- 12000 мм. Суффикс `_cm`/`_mm` в имени каждой колонки — единственное, что эти две группы разделяет,
-- поэтому переименование здесь обязательно и не косметика.
--
-- ГЕОМЕТРИЯ ДВИЖКА РАСКЛАДКИ ОСТАЁТСЯ В САНТИМЕТРАХ и в этой миграции не участвует вовсе: это
-- система координат (контуры, габариты, размещения, допуски замера), а не поле с единицей.
-- Конвертируют ровно две названные функции на её границе, в клиенте.
--
-- ПОЧЕМУ DECIMAL(6,1), А НЕ (6,2). В сантиметрах колонка хранила сотые, то есть 0.01 см = 0.1 мм.
-- Десятая доля миллиметра — ровно та же точность, ни бита не теряется. Дальше дробить нечего: замер
-- по DXF сам округляется до сотых сантиметра.
--
-- НОЛЬ ОСТАЁТСЯ ЗАКОННЫМ ВО ВСЕХ ЧЕТЫРЁХ, и 0 * 10 = 0 это сохраняет. Ноль значит «выкройки несут
-- линию кроя, офсет не нужен», NULL значит «не настроено», и ни один CHECK ниже не требует > 0 —
-- требование положительности превратило бы законную настройку цеха в ошибку данных.
--
-- ПОТОЛОК 100. Прежний был 10 см; 10 × 10 = 100 мм. Три копии этого числа обязаны двигаться вместе:
-- CHECK'и здесь, entity.MaxSeamAllowanceMm и клиентская MAX_SEAM_ALLOWANCE_MM.
--
-- ИДЕМПОТЕНТНОСТЬ: каждая колонка переезжает четырьмя шагами (ADD → UPDATE → DROP → CHECK), и КАЖДЫЙ
-- под своей проверкой information_schema. Порядок важен: UPDATE стоит между ADD и DROP, поэтому
-- падение после ADD оставляет обе колонки, и повторный прогон досчитает UPDATE, а не потеряет
-- данные. Проверка перед UPDATE смотрит на наличие СТАРОЙ колонки — если её уже нет, значения
-- перенесены, и повтор не умножит их второй раз. Это единственная опасность файла, и она закрыта
-- именно так.

-- --- 1. workshop_settings.default_seam_allowance_cm -> _mm --------------------------------------
SET @has := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'workshop_settings'
      AND COLUMN_NAME = 'default_seam_allowance_mm');
SET @ddl := IF(@has = 0,
    'ALTER TABLE workshop_settings ADD COLUMN default_seam_allowance_mm DECIMAL(6,1) NULL COMMENT ''припуск на шов по умолчанию, ММ; NULL = не настроено; 0 — ЗАКОННОЕ значение: выкройки цеха несут линию кроя'' AFTER cutting_table_length_cm',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @old := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'workshop_settings'
      AND COLUMN_NAME = 'default_seam_allowance_cm');
SET @ddl := IF(@old = 1,
    'UPDATE workshop_settings SET default_seam_allowance_mm = default_seam_allowance_cm * 10 WHERE default_seam_allowance_cm IS NOT NULL',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @ddl := IF(@old = 1,
    'ALTER TABLE workshop_settings DROP CHECK chk_workshop_settings_seam_allowance, DROP COLUMN default_seam_allowance_cm',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @chk := (SELECT COUNT(*) FROM information_schema.TABLE_CONSTRAINTS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'workshop_settings'
      AND CONSTRAINT_NAME = 'chk_workshop_settings_seam_allowance_mm');
SET @ddl := IF(@chk = 0,
    'ALTER TABLE workshop_settings ADD CONSTRAINT chk_workshop_settings_seam_allowance_mm CHECK (default_seam_allowance_mm IS NULL OR (default_seam_allowance_mm >= 0 AND default_seam_allowance_mm <= 100))',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- --- 2. tech_card.required_seam_allowance_cm -> _mm ---------------------------------------------
SET @has := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card'
      AND COLUMN_NAME = 'required_seam_allowance_mm');
SET @ddl := IF(@has = 0,
    'ALTER TABLE tech_card ADD COLUMN required_seam_allowance_mm DECIMAL(6,1) NULL COMMENT ''требуемый припуск ЭТОЙ карточки, ММ; NULL = брать цеховой по умолчанию; 0 законен (выкройки модели несут линию кроя). Голова каскада: цех -> карточка -> шаг''',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @old := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card'
      AND COLUMN_NAME = 'required_seam_allowance_cm');
SET @ddl := IF(@old = 1,
    'UPDATE tech_card SET required_seam_allowance_mm = required_seam_allowance_cm * 10 WHERE required_seam_allowance_cm IS NOT NULL',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @ddl := IF(@old = 1,
    'ALTER TABLE tech_card DROP CHECK chk_tc_required_seam_allowance, DROP COLUMN required_seam_allowance_cm',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @chk := (SELECT COUNT(*) FROM information_schema.TABLE_CONSTRAINTS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card'
      AND CONSTRAINT_NAME = 'chk_tc_required_seam_allowance_mm');
SET @ddl := IF(@chk = 0,
    'ALTER TABLE tech_card ADD CONSTRAINT chk_tc_required_seam_allowance_mm CHECK (required_seam_allowance_mm IS NULL OR (required_seam_allowance_mm >= 0 AND required_seam_allowance_mm <= 100))',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- --- 3+4. tech_card_marker: seam_allowance_cm / contour_allowance_cm -> _mm ---------------------
--
-- ЭТИ ДВЕ — ЕДИНСТВЕННЫЕ ВО ВСЁМ РАЗРЫВЕ, ГДЕ ЕСТЬ ЧТО КОНВЕРТИРОВАТЬ. Раскладки снимают с Ф3, они
-- на бете и на проде, и их значения реальны. Обе едут ОДНИМ ALTER: их сумма и есть припуск, лежащий
-- на ткани (0276), и половина, уехавшая в миллиметры при второй половине в сантиметрах, дала бы
-- гейту годности сумму, которая выглядит правдоподобно и неверна в одиннадцать раз.
SET @has := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_marker'
      AND COLUMN_NAME = 'seam_allowance_mm');
SET @ddl := IF(@has = 0,
    'ALTER TABLE tech_card_marker
        ADD COLUMN seam_allowance_mm DECIMAL(6,1) NULL COMMENT ''припуск, ДОБАВЛЕННЫЙ раздутием контура наружу, ММ; 0 = не добавляли; NULL = не записано (старая норма)'' AFTER edge_margin_cm,
        ADD COLUMN contour_allowance_mm DECIMAL(6,1) NULL COMMENT ''припуск, УЖЕ содержащийся в разложенном контуре, замерен по файлу, ММ; 0 = разложена линия шва; >0 = линия кроя; NULL = замерить было нечем'' AFTER seam_allowance_mm',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @old := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_marker'
      AND COLUMN_NAME = 'seam_allowance_cm');
SET @ddl := IF(@old = 1,
    'UPDATE tech_card_marker SET seam_allowance_mm = seam_allowance_cm * 10, contour_allowance_mm = contour_allowance_cm * 10 WHERE seam_allowance_cm IS NOT NULL OR contour_allowance_cm IS NOT NULL',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- chk_tcm_allowance_nonneg (0276) СТОИТ НА ОБЕИХ СТАРЫХ КОЛОНКАХ, и MySQL отвечает 3959 «column
-- cannot be dropped or renamed» на попытку убрать колонку из-под живого CHECK'а. Снимается ТЕМ ЖЕ
-- ALTER'ом, что и колонки: разными операторами это два коммита, и падение между ними оставило бы
-- таблицу с проверкой, ссылающейся на колонку, которой уже нет.
--
-- Поймано прогоном миграций на одноразовом контейнере. На бете этого видно НЕ БЫЛО: DigitalOcean
-- при провале сборки продолжает отдавать предыдущий деплой, поэтому /readyz отвечал 200 от СТАРОГО
-- билда, и «зелёная» бета означала бы ровно ничего.
SET @ddl := IF(@old = 1,
    'ALTER TABLE tech_card_marker DROP CHECK chk_tcm_allowance_nonneg, DROP CHECK chk_tcm_no_double_allowance, DROP COLUMN seam_allowance_cm, DROP COLUMN contour_allowance_cm',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- ОБА CHECK'А ВОЗВРАЩАЮТСЯ, В МИЛЛИМЕТРАХ, ОДНИМ ALTER'ОМ. ЗАПРЕТ ДВОЙНОГО ПРИПУСКА — перевыпуск
-- инварианта 0276 §5.3, и его нельзя было просто снять: раскладка не может одновременно лежать по
-- линии кроя (contour > 0) и быть раздутой офсетом (seam > 0), иначе припуск посчитан дважды, а
-- длина настила завышена по всему периметру каждой детали. NULL проходит НАМЕРЕННО: UNKNOWN у MySQL
-- не нарушает CHECK, и это честная ветка «замерить было нечем».
--
-- ВЕРХНЕЙ ГРАНИЦЫ ЗДЕСЬ НЕТ, И ЭТО ИСПРАВЛЕНИЕ. Первая редакция файла требовала <= 100 мм. Потолок
-- припуска — проверка ЕДИНИЦ («не записали ли сантиметры в миллиметровую колонку»), а не физический
-- закон, и предшественник (chk_tcm_allowance_nonneg, 0276) требовал только >= 0. MySQL проверяет
-- ADD CONSTRAINT по ВСЕМ существующим строкам, поэтому одна историческая раскладка с припуском
-- больше 10 см уронила бы миграцию (3819) — а с MYSQL_AUTOMIGRATE=true это остановка старта прода,
-- автоматический откат DO и лгущий /readyz. Проверять единицу задним числом по данным, записанным
-- когда правила ещё не было, — не наше дело: потолок теперь живёт на ЗАПИСИ, в
-- entity.ValidateMarkerAllowanceMm, где он отвечает внятным field violation, а не кодом 3819.
-- 0291 приводит бету (там строгая редакция уже применилась) к этому же состоянию.
SET @chk := (SELECT COUNT(*) FROM information_schema.TABLE_CONSTRAINTS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_marker'
      AND CONSTRAINT_NAME = 'chk_marker_allowance_mm');
SET @ddl := IF(@chk = 0,
    'ALTER TABLE tech_card_marker
        ADD CONSTRAINT chk_marker_allowance_mm CHECK ((seam_allowance_mm IS NULL OR seam_allowance_mm >= 0) AND (contour_allowance_mm IS NULL OR contour_allowance_mm >= 0)),
        ADD CONSTRAINT chk_tcm_no_double_allowance_mm CHECK (seam_allowance_mm IS NULL OR contour_allowance_mm IS NULL OR seam_allowance_mm = 0 OR contour_allowance_mm = 0)',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- +migrate Down
--
-- Симметрично, с делением на 10. Деление точное: значения пришли умножением на 10 из колонки с
-- сотыми, поэтому обратный ход возвращает ровно исходное число, а не округление.
SET @back := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_marker'
      AND COLUMN_NAME = 'seam_allowance_mm');
SET @ddl := IF(@back = 1,
    'ALTER TABLE tech_card_marker DROP CHECK chk_tcm_no_double_allowance_mm, DROP CHECK chk_marker_allowance_mm, ADD COLUMN seam_allowance_cm DECIMAL(6,2) NULL, ADD COLUMN contour_allowance_cm DECIMAL(6,2) NULL',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @ddl := IF(@back = 1,
    'UPDATE tech_card_marker SET seam_allowance_cm = seam_allowance_mm / 10, contour_allowance_cm = contour_allowance_mm / 10 WHERE seam_allowance_mm IS NOT NULL OR contour_allowance_mm IS NOT NULL',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @ddl := IF(@back = 1,
    'ALTER TABLE tech_card_marker ADD CONSTRAINT chk_tcm_allowance_nonneg CHECK ((seam_allowance_cm IS NULL OR seam_allowance_cm >= 0) AND (contour_allowance_cm IS NULL OR contour_allowance_cm >= 0)), ADD CONSTRAINT chk_tcm_no_double_allowance CHECK (seam_allowance_cm IS NULL OR contour_allowance_cm IS NULL OR seam_allowance_cm = 0 OR contour_allowance_cm = 0), DROP COLUMN contour_allowance_mm, DROP COLUMN seam_allowance_mm',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @back := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card'
      AND COLUMN_NAME = 'required_seam_allowance_mm');
SET @ddl := IF(@back = 1,
    'ALTER TABLE tech_card DROP CHECK chk_tc_required_seam_allowance_mm, ADD COLUMN required_seam_allowance_cm DECIMAL(6,2) NULL',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @ddl := IF(@back = 1,
    'UPDATE tech_card SET required_seam_allowance_cm = required_seam_allowance_mm / 10 WHERE required_seam_allowance_mm IS NOT NULL',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @ddl := IF(@back = 1,
    'ALTER TABLE tech_card ADD CONSTRAINT chk_tc_required_seam_allowance CHECK (required_seam_allowance_cm IS NULL OR (required_seam_allowance_cm >= 0 AND required_seam_allowance_cm <= 10)), DROP COLUMN required_seam_allowance_mm',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @back := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'workshop_settings'
      AND COLUMN_NAME = 'default_seam_allowance_mm');
SET @ddl := IF(@back = 1,
    'ALTER TABLE workshop_settings DROP CHECK chk_workshop_settings_seam_allowance_mm, ADD COLUMN default_seam_allowance_cm DECIMAL(6,2) NULL AFTER cutting_table_length_cm',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @ddl := IF(@back = 1,
    'UPDATE workshop_settings SET default_seam_allowance_cm = default_seam_allowance_mm / 10 WHERE default_seam_allowance_mm IS NOT NULL',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @ddl := IF(@back = 1,
    'ALTER TABLE workshop_settings ADD CONSTRAINT chk_workshop_settings_seam_allowance CHECK (default_seam_allowance_cm IS NULL OR (default_seam_allowance_cm >= 0 AND default_seam_allowance_cm <= 10)), DROP COLUMN default_seam_allowance_mm',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;
