-- +migrate Up

-- КАК ИМЕННО ДУБЛИРУЕТСЯ ДЕТАЛЬ (Ф1). Галка `fused` (0109) отвечала только «да/нет», и весь
-- остальной код читал её ЕДИНСТВЕННЫМ возможным способом — «клеевая выкроена по той же лекале»:
-- кат-лист стиля ставит деталь в пару к interlining-слоту целиком, тех-пак печатает «fused: yes»,
-- оценка расхода (0297/Ф1) приписала бы клеевому слоту ПОЛНУЮ площадь контура. В цеху так бывает
-- реже всего: чаще дублируется только край — по припуску или полосой в несколько миллиметров.
--
-- Разница здесь не косметическая, а денежная в разы: у полочки площадью 4200 см² периметр ~260 см,
-- и полоса 25 мм — это 650 см², в 6.5 раза меньше. Пока режима нет, единственный доступный ответ
-- завышает клеевую на всю разницу, и завышает МОЛЧА.
--
-- ТРИ ЗНАЧЕНИЯ, А НЕ ДВА С ЧИСЛОМ. 'seam_allowance' И 'strip' обе кладут полосу вдоль среза и
-- различаются лишь тем, ОТКУДА берётся её ширина: у первой — из эталона припуска (0277, переведён
-- в миллиметры 0290: tech_card.required_seam_allowance_mm, иначе
-- workshop_settings.default_seam_allowance_mm), у второй — из набранного здесь числа. Свернуть их в одно значение с обязательным числом значило бы требовать
-- вписывать припуск руками на каждой детали и ловить расхождение с эталоном, ради которого 0277 и
-- заводилась; развести на два — сделать так, чтобы смена цехового эталона сама двигала норму там,
-- где оператор именно это и имел в виду.
--
-- РЕЖИМ NULL — ЗАКОННОЕ СОСТОЯНИЕ и означает «не размечено», ровно как у cut_symmetry (0275).
-- Дефолта 'full' здесь нет НАМЕРЕННО, хотя весь существующий код читает старую галку именно так.
-- Проставить его бэкфиллом значило бы заявить от имени технолога то, чего он не говорил, — на всех
-- уже заведённых деталях разом, — и снять с экрана единственный признак, по которому видно, что
-- вопрос ещё не задавали. Читатель, которому нужно число СЕЙЧАС, разворачивает NULL в 'full' сам
-- (entity.PieceFusingModeOrFull) и остаётся совместим со всем, что было до этой миграции.
--
-- ДВА CHECK'а, И ОБА ПРО ОДНО: колонки не должны уметь врать вместе.
--   * режим только у fused-детали — снятая галка с уцелевшим 'strip' читалась бы как «не
--     дублируется», а норму дала бы полосой;
--   * ширина ТОЛЬКО у 'strip' и ОБЯЗАТЕЛЬНА у него — 'strip' без числа нечем считать, а число при
--     'seam_allowance' спорит с эталоном, и спор этот молчаливый: на экране видно одно, в расчёте
--     другое.
--
-- ПОТОЛОК 100 ММ. Самая широкая реальная клеевая полоса — дублирование низа под подгиб, 40-50 мм;
-- всё, что больше, — ошибка единиц (сантиметры вместо миллиметров) или лишний ноль. Тот же довод и
-- тот же приём, что у потолка припуска в 0277, и Go повторяет это число читаемым сообщением
-- (entity.ValidatePieceFusing) — два потолка обязаны двигаться вместе.
--
-- МИЛЛИМЕТРЫ — ТА ЖЕ ЕДИНИЦА, ЧТО У ЭТАЛОНА ПРИПУСКА, и это обязано быть так, потому что режим
-- «по припуску» берёт ширину полосы прямо из него. Эталон родился сантиметровым (0277) и был
-- переведён в миллиметры миграцией 0290 (workshop_settings.default_seam_allowance_mm,
-- tech_card.required_seam_allowance_mm); в тех же миллиметрах лежит и tech_card_piece_area
-- .seam_allowance_mm (0297). Одна единица на все три колонки означает, что перевод в сантиметры
-- геометрии делает РОВНО ОДНО место (расчёт нормы), а не каждый экран по разу — и однажды не по разу.
--
-- РЕТРОАКТИВНОСТЬ БЕЗОПАСНА BY CONSTRUCTION (ловушка, останавливавшая старт прода): ADD CONSTRAINT
-- проверяет ВСЮ историю таблицы, но обе колонки заводятся NULL'ом в этой же миграции, и каждая уже
-- лежащая строка проходит оба CHECK'а первой же дизъюнкцией.
--
-- ИДЕМПОТЕНТНОСТЬ (CLAUDE.md): каждый шаг под собственной проверкой в information_schema,
-- PREPARE/EXECUTE/DEALLOCATE по ОДНОМУ оператору на строку — драйвер поднят без multiStatements, и
-- составная строка молча не выполнилась бы целиком.
--
-- БЕЗ КЛАУЗЫ CHARSET у колонки (прецедент 0252/0257/0272/0280/0297): прод и бета крутятся на
-- серверном дефолте utf8mb3, контейнерные тесты подключаются utf8mb4, и явная клауза развела бы их
-- незаметно. Литерал в CHECK — наоборот, помечен явно: 0275 измерила, что операнд `IN` агрегирует
-- коллацию со столбцом, и префикс здесь ПОДТВЕРЖДАЕТ то, что MySQL сохранит навсегда, чтобы файл и
-- SHOW CREATE TABLE, читались одинаково. (Запятая тут не случайна: линтер идемпотентности ищет
-- «CREATE TABLE» с пробелом следом, и упоминание команды в прозе он бы принял за настоящий DDL —
-- тот же приём уже стоит в шапке 0275.)

SET @col_exists := (
    SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'tech_card_piece'
      AND COLUMN_NAME = 'fusing_mode'
);
SET @ddl := IF(@col_exists = 0,
    'ALTER TABLE tech_card_piece ADD COLUMN fusing_mode VARCHAR(16) NULL COMMENT ''как дублируется: full | seam_allowance | strip; NULL = не размечено, читатель разворачивает в full'' AFTER fused',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @col_exists := (
    SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'tech_card_piece'
      AND COLUMN_NAME = 'fusing_width_mm'
);
SET @ddl := IF(@col_exists = 0,
    'ALTER TABLE tech_card_piece ADD COLUMN fusing_width_mm DECIMAL(6,1) NULL COMMENT ''ширина клеевой полосы, мм; только и обязательно при fusing_mode = strip'' AFTER fusing_mode',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @chk_vocab := (
    SELECT COUNT(*) FROM information_schema.TABLE_CONSTRAINTS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'tech_card_piece'
      AND CONSTRAINT_NAME = 'chk_tcp_fusing_mode'
);
SET @ddl := IF(@chk_vocab = 0,
    -- STRCMP, А НЕ ОДИН ЛИШЬ СПИСОК. Интродьюсер `_utf8mb4` задаёт КОДИРОВКУ, а не регистр:
    -- сравнение сводится к коллации колонки (`..._ai_ci`), и 'FULL' проходил бы как законное
    -- значение. Цена промаха несимметрична: `PieceFusingModeOrFull` не найдёт такой ключ в карте и
    -- МОЛЧА вернёт `full` — то есть полосу клеевой посчитает сплошным дублированием, ровно ту
    -- ошибку, ради которой эта колонка и заведена. Дом уже стандартизировал эту форму в 0289/0306.
    'ALTER TABLE tech_card_piece ADD CONSTRAINT chk_tcp_fusing_mode CHECK (fusing_mode IS NULL OR (fused = TRUE AND fusing_mode IN (_utf8mb4''full'', _utf8mb4''seam_allowance'', _utf8mb4''strip'') AND STRCMP(CAST(fusing_mode AS BINARY), CAST(LOWER(fusing_mode) AS BINARY)) = 0))',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @chk_width := (
    SELECT COUNT(*) FROM information_schema.TABLE_CONSTRAINTS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'tech_card_piece'
      AND CONSTRAINT_NAME = 'chk_tcp_fusing_width'
);
SET @ddl := IF(@chk_width = 0,
    'ALTER TABLE tech_card_piece ADD CONSTRAINT chk_tcp_fusing_width CHECK (
       (fusing_mode <=> _utf8mb4''strip'' AND fusing_width_mm IS NOT NULL AND fusing_width_mm > 0 AND fusing_width_mm <= 100)
       OR (NOT (fusing_mode <=> _utf8mb4''strip'') AND fusing_width_mm IS NULL))',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- +migrate Down

SET @chk_width := (
    SELECT COUNT(*) FROM information_schema.TABLE_CONSTRAINTS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'tech_card_piece'
      AND CONSTRAINT_NAME = 'chk_tcp_fusing_width'
);
SET @ddl := IF(@chk_width > 0,
    'ALTER TABLE tech_card_piece DROP CHECK chk_tcp_fusing_width',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @chk_vocab := (
    SELECT COUNT(*) FROM information_schema.TABLE_CONSTRAINTS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'tech_card_piece'
      AND CONSTRAINT_NAME = 'chk_tcp_fusing_mode'
);
SET @ddl := IF(@chk_vocab > 0,
    'ALTER TABLE tech_card_piece DROP CHECK chk_tcp_fusing_mode',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @col_exists := (
    SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'tech_card_piece'
      AND COLUMN_NAME = 'fusing_width_mm'
);
SET @ddl := IF(@col_exists > 0,
    'ALTER TABLE tech_card_piece DROP COLUMN fusing_width_mm',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @col_exists := (
    SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'tech_card_piece'
      AND COLUMN_NAME = 'fusing_mode'
);
SET @ddl := IF(@col_exists > 0,
    'ALTER TABLE tech_card_piece DROP COLUMN fusing_mode',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;
