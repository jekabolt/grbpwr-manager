-- Полоса DESIGN: ПОТОЛОК ГЕНЕРАЦИИ СНИМАЕТСЯ КАК ПОНЯТИЕ (L-8).
--
-- СЛОВА ВЛАДЕЛЬЦА, дословно и дважды: «у нас не должно быть никаких потолков по генерации что за
-- бред откуда это взялось вообще не могу сгенерить 3д», и на вопрос «поднять ли» — «у нас в
-- принципе не должно быть потолка похуй чем он съеден убери потолок».
--
-- ЭТО НЕ НАСТРОЙКА ЧИСЛА. Поднятый потолок — тот же потолок, просто позже; он снова упрётся, снова
-- в середине работы, и снова человек узнает об этом из отказа. Поэтому уходит САМА КОЛОНКА.
--
-- ─── ПОЧЕМУ КОЛОНКУ УДАЛЯЮТ, А НЕ ОБНУЛЯЮТ И НЕ ДЕЛАЮТ NULLABLE ───
--
-- У `daily_budget` было объявлено (0344): «0 = сегодня не запускаем, это законное состояние». То
-- есть НОЛЬ — САМЫЙ ЗАКРЫТЫЙ ИЗ ВОЗМОЖНЫХ ПОТОЛКОВ, а не его отсутствие. Наивное снятие — оставить
-- колонку и записать в неё 0 — воспроизвело бы ровно ту жалобу, с которой всё началось, и уже
-- навсегда. NULL как «потолка нет» работал бы, но оставил бы колонку, у которой ДВА смысла у
-- пустоты и один писатель, способный вернуть 2.00 обратно; отдельный флаг `budget_enforced`
-- оставил бы механизм целиком и добавил бы второй член, который с первым разъезжается.
--
-- Решающий довод — у требования: «нет потолка» обязано быть СОСТОЯНИЕМ ПО УМОЛЧАНИЮ и обязано
-- пережить чистую установку без единой настройки. Пока колонка существует, у неё есть DEFAULT
-- (2.00), и чистая установка получает потолок в два доллара молча. Единственная форма, в которой
-- требование выполняется ПО ПОСТРОЕНИЮ, — та, где записать потолок некуда.
--
-- ⚠ И ЭТО НЕ ГИПОТЕТИЧЕСКАЯ ЛОВУШКА. loadSettings при отсутствующей строке синглтона отдавал
-- DailyBudget = 0 — то есть у инсталляции, где строку кто-то удалил, полоса была ЗАКРЫТА НАВСЕГДА,
-- и сказано это было бы теми же словами «потолок исчерпан». Ловушка умирает вместе с колонкой.
--
-- ─── ЧТО ОСТАЁТСЯ, И ЭТО ГЛАВНОЕ ───
--
-- ДЕНЬГИ ПО-ПРЕЖНЕМУ МЕРЯЮТСЯ, ЗАПИСЫВАЮТСЯ И ЧИТАЮТСЯ. Владелец возражал не против учёта — он
-- возражал против МАШИНЫ, РЕШАЮЩЕЙ, ЧТО СЕГОДНЯ РАБОТАТЬ НЕЛЬЗЯ; про цену он как раз говорит, и
-- поводом ко всему был прогон, стоивший $100 вместо $0.60. Поэтому НЕ ТРОГАЮТСЯ ни таблица
-- design_budget_day (reserved/spent по дням), ни design_run.price_estimate / price_actual, ни
-- design_run_attempt.price. Резерв на старте и его снятие при закрытии остаются как были: они
-- теперь ведут БУХГАЛТЕРИЮ, а не ворота.
--
-- Настройки полосы (валюта и часовой пояс дня) остаются: они отвечают на «в чём считать» и «чей
-- сегодня», а не на «можно ли работать».
--
-- ─── ЧТО ТЕПЕРЬ ОГРАНИЧИВАЕТ ТРАТЫ ───
--
-- Не деньги, а ПОВТОРЫ, и это правильный уровень: designMaxPaidAttempts = 5 и designMaxRounds = 10
-- (queue.go) не дают одному заданию покупать один и тот же ответ бесконечно, а сторож цены в
-- internal/fal закрывает тот конкретный дефект, из-за которого один прогон съел дневную сумму
-- целиком. Потолок дня против такого дефекта бесполезен по устройству: он срабатывает ПОСЛЕ того,
-- как деньги потрачены, и наказывает не баг, а человека.
--
-- ─── ЦЕНА И ОТКАТ ───
--
-- design_settings — СИНГЛТОН, одна строка. Любой алгоритм ALTER на ней миллисекунды, поэтому про
-- INSTANT/INPLACE здесь нечего обещать и нечего требовать (см. довод против явного ALGORITHM в
-- шапках 0356/0357: явная директива умеет только ОТКЛОНИТЬ оператор, ускорить — нет).
--
-- ⚠ ПОРЯДОК НЕСУЩИЙ: CHECK снимается ПЕРВЫМ. chk_design_settings_daily_budget ссылается на
-- колонку, и DROP COLUMN под живым ограничением падает (MySQL 3959).
--
-- DOWN ВОЗВРАЩАЕТ КОЛОНКУ СО ШКОЛЬНЫМ DEFAULT 2.00 И ТЕРЯЕТ ПРЕЖНЕЕ ЗНАЧЕНИЕ — сказано вслух:
-- откат этой миграции возвращает не «прежний потолок», а потолок ПО УМОЛЧАНИЮ, то есть заново
-- вводит два доллара в день. Восстановить настоящее число неоткуда: его хранила ровно эта колонка.
-- Go после отката её всё равно не читает, так что до пере-выкатки старого бинаря она остаётся
-- мёртвой — и это лучший из доступных исходов, потому что живой потолок без читателя молчит, а
-- мёртвая колонка с читателем закрывает полосу.
--
-- ИДЕМПОТЕНТНОСТЬ: каждый шаг гейтится по information_schema, PREPARE / EXECUTE / DEALLOCATE —
-- каждый своей строкой (прод ходит без multiStatements, а контейнерный тест эту поломку
-- маскирует). Никаких ADD CONSTRAINT … CHECK — общее правило полосы.

-- +migrate Up

SET @ds_chk := (SELECT COUNT(*) FROM information_schema.TABLE_CONSTRAINTS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'design_settings'
      AND CONSTRAINT_NAME = 'chk_design_settings_daily_budget' AND CONSTRAINT_TYPE = 'CHECK');
SET @ddl := IF(@ds_chk = 1,
    'ALTER TABLE design_settings DROP CHECK chk_design_settings_daily_budget',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @ds_col := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'design_settings'
      AND COLUMN_NAME = 'daily_budget');
SET @ddl := IF(@ds_col = 1,
    'ALTER TABLE design_settings DROP COLUMN daily_budget',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- +migrate Down

SET @ds_col_down := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'design_settings'
      AND COLUMN_NAME = 'daily_budget');
SET @ddl := IF(@ds_col_down = 0,
    'ALTER TABLE design_settings
        ADD COLUMN daily_budget DECIMAL(8,2) NOT NULL DEFAULT 2.00
            COMMENT ''ПОТОЛОК СНЯТ 0358: колонка возвращена откатом со значением по умолчанию, прежнее число утеряно. Go её не читает''',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @ds_chk_down := (SELECT COUNT(*) FROM information_schema.TABLE_CONSTRAINTS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'design_settings'
      AND CONSTRAINT_NAME = 'chk_design_settings_daily_budget' AND CONSTRAINT_TYPE = 'CHECK');
SET @ddl := IF(@ds_chk_down = 0,
    'ALTER TABLE design_settings ADD CONSTRAINT chk_design_settings_daily_budget CHECK (daily_budget >= 0)',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;
