-- +migrate Up

-- Ф5б.5. ПРИЁМКА КРОЯ. Между «раскроили» и «детали дошли до пошива» сегодня нет ничего: у строки
-- прогона живут planned_qty / received_qty / defect_qty, и всё. Раскроенные, но не дошедшие до
-- пошива детали не существуют ни в одном числе.
--
-- ЦЕПОЧКА НАЗЫВАЕТСЯ ОДИН РАЗ, И НАЗЫВАЕТСЯ ОНА В ПРОТО (common.ProductionRunCutReceipt). Здесь
-- важно только следствие: существующие поля НЕ переозначиваются. Приёмка кроя вставляет два числа
-- МЕЖДУ planned_qty и received_qty, а не переименовывает их. Переозначь мы received_qty в «принято
-- в пошив» — завелась бы двойная бухгалтерия, где «принято в пошив» и «сдано готовым» разойдутся,
-- и оба будут называться «принято».
--
-- ЧИСЛА НЕ ВАЛИДИРУЮТСЯ ДРУГ ПРОТИВ ДРУГА, И ЭТО РЕШЕНИЕ, А НЕ ПРОПУСК. «Раскроено 12 при
-- заказанных 10» — законный перекрой, а «принято 9 из 12 раскроенных» — законный брак раскроя.
-- CHECK'и здесь ровно два, и оба про сам домен чисел (неотрицательность), а не про их отношение:
-- расхождение обязано быть ПОКАЗАНО, а не запрещено. Запрет превратил бы отчёт о факте в спор с
-- формой.
--
-- КЛЮЧ (lay_id, size_id) — ЕСТЕСТВЕННЫЙ, БЕЗ ULID. Строка приёмки описывает пару, которая уже
-- названа настилом и его снимком количеств: у одного настила один размер приходит один раз.
-- Client-minted ULID заводится там, где строку надо диффить по стабильной идентичности через
-- полную замену детей (0281:14-20); здесь замены нет, есть апсерт по паре, и второго читателя у
-- ключа не появилось бы.
--
-- CASCADE ОТ НАСТИЛА, И ЭТО ВЫНУЖДЕННО — тот же довод, что в 0281:117-124. DeleteProductionRun не
-- транзакционен и удаляет одну строку, полагаясь на каскады; порядок обхода каскадов InnoDB не
-- специфицирован, поэтому RESTRICT сделал бы удаление прогона падающим или нет в зависимости от
-- того, что движок обошёл раньше.
--
-- FK НА РАЗМЕР — RESTRICT (явно, а не по умолчанию), как в 0273. Размер — словарь, живущий дольше
-- любого прогона, а строка приёмки, потерявшая размер, делает свой крой неатрибутируемым: «12 штук
-- чего-то» — не факт, а мусор.
--
-- БЕЗ CHARSET-КЛАУЗА (прецедент 0252/0257/0281): прод и бета живут на серверном utf8mb3,
-- контейнерные тесты подключаются utf8mb4, и явное указание развело бы их незаметно.
--
-- Идемпотентность (CLAUDE.md): CREATE TABLE IF NOT EXISTS со всеми ограничениями inline и
-- ИМЕНОВАННЫМИ — авто-именованные <table>_chk_<n> позиционны и дропаются не тем именем.

CREATE TABLE IF NOT EXISTS production_run_cut_receipt (
    id           INT PRIMARY KEY AUTO_INCREMENT,
    lay_id       INT NOT NULL,
    size_id      INT NOT NULL,
    cut_qty      INT NOT NULL DEFAULT 0 COMMENT 'ВЫКРОЕНО: сколько комплектов деталей этого размера реально снято с настила. Больше заказанного — законный перекрой, не ошибка',
    accepted_qty INT NOT NULL DEFAULT 0 COMMENT 'ПРИНЯТО В ПОШИВ: сколько из выкроенного дошло до швейного участка. Меньше cut_qty — брак раскроя',
    note         VARCHAR(512) NULL,
    created_by   VARCHAR(255) NOT NULL DEFAULT '',
    updated_by   VARCHAR(255) NOT NULL DEFAULT '',
    created_at   TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at   TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    CONSTRAINT uniq_prcr_lay_size UNIQUE (lay_id, size_id),
    CONSTRAINT chk_prcr_cut_qty CHECK (cut_qty >= 0),
    CONSTRAINT chk_prcr_accepted_qty CHECK (accepted_qty >= 0),
    CONSTRAINT fk_prcr_lay FOREIGN KEY (lay_id) REFERENCES production_run_lay (id) ON DELETE CASCADE,
    CONSTRAINT fk_prcr_size FOREIGN KEY (size_id) REFERENCES size (id) ON DELETE RESTRICT,
    INDEX idx_prcr_size (size_id)
) ENGINE=InnoDB COMMENT 'ПРИЁМКА КРОЯ (Ф5б): выкроено и принято в пошив на пару (настил, размер)';

-- +migrate Down

-- ГВАРД ПЕРВЫМ, ДО DROP (прецедент 0281 Down). Это рукописный отчёт цеха о том, что физически
-- произошло со столом, и снести его откатом молча значит потерять работу человека под видом
-- миграции. SIGNAL непреобразуем в prepared-протоколе, поэтому отказ — обращение к НЕСУЩЕСТВУЮЩЕЙ
-- КОЛОНКЕ, чьё имя и есть сообщение: ERROR 1054, детерминированно, без единого DDL. Идентификатор
-- режется на 64 символах, поэтому текст короткий и ASCII.
--
-- Проверка существования таблицы обязательна: файл должен быть перезапускаем после частичного
-- отката, а COUNT(*) по уже дропнутой таблице — это 1146, то есть отказ по неверной причине.

SET @have := (SELECT COUNT(*) FROM information_schema.TABLES
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'production_run_cut_receipt');
SET @sql := IF(@have = 0, 'SELECT 0 INTO @blocking',
    'SELECT COUNT(*) INTO @blocking FROM production_run_cut_receipt');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

SET @sql := IF(@blocking = 0, 'SELECT 1',
    CONCAT('SELECT `0287 Down blocked: ', @blocking, ' cut receipts would be destroyed`'));
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

DROP TABLE IF EXISTS production_run_cut_receipt;
