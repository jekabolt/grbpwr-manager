-- ОТЛОЖЕННЫЙ СНОС ДВУХ КОЛОНОК БЛОКА ДЕФОЛТОВ: `pressing` и `overlock_thread_count`.

-- Это вторая половина миграции 0306, намеренно отделённая от неё во времени. 0306 перенесла
-- содержимое обеих колонок (проза ВТО — в `notes` под тегом «ВТО (перенос 0306):», счётчик ниток —
-- в профиль оборудования `LEGACYOVERLOCK…`) и перестала их читать и писать, но САМИ КОЛОНКИ
-- оставила стоять пустыми. Причина была ровно одна: снос — единственный разрушительный оператор
-- всего выката, и он закрывает дорогу назад, потому что ПРЕДЫДУЩИЙ бинарь обе колонки ВСТАВЛЯЕТ
-- (`insertTechCardConstruction` до 0306). Пока они стоят, кнопка отката DO работает; после этого
-- файла — уже нет.

-- ЗАПУСКАТЬ ТОЛЬКО ПОСЛЕ ТОГО, КАК ПРОД ОТРАБОТАЛ НА НОВОМ БИНАРЕ. Владелец дал добро 2026-08-16,
-- в тот же день, когда 0304-0310 уехали на прод.

-- АВАРИЙНЫЙ ВЫХОД, если откат к до-0306 бинарю всё-таки понадобится. Кнопка DO миграции Down НЕ
-- гоняет — приложение на старте выполняет только Up. Значит колонки надо вернуть руками, и это
-- дословно блок Down ниже: он воссоздаёт ОБЕ колонки и их CHECK. Содержимое не возвращается и
-- возвращено быть не может — оно давно живёт в notes и в профилях, и разливать его обратно значило
-- бы дублировать текст, который технолог с тех пор мог править.

-- НА БЕТЕ ЭТОТ ФАЙЛ — ПУСТАЯ ОПЕРАЦИЯ, и это не случайность, а единственный доступный там исход:
-- бета применила ПЕРВУЮ редакцию 0306, которая сносила колонки шагом 8c, поэтому их там нет с
-- 2026-08-15. СЛЕДСТВИЕ, КОТОРОЕ НАДО ЗНАТЬ: снос по-настоящему выполняется ТОЛЬКО на проде, и
-- бета его не репетирует. Отсюда форма ниже — три НЕЗАВИСИМЫХ оператора вместо одного составного
-- ALTER-а: падение на любом из них оставляет состояние, из которого следующий старт продолжает с
-- места обрыва, а не упирается в «половина снята, половина нет». Каждый из трёх — тот же гейт
-- `IF(проба, 'литерал', 'SELECT 1')`, что стоит по всему репозиторию и отработал на обеих средах.

-- ПОРЯДОК ОБЯЗАТЕЛЕН: CHECK уходит ПЕРВЫМ. MySQL 8 отказывается снимать колонку, на которую
-- ссылается живой CHECK (ошибка 3959). Пока проверка снята, колонка свободна.

-- +migrate Up

-- 1. Проверка `overlock_thread_count` ищется ПО СОДЕРЖИМОМУ CLAUSE, а не по имени: 0289 завела её
--    именованной (`chk_construction_overlock_threads`), но искать по содержимому дешевле ровно на
--    один класс ошибок — безымянная позиционная проверка нашлась бы тоже. NULL (уже снята)
--    вырождается в 'SELECT 1'. Подзапрос без LIMIT намеренно: две подходящие строки уронят его
--    ошибкой 1242, и это ШТАТНЫЙ СТОП-КРАН — «нашлось два кандидата» значит, что схема не та,
--    которую этот файл описывает, и снимать вслепую нельзя (приём шага 4 миграции 0306).
SET @chk := (SELECT c.CONSTRAINT_NAME
    FROM information_schema.CHECK_CONSTRAINTS c
    JOIN information_schema.TABLE_CONSTRAINTS t
      ON t.CONSTRAINT_SCHEMA = c.CONSTRAINT_SCHEMA AND t.CONSTRAINT_NAME = c.CONSTRAINT_NAME
    WHERE c.CONSTRAINT_SCHEMA = DATABASE() AND t.TABLE_NAME = 'tech_card_construction'
      AND c.CHECK_CLAUSE LIKE '%overlock_thread_count%');
SET @ddl := IF(@chk IS NULL, 'SELECT 1',
    CONCAT('ALTER TABLE tech_card_construction DROP CHECK ', @chk));
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- 2. Проза ВТО. Её содержимое уехало в notes шагом 8b миграции 0306.
SET @has_pressing := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_construction'
      AND COLUMN_NAME = 'pressing');
SET @ddl := IF(@has_pressing = 1,
    'ALTER TABLE tech_card_construction DROP COLUMN pressing', 'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- 3. Счётчик ниток. Его содержимое стало профилем `LEGACYOVERLOCK…` шагом 8a миграции 0306.
SET @has_threads := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_construction'
      AND COLUMN_NAME = 'overlock_thread_count');
SET @ddl := IF(@has_threads = 1,
    'ALTER TABLE tech_card_construction DROP COLUMN overlock_thread_count', 'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- ИМЯ И НОМЕР УХОДЯТ НАВСЕГДА. Переиспользовать `pressing` или `overlock_thread_count` под новым
-- смыслом нельзя — это тот самый молчаливый дрейф, против которого написана шапка 0289. Позиции 3
-- и 5 проекции дайджеста CONSTRUCTION заморожены константами по той же причине и остаются
-- замороженными: подписи, снятые до 0306, обязаны пересчитываться байт в байт.

-- +migrate Down

-- Возврат СХЕМЫ, не данных. Колонки приходят пустыми, тег «ВТО (перенос 0306):» в notes остаётся
-- на месте: стирать его значило бы резать пользовательский текст по шаблону. Форма дословно
-- повторяет шаг D3 миграции 0306 — он этот же откат уже описывает, и два разных описания одного
-- возврата разошлись бы на первой правке.

-- Гейт один, на `pressing`: после Up обеих колонок нет вместе, а ADD COLUMN на существующую
-- колонку — ошибка, а не пустая операция.
SET @cons_back := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_construction'
      AND COLUMN_NAME = 'pressing');
SET @ddl := IF(@cons_back = 0,
    'ALTER TABLE tech_card_construction
        ADD COLUMN pressing VARCHAR(255) NULL COMMENT ''ВТО / финишная отделка'',
        ADD COLUMN overlock_thread_count TINYINT NULL COMMENT ''ниток в оверлоке: 3, 4 или 5; NULL = не задано'',
        ADD CONSTRAINT chk_construction_overlock_threads CHECK (overlock_thread_count IS NULL OR (overlock_thread_count >= 3 AND overlock_thread_count <= 5))',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;
