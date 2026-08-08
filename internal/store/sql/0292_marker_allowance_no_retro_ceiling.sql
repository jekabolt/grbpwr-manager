-- +migrate Up

-- СНЯТИЕ РЕТРОАКТИВНОГО ПОТОЛКА С ПРИПУСКА РАСКЛАДКИ.
--
-- Первая редакция 0290 добавляла chk_marker_allowance_mm с верхней границей 100 мм. Она успела
-- примениться НА БЕТЕ; на проде 0290 ещё не выполнялась и выполнится уже без границы. Этот файл
-- существует ровно затем, чтобы оба контура пришли к ОДНОМУ состоянию, а не разошлись молча.
--
-- ЧЕМ БЫЛА ОПАСНА ГРАНИЦА. MySQL проверяет ADD CONSTRAINT по ВСЕМ существующим строкам таблицы.
-- Предшественник (chk_tcm_allowance_nonneg, 0276) требовал только >= 0, а серверный валидатор
-- пропускал до 10000, поэтому раскладка с припуском больше 10 см была законной ровно до этого
-- момента. Одна такая строка на проде = 3819 на миграции = при MYSQL_AUTOMIGRATE=true остановка
-- старта, автоматический откат DO и /readyz, который отвечает 200 от ПРЕДЫДУЩЕГО билда. Проверить
-- заранее было нечем: данные прода недоступны из инструмента, которым писалась миграция.
--
-- ПОЧЕМУ ГРАНИЦА ВООБЩЕ БЫЛА И КУДА ОНА УЕХАЛА. Потолок припуска — не физика, а ПРОВЕРКА ЕДИНИЦ:
-- ловит сантиметры, записанные в миллиметровую колонку. Такую проверку нельзя применять задним
-- числом к строкам, записанным когда правила не существовало: историческая запись — это факт
-- замера, а не нарушение. Её место на ЗАПИСИ, и она там теперь есть —
-- entity.ValidateMarkerAllowanceMm отвечает внятным field violation с указанием поля, тогда как
-- CHECK выстрелил бы кодом 3819 без адреса (тот же довод, что в шапке 0278 и 0289).
--
-- ЗАПРЕТ ДВОЙНОГО ПРИПУСКА (chk_tcm_no_double_allowance_mm) НЕ ТРОГАЕТСЯ. Он про соотношение двух
-- колонок, а не про величину, и на исторических данных выполним: пара «оба > 0» была запрещена
-- схемой с 0276, поэтому нарушить его существующая строка не может.
--
-- ИДЕМПОТЕНТНОСТЬ: снять и добавить — два отдельных шага, каждый под своей проверкой. Повтор после
-- падения между ними видит уже снятый CHECK и просто досоздаёт новый.

-- 1. Снять СТАРУЮ редакцию — только если она действительно строгая. На проде (и на свежей базе)
--    0290 уже создала расслабленную, и трогать её незачем: '100' в тексте CHECK'а и есть признак.
SET @strict := (SELECT COUNT(*) FROM information_schema.CHECK_CONSTRAINTS c
    JOIN information_schema.TABLE_CONSTRAINTS t
      ON t.CONSTRAINT_SCHEMA = c.CONSTRAINT_SCHEMA AND t.CONSTRAINT_NAME = c.CONSTRAINT_NAME
    WHERE c.CONSTRAINT_SCHEMA = DATABASE() AND t.TABLE_NAME = 'tech_card_marker'
      AND c.CONSTRAINT_NAME = 'chk_marker_allowance_mm' AND c.CHECK_CLAUSE LIKE '%100%');
SET @ddl := IF(@strict = 1,
    'ALTER TABLE tech_card_marker DROP CHECK chk_marker_allowance_mm',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- 2. Вернуть его же без верхней границы. Неотрицательность остаётся: отрицательный припуск не
--    «непроверенная единица», а бессмыслица, и таких строк в истории быть не может — 0276 их
--    запрещала с самого начала.
SET @has := (SELECT COUNT(*) FROM information_schema.TABLE_CONSTRAINTS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_marker'
      AND CONSTRAINT_NAME = 'chk_marker_allowance_mm');
SET @ddl := IF(@has = 0,
    'ALTER TABLE tech_card_marker ADD CONSTRAINT chk_marker_allowance_mm CHECK ((seam_allowance_mm IS NULL OR seam_allowance_mm >= 0) AND (contour_allowance_mm IS NULL OR contour_allowance_mm >= 0))',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- +migrate Down
--
-- Откат ВОЗВРАЩАЕТ границу, и он может упасть — ровно по той причине, ради которой она снята. Это
-- честно: откат к состоянию, несовместимому с накопленными данными, обязан отказать, а не молча
-- отбросить строки.
SET @has := (SELECT COUNT(*) FROM information_schema.TABLE_CONSTRAINTS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_marker'
      AND CONSTRAINT_NAME = 'chk_marker_allowance_mm');
SET @ddl := IF(@has = 1,
    'ALTER TABLE tech_card_marker DROP CHECK chk_marker_allowance_mm',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @ddl := 'ALTER TABLE tech_card_marker ADD CONSTRAINT chk_marker_allowance_mm CHECK ((seam_allowance_mm IS NULL OR (seam_allowance_mm >= 0 AND seam_allowance_mm <= 100)) AND (contour_allowance_mm IS NULL OR (contour_allowance_mm >= 0 AND contour_allowance_mm <= 100)))';
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;
