-- ОЦЕНКА РАСХОДА НА ИЗДЕЛИЕ — единственное новое данное «слотов материалов» (B-16).
--
-- ЗАЧЕМ ОТДЕЛЬНАЯ КОЛОНКА, ЕСЛИ «СКОЛЬКО» В КАРТОЧКЕ УЖЕ ЕСТЬ ДВАЖДЫ. У мерной строки на стадии
-- замысла адреса нет вовсе: норма живёт в `tech_card_colorway_usage.consumption`, то есть в рецепте
-- колорвея, которого на замысле ещё не существует. У счётной адрес есть — `qty_per_garment` (0333),
-- — но это ПОДПИСАННАЯ норма закупки: она входит в дайджест MATERIALS, в себестоимость и в
-- потребность цеха. Положив туда приближение модели, мы сделали бы кнопку черновика правкой денег и
-- протуханием утверждённых подписей.
--
-- И ОДНА КОЛОНКА, А НЕ ДВЕ («оценка ткани» + «оценка фурнитуры»): это один вопрос «сколько на
-- изделие, примерно» с разной ЕДИНИЦЕЙ, а единица у строки уже есть — `unit`. Два поля были бы
-- ложным расщеплением одного приёма.
--
-- СОВЕЩАТЕЛЬНАЯ НАВСЕГДА. Значение не читают ни костинг, ни план материалов, ни кат-лист, ни
-- проекция подписи (materialsRow/materialsTails не меняются — дрейф-проба проекции обязана остаться
-- зелёной, и это и есть доказательство, что подписи не протухли).
--
-- DECIMAL(12,3), А НЕ FLOAT: соседние «сколько» карточки (consumption, qty_per_garment) — десятичные
-- с тремя знаками, и оценка, округляемая иначе, начала бы расходиться с числом, рядом с которым её
-- показывают. NULL, А НЕ 0: «не оценено» и «оценено в ноль» — разные утверждения, и первое стоит у
-- каждой существующей строки.
--
-- БЕЗ CHECK. Неотрицательность проверяется в Go (dto, поле `bom_items[i].est_usage`), потому что
-- ADD CONSTRAINT … CHECK идёт алгоритмом COPY: он переписывает tech_card_bom_item целиком и
-- проверяет ВСЮ историю, а шаг миграции на деплое упирается в захардкоженный пятиминутный потолок.
-- Голый ADD COLUMN — INSTANT, таблица не копируется.
--
-- Идемпотентно, охрана по information_schema; PREPARE/EXECUTE/DEALLOCATE — каждый на своей строке.

-- +migrate Up

SET @bom_est_usage := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_bom_item'
      AND COLUMN_NAME = 'est_usage');
SET @ddl := IF(@bom_est_usage = 0,
    'ALTER TABLE tech_card_bom_item
        ADD COLUMN est_usage DECIMAL(12,3) NULL COMMENT ''оценка расхода на изделие на стадии замысла, в единице строки (unit); совещательная — не входит ни в костинг, ни в план материалов, ни в подпись MATERIALS; NULL = не оценено''',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- +migrate Down

SET @bom_est_usage := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_bom_item'
      AND COLUMN_NAME = 'est_usage');
SET @ddl := IF(@bom_est_usage = 1, 'ALTER TABLE tech_card_bom_item DROP COLUMN est_usage', 'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;
