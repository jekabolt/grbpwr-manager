-- Полоса DESIGN, кусок 5 из 7: деньги — потолок дня и счётчик дня.
--
-- ЗАЧЕМ ОТДЕЛЬНАЯ СТРОКА ДНЯ, А НЕ `SUM` ПО ПРОГОНАМ. Три вещи чинятся разом:
--
--   1. ДЕДЛОК. `SELECT SUM(price) FROM design_run WHERE created_at >= today` под SERIALIZABLE
--      берёт next-key S-блокировки ОТКРЫТОГО диапазона, и обе транзакции затем хотят вставить в
--      этот же диапазон — это 1213, который спасает не изоляция, а ретрай
--      (`internal/store/db.go:107-141`, `IsErrorRepeat` — только 1213/1205). Формулировка
--      «две вставки под потолком сериализуются бесплатно» неверна. Точечный
--      `UPDATE … WHERE day = :d` берёт ОДНУ строку.
--   2. ЖИЗНЬ ПОСЛЕ УДАЛЕНИЯ КАРТОЧКИ. Таблица намеренно НЕ каскадится от `tech_card`: удаление
--      карточки больше не «освобождает» бюджет дня задним числом. Цена, которую принимаем и
--      называем вслух: постатейная история трат ПО карточке уходит вместе с карточкой, а дневная
--      и месячная суммы живут здесь и не уходят.
--   3. ЧАСОВОЙ ПОЯС НАЗВАН. «created_at >= today» не отвечало, ЧЕЙ today. Ключ дня считает Go в
--      `design_settings.budget_timezone`; при другом ответе владельца меняется одна строка
--      конфига, а не смысл всех прошлых строк.
--
-- ДЕНЬГИ СЧИТАЮТСЯ ПО ПОПЫТКАМ, А НЕ ПО ПРОГОНАМ: `spent` растёт на каждой ЗАКРЫТОЙ попытке,
-- включая оплаченный провал (`design_run_attempt.price`), а `reserved` держит оценки запущенного
-- и ещё не завершённого. Полоса «today $0.41 of $2.00» показывает `reserved + spent` против
-- `daily_budget` — процессная память рестарт не переживает, а деньги переживают, поэтому этот
-- четвёртый пояс персистентный (первые три — in-flight, минимальный интервал и почасовое окно —
-- уже написаны в `analysisRunGuard`, `internal/apisrv/admin/techcard_analysis.go:216-297`).
--
-- SINGLETON ПО ОБРАЗЦУ `0272_workshop_settings.sql:43-66`: одна строка ЕСТЬ вся конфигурация,
-- отсюда множественное число в имени таблицы и CHECK (id = 1). `INSERT IGNORE` — потому что MySQL
-- коммитит DDL пооператорно, и следующий старт после половинчатого применения зайдёт с начала.
-- Валюта и пояс — обычные колонки со значением по умолчанию, а не словарь: их читает Go.

-- +migrate Up

CREATE TABLE IF NOT EXISTS design_settings (
    id INT NOT NULL PRIMARY KEY COMMENT 'singleton; единственное законное значение — 1',
    daily_budget DECIMAL(8,2) NOT NULL DEFAULT 2.00 COMMENT 'потолок трат на ИИ за день; 0 = «сегодня не запускаем», это законное состояние',
    currency CHAR(3) NOT NULL DEFAULT 'USD' COMMENT 'валюта потолка и счётчиков',
    budget_timezone VARCHAR(64) NOT NULL DEFAULT 'Europe/Warsaw' COMMENT 'чей «сегодня» обнуляет полосу; org-решение, а не MySQL-сессия. Ключ дня считает Go в этом поясе',
    updated_by VARCHAR(255) NOT NULL DEFAULT '' COMMENT 'username из JWT последнего писателя',
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    CONSTRAINT chk_design_settings_singleton CHECK (id = 1),
    CONSTRAINT chk_design_settings_daily_budget CHECK (daily_budget >= 0)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT 'Настройки полосы DESIGN: одна строка ЕСТЬ вся конфигурация';

-- Сид singleton со значениями по умолчанию. INSERT IGNORE держит файл переисполнимым после
-- половинчатого применения (MySQL коммитит DDL пооператорно, следующий старт идёт с начала).
INSERT IGNORE INTO design_settings (id) VALUES (1);

CREATE TABLE IF NOT EXISTS design_budget_day (
    day DATE PRIMARY KEY COMMENT 'ключ дня, посчитанный в budget_timezone В GO, а не в MySQL: у сервера свой часовой пояс, и он не является ответом организации',
    reserved DECIMAL(10,4) NOT NULL DEFAULT 0 COMMENT 'оценки запущенного и ещё не завершённого; снимается по мере закрытия попыток',
    spent DECIMAL(10,4) NOT NULL DEFAULT 0 COMMENT 'факт по ПОПЫТКАМ — включая оплаченные провалы; ретрай платит второй раз, и полоса это видит',
    currency CHAR(3) NOT NULL DEFAULT 'USD',
    updated_at TIMESTAMP(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT 'Счётчик трат дня одной строкой: точечный UPDATE вместо SUM по открытому диапазону';

-- +migrate Down

DROP TABLE IF EXISTS design_budget_day;
DROP TABLE IF EXISTS design_settings;
