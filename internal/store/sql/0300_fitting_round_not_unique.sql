-- +migrate Up

-- Примерка перестала быть раундом — снимаем UNIQUE (tech_card_id, round_number) с fitting.
--
-- ЧТО СЛОМАНО. Добавить ВТОРУЮ примерку на тот же семпл нельзя: сервер отвечает 13/«can't add
-- fitting», под которым лежит драйверная 1062 на uniq_fitting_round (0102). Тот индекс писался в
-- модели, где примерка И БЫЛА раундом: «сколько итераций до утверждения» считалось по строкам
-- fitting, поэтому (карточка, номер раунда) обязана была быть уникальной.
--
-- ПОЧЕМУ ОН БОЛЬШЕ НЕ ВЕРЕН. WS6 (§2.7) развёл объект и событие: раунд разработки несёт СЕМПЛ
-- (sample.round_number, миграция 0170 — и её индекс idx_sample_round СОЗНАТЕЛЬНО не уникален,
-- «в раунде может быть сшито несколько семплов»), а примерка — событие НА семпле. Из этого прямо
-- следует, что раунд вправе иметь больше одной примерки: тот же семпл примеряют повторно после
-- правок, а два семпла одного раунда (размеры/колорвеи) примеряют каждый. Уникальность осталась
-- от прежней модели и запрещает ровно то, что новая модель разрешает; carry-over и так берёт
-- раунд из семпла (ListOpenFittingChangeRequests джойнит sample.round_number, не fitting).
--
-- НИЧТО НЕ ОПИРАЕТСЯ НА УНИКАЛЬНОСТЬ: ни один запрос не ищет примерку по (карточка, раунд), а
-- сводки развития агрегируют раунд суммой и максимумом (ComputeTechCardDevCostSummary: byRound
-- складывает, rounds_to_approval берёт максимум) — дубли раунда там честно схлопываются в один
-- ведро, а не портят число.
--
-- ПОРЯДОК ШАГОВ ОБЯЗАТЕЛЕН: сначала создаём обычный индекс с тем же префиксом (tech_card_id, …),
-- и только потом снимаем уникальный. На fitting висит FK fk_fitting_tech_card (0069), а InnoDB не
-- даёт удалить последний индекс, покрывающий столбец внешнего ключа: обратный порядок упал бы
-- ошибкой 1553 и остановил бы старт прода.
--
-- Гарды по information_schema: файл идемпотентен (DDL в MySQL авто-коммитится, повторный прогон
-- после падения ниже по файлу обязан быть no-op). PREPARE/EXECUTE/DEALLOCATE — по одному оператору
-- на строку: прод ходит без multiStatements.
SET @needs := (
    SELECT COUNT(*) FROM information_schema.STATISTICS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'fitting'
      AND INDEX_NAME = 'idx_fitting_round'
);
SET @ddl := IF(@needs = 0,
    'ALTER TABLE fitting ADD INDEX idx_fitting_round (tech_card_id, round_number)',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @needs := (
    SELECT COUNT(*) FROM information_schema.STATISTICS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'fitting'
      AND INDEX_NAME = 'uniq_fitting_round'
);
SET @ddl := IF(@needs > 0,
    'ALTER TABLE fitting DROP INDEX uniq_fitting_round',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- +migrate Down

-- Обратный порядок симметричен и по той же причине: уникальный индекс возвращается ПЕРВЫМ, чтобы
-- FK на tech_card_id ни на мгновение не остался без покрытия.
--
-- Возврат UNIQUE проверяет ВСЮ историю таблицы: если под новым бинарём в одном раунде уже
-- записана вторая примерка, ADD упадёт — и это единственно честное поведение, потому что молча
-- удалять примерки ради индекса нельзя. Откат в такой ситуации требует ручного разведения
-- номеров; поэтому Down здесь — формальность симметрии, а не рабочий сценарий.
SET @needs := (
    SELECT COUNT(*) FROM information_schema.STATISTICS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'fitting'
      AND INDEX_NAME = 'uniq_fitting_round'
);
SET @ddl := IF(@needs = 0,
    'ALTER TABLE fitting ADD CONSTRAINT uniq_fitting_round UNIQUE (tech_card_id, round_number)',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @needs := (
    SELECT COUNT(*) FROM information_schema.STATISTICS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'fitting'
      AND INDEX_NAME = 'idx_fitting_round'
);
SET @ddl := IF(@needs > 0,
    'ALTER TABLE fitting DROP INDEX idx_fitting_round',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;
