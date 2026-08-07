-- +migrate Up

-- ЭТАЛОН ПРИПУСКА (Ф3.2): цеховой дефолт + переопределение на карточке.
--
-- ЗАЧЕМ ОТДЕЛЬНЫЙ ФАЙЛ ОТ 0276. 0276 трогает tech_card_marker; этот — workshop_settings и tech_card.
-- Разные таблицы, разные фазы отката, и домовое правило предпочитает несколько маленьких миграций
-- одной большой («Prefer several small migrations over one large destructive overhaul»). Ещё точнее:
-- 0276 записывает ФАКТ (какой припуск у этой раскладки), а этот файл заводит ЭТАЛОН (какой припуск
-- полагается). Гейт годности (Ф6) сравнивает одно с другим; сложить их в один файл значило бы связать
-- откат факта с откатом эталона, а они независимы.
--
-- ВТОРОЙ ЖИЛЕЦ ДОМА НАСТРОЕК ЦЕХА. Первым была длина стола (0272); шапка 0272 и entity/workshop.go
-- назвали припуск следующим. Форма та же и по той же причине: одна ТИПИЗИРОВАННАЯ колонка на
-- настройку, NULL = не настроено.
--
-- ОДНО ОТЛИЧИЕ ОТ ДЛИНЫ СТОЛА, И ОНО ЗАГРУЖЕНО. У длины стола ноль бессмыслен, поэтому её CHECK
-- требует > 0. У припуска НОЛЬ ЗАКОНЕН и означает конкретную настройку цеха: «наши выкройки несут
-- линию кроя, офсет добавлять не надо». Поэтому здесь >= 0, а «не настроено» по-прежнему выражается
-- NULL'ом. Ровно ради таких различий 0272 выбрала типизированные колонки вместо общего k/v: один
-- CHECK на все ключи такого различия сделать не может.
--
-- ПОТОЛОК 10 СМ. Самый широкий реальный припуск — подгиб низа, 4-5 см; всё, что больше, — ошибка
-- единиц (миллиметры вместо сантиметров) или лишний ноль. Go-слой повторяет тот же потолок с
-- читаемым сообщением (entity.ValidateDefaultSeamAllowanceCm) — эти два числа обязаны двигаться вместе.
--
-- ПЕРЕОПРЕДЕЛЕНИЕ ЖИВЁТ НА tech_card, А НЕ НА tech_card_construction, и это не вопрос вкуса:
-- дайджест секции CONSTRUCTION хеширует проекцию её содержимого и сравнивается с подписью
-- утверждения. Любое поле, добавленное в ту проекцию, мгновенно делает КАЖДУЮ утверждённую подпись
-- CONSTRUCTION «изменившейся с момента утверждения» — разом на всех карточках. Колонка на tech_card
-- ни в одну проекцию дайджеста не входит и входить не должна. И не путать со свободным текстом
-- construction.seam_allowances («5 мм»), который остаётся человеческой заметкой: здесь ЧИСЛО В
-- САНТИМЕТРАХ, которое читает машина.
--
-- ИДЕМПОТЕНТНОСТЬ (CLAUDE.md): каждый шаг под собственной проверкой в information_schema,
-- PREPARE/EXECUTE/DEALLOCATE по одному оператору на строку.

SET @col := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'workshop_settings'
      AND COLUMN_NAME = 'default_seam_allowance_cm');
SET @ddl := IF(@col = 0,
    'ALTER TABLE workshop_settings ADD COLUMN default_seam_allowance_cm DECIMAL(6,2) NULL COMMENT ''припуск на шов по умолчанию, см; NULL = не настроено; 0 — ЗАКОННОЕ значение: выкройки цеха несут линию кроя'' AFTER cutting_table_length_cm',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @chk := (SELECT COUNT(*) FROM information_schema.TABLE_CONSTRAINTS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'workshop_settings'
      AND CONSTRAINT_NAME = 'chk_workshop_settings_seam_allowance');
SET @ddl := IF(@chk = 0,
    'ALTER TABLE workshop_settings ADD CONSTRAINT chk_workshop_settings_seam_allowance CHECK (
       default_seam_allowance_cm IS NULL
       OR (default_seam_allowance_cm >= 0 AND default_seam_allowance_cm <= 10))',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @col := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card'
      AND COLUMN_NAME = 'required_seam_allowance_cm');
SET @ddl := IF(@col = 0,
    'ALTER TABLE tech_card ADD COLUMN required_seam_allowance_cm DECIMAL(6,2) NULL COMMENT ''требуемый припуск ЭТОЙ карточки, см; NULL = брать цеховой по умолчанию; 0 законен (выкройки модели несут линию кроя)''',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @chk := (SELECT COUNT(*) FROM information_schema.TABLE_CONSTRAINTS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card'
      AND CONSTRAINT_NAME = 'chk_tc_required_seam_allowance');
SET @ddl := IF(@chk = 0,
    'ALTER TABLE tech_card ADD CONSTRAINT chk_tc_required_seam_allowance CHECK (
       required_seam_allowance_cm IS NULL
       OR (required_seam_allowance_cm >= 0 AND required_seam_allowance_cm <= 10))',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- +migrate Down

-- ГВАРД ПЕРВЫМ, как в 0273/0276, и по той же причине: дроп колонок стирает НАСТРОЕННЫЙ эталон, а
-- восстановить его неоткуда. Пока эталон нигде не задан — а это состояние сразу после неудачной
-- выкатки, ради которого откат и существует, — откат проходит свободно.
--
-- Гвард обязан переживать частичный откат: он ссылается на колонки, которые этот же файл дропает.
-- Отсюда проверка наличия обеих колонок и запрос, собранный PREPARE'ом.
SET @blocking := 0;
SET @have := (SELECT
    (SELECT COUNT(*) FROM information_schema.COLUMNS
       WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'workshop_settings'
         AND COLUMN_NAME = 'default_seam_allowance_cm')
    + (SELECT COUNT(*) FROM information_schema.COLUMNS
       WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card'
         AND COLUMN_NAME = 'required_seam_allowance_cm'));
SET @ddl := IF(@have < 2, 'SELECT 0 INTO @blocking',
    'SELECT (SELECT COUNT(*) FROM workshop_settings WHERE default_seam_allowance_cm IS NOT NULL)
          + (SELECT COUNT(*) FROM tech_card WHERE required_seam_allowance_cm IS NOT NULL)
     INTO @blocking');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @ddl := IF(@blocking = 0, 'SELECT 1',
    CONCAT('SELECT `0277 Down blocked: ', @blocking, ' rows carry a configured seam allowance`'));
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @chk := (SELECT COUNT(*) FROM information_schema.TABLE_CONSTRAINTS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card'
      AND CONSTRAINT_NAME = 'chk_tc_required_seam_allowance');
SET @ddl := IF(@chk > 0,
    'ALTER TABLE tech_card DROP CHECK chk_tc_required_seam_allowance',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @col := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card'
      AND COLUMN_NAME = 'required_seam_allowance_cm');
SET @ddl := IF(@col > 0,
    'ALTER TABLE tech_card DROP COLUMN required_seam_allowance_cm',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @chk := (SELECT COUNT(*) FROM information_schema.TABLE_CONSTRAINTS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'workshop_settings'
      AND CONSTRAINT_NAME = 'chk_workshop_settings_seam_allowance');
SET @ddl := IF(@chk > 0,
    'ALTER TABLE workshop_settings DROP CHECK chk_workshop_settings_seam_allowance',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @col := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'workshop_settings'
      AND COLUMN_NAME = 'default_seam_allowance_cm');
SET @ddl := IF(@col > 0,
    'ALTER TABLE workshop_settings DROP COLUMN default_seam_allowance_cm',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;
