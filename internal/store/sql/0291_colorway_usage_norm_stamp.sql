-- +migrate Up

-- Ф6.8. ШТАМП НОРМЫ НА СТРОКЕ РЕЦЕПТА. 0261 научила строку говорить, ОТКУДА взят расход
-- (consumption_source = manual или marker), но не КАКАЯ ИМЕННО раскладка его дала и КОГДА. Из-за
-- этого карточка не могла сказать единственную вещь, ради которой провенанс и заводился —
-- «норма применена тогда-то из раскладки N, а раскладка с тех пор изменена». Две колонки ниже это
-- и есть — висящий id плюс серверная отметка времени, которую клиент сравнивает с updated_at той же
-- раскладки.
--
-- ВНЕШНЕГО КЛЮЧА НА tech_card_marker СОЗНАТЕЛЬНО НЕТ. Раскладки удаляют, это обычное действие цеха,
-- и обе развязки FK хуже отсутствия связи. RESTRICT уронил бы удаление раскладки, на которую хоть
-- раз сослалась норма, то есть запер бы обычное действие ради аудита. SET NULL молча стёр бы ровно
-- тот штамп, ради которого колонка и заведена, и строка потеряла бы память о происхождении расхода
-- в тот самый момент, когда эта память впервые понадобилась. Висящий id читается как «раскладка
-- удалена» — это не деградация, а правда, и она честнее обеих альтернатив. Тот же размен уже сделан
-- в 0260 на bom_line_key.
--
-- norm_applied_at ПИШЕТ ТОЛЬКО СЕРВЕР (см. UpdateColorwayRecipe). Клиентское значение игнорируется,
-- а отметка сдвигается ТОЛЬКО когда меняется пара (источник, раскладка). Если штамповать её на
-- каждой записи рецепта, правка любого соседнего поля обновила бы отметку и погасила бы индикатор
-- расхождения — то есть расхождение исчезало бы с экрана оттого, что кто-то поправил рядом стоящее
-- число, и сама поломка выглядела бы как её отсутствие.
--
-- ТИП ОТМЕТКИ. TIMESTAMP NULL DEFAULT NULL написан полностью, а не сокращённо — в этой таблице нет
-- ни одной колонки TIMESTAMP, поэтому новая была бы ПЕРВОЙ, а первая TIMESTAMP-колонка при
-- explicit_defaults_for_timestamp = OFF получает неявные DEFAULT CURRENT_TIMESTAMP ON UPDATE
-- CURRENT_TIMESTAMP. Атрибут NULL это отключает, DEFAULT NULL закрепляет намерение явно; иначе
-- отметка «когда применена норма» переписывалась бы на КАЖДОМ обновлении строки, что ровно
-- противоположно её смыслу.
--
-- ПАРНОГО CHECK «оба NULL либо оба заданы» здесь нет — он бил бы 3819 без имени поля на сохранении
-- рецепта, а инвариант держит один-единственный писатель (UpdateColorwayRecipe пишет пару вместе).
-- Проверка на положительность оставлена — явный 0 с провода означает «снять штамп» и обязан лечь
-- NULL'ом, а не ссылкой на несуществующую раскладку 0.
--
-- Идемпотентность (CLAUDE.md) — каждый DDL под собственной проверкой в information_schema,
-- PREPARE/EXECUTE/DEALLOCATE по одному оператору на строку. Без CHARSET-клаузы (прод и бета на
-- utf8mb3, локальные тесты на utf8mb4).

SET @need_col := (SELECT COUNT(*) = 0 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_colorway_usage'
      AND COLUMN_NAME = 'norm_marker_id');
SET @sql := IF(@need_col,
    'ALTER TABLE tech_card_colorway_usage ADD COLUMN norm_marker_id INT NULL AFTER waste_cut_pct',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

SET @need_col := (SELECT COUNT(*) = 0 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_colorway_usage'
      AND COLUMN_NAME = 'norm_applied_at');
SET @sql := IF(@need_col,
    'ALTER TABLE tech_card_colorway_usage ADD COLUMN norm_applied_at TIMESTAMP NULL DEFAULT NULL AFTER norm_marker_id',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

SET @need_chk := (SELECT COUNT(*) = 0 FROM information_schema.TABLE_CONSTRAINTS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_colorway_usage'
      AND CONSTRAINT_NAME = 'chk_tccu_norm_marker_id');
SET @sql := IF(@need_chk,
    'ALTER TABLE tech_card_colorway_usage ADD CONSTRAINT chk_tccu_norm_marker_id CHECK (norm_marker_id IS NULL OR norm_marker_id > 0)',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

-- +migrate Down

-- Аддитивно и обратимо. Порядок вынужденный — CHECK читает колонку и обязан уйти раньше неё. Каждый
-- шаг под своей проверкой, чтобы недооткаченный файл перезапускался начисто. Гварды на данные нет —
-- теряется только штамп, добавленный этой же миграцией, схема после отката согласована.

SET @has_chk := (SELECT COUNT(*) > 0 FROM information_schema.TABLE_CONSTRAINTS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_colorway_usage'
      AND CONSTRAINT_NAME = 'chk_tccu_norm_marker_id');
SET @sql := IF(@has_chk,
    'ALTER TABLE tech_card_colorway_usage DROP CHECK chk_tccu_norm_marker_id',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

SET @has_col := (SELECT COUNT(*) > 0 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_colorway_usage'
      AND COLUMN_NAME = 'norm_applied_at');
SET @sql := IF(@has_col, 'ALTER TABLE tech_card_colorway_usage DROP COLUMN norm_applied_at', 'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

SET @has_col := (SELECT COUNT(*) > 0 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_colorway_usage'
      AND COLUMN_NAME = 'norm_marker_id');
SET @sql := IF(@has_col, 'ALTER TABLE tech_card_colorway_usage DROP COLUMN norm_marker_id', 'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;
