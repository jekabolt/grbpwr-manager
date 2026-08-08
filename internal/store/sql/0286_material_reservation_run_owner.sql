-- +migrate Up

-- Ф5б.4 + Ф5б.6. РЕЗЕРВ ТКАНИ ПОД ПРОИЗВОДСТВЕННЫЙ ПРОГОН — В ТОМ ЖЕ РЕЕСТРЕ, А НЕ ВО ВТОРОМ.
--
-- Реестр material_reservation_ledger (0164) резервирует упаковку под заказы покупателей и держит
-- order_id INT NOT NULL с внешним ключом на customer_order. Ткань не резервирует никто, и два
-- прогона на один артикул оба видят «хватает». Претензия ПРОГОНА в сегодняшний реестр физически не
-- ложится — ей нечем назвать своего владельца.
--
-- ПОЧЕМУ НЕ ВТОРАЯ ТАБЛИЦА, И ЭТО ГЛАВНОЕ РЕШЕНИЕ ФАЙЛА. available(material) = on_hand − Σ открытых
-- претензий обязано быть ОДНИМ числом над ОБОИМИ источниками. Две таблицы — это два полуответа,
-- которые каждый читатель обязан сложить сам, и первый забывший продаёт то, что уже удержано.
-- Ровно ради единого available реестр и заводился (0164:1-12).
--
-- ПРОВЕРКА ПРАВИЛЬНОСТИ ЭТОГО РЕШЕНИЯ ВСТРОЕНА В НЕГО, и она уже пройдена. Читатель openReservedQty
-- (internal/store/inventory/reservation.go:200) суммирует открытые претензии ПО material_id и НЕ
-- смотрит на владельца вовсе. Значит после этой миграции он начинает учитывать резерв прогонов БЕЗ
-- ЕДИНОЙ ПРАВКИ — и оба его потребителя (MaterialAvailable, AdjustMaterialStock) вместе с ним.
-- Понадобись здесь правка читателя — решение было бы неверным, и вместо неё нужна была бы вторая
-- таблица с честным объединением. Правка не понадобилась.
--
-- XOR, А НЕ «ОДИН ИЗ ДВУХ МОЖЕТ БЫТЬ ПУСТ». Претензия без владельца — это удержание, которое некому
-- закрыть: оно вечно давит на available и не отпускается ни закрытием заказа, ни закрытием прогона.
-- Претензия с двумя владельцами закрылась бы дважды и отпустила бы ткань, которую всё ещё держит
-- второй. Прецедент инварианта — chk_prl_variant_xor (0253).
--
-- lot_id — ЗАЧЕМ ОН ЗДЕСЬ (Ф5б.6). Перекрой через месяц из другой партии виден на изделии как
-- дефект, поэтому ткань под перекрой резервируется из ТОГО ЖЕ лота. Пер-лотное «сколько свободно в
-- лоте X» = remaining_qty − Σ открытых претензий с этим lot_id — отдельный запрос, который зовёт
-- ТОЛЬКО проверка перекроя; в общий available лот не входит, потому что общий available спрашивает
-- про материал, а не про рулон. Отсюда и SET NULL: пропавший лот не должен уносить претензию —
-- претензия про материал и остаётся верной без рулона.
--
-- FK на прогон — CASCADE. Симметрично заказному FK (0164) и вынужденно так же, как в 0281:
-- DeleteProductionRun не транзакционен и живёт каскадами; RESTRICT сделал бы удаление прогона
-- зависящим от порядка обхода InnoDB.
--
-- КЛЮЧ ИДЕМПОТЕНТНОСТИ НЕ МЕНЯЕТСЯ СХЕМОЙ И НЕ ПЕРЕПИСЫВАЕТСЯ. claim_key остаётся VARCHAR(64) с
-- UNIQUE(claim_key, event). Претензия прогона берёт формат run:{run_id}:{material_id}:{gen} — его
-- задаёт writer, а не схема. Префикс разводит пространства ключей: без него прогон 7 и заказ 7 на
-- одном материале столкнулись бы на одном ключе, и вторая претензия молча схлопнулась бы в
-- INSERT IGNORE, то есть удержание читалось бы как уже записанное и не записалось бы никогда.
-- Поколение {gen} нужно потому, что реестр append-only и UNIQUE физически запрещает второй reserve
-- на тот же ключ: корректировка — это release текущего поколения плюс reserve следующего, а не
-- UPDATE. Заказные ключи остаются как есть: переписать ключ идемпотентности значит сделать все
-- открытые претензии невидимыми для того кода, который их закрывает.
--
-- БЕЗОПАСНОСТЬ НА СУЩЕСТВУЮЩИХ ДАННЫХ. До миграции order_id NOT NULL у КАЖДОЙ строки, значит run_id
-- у всех NULL, значит XOR тривиально истинен на всём наборе и CHECK принимается без единой правки
-- данных. Снятие NOT NULL само по себе ничего не переписывает: ни одна существующая строка не
-- становится NULL от того, что ей это разрешили.
--
-- Таблица создана с явным DEFAULT CHARSET=utf8mb4 (0164) — её кодировка НЕ трогается; добавляются
-- только INT-колонки, у которых кодировки нет.
--
-- Идемпотентность: каждый DDL под своей проверкой, PREPARE/EXECUTE/DEALLOCATE по одному оператору
-- на строку. Индексы заводятся ДО внешних ключей, чтобы MySQL переиспользовал их вместо
-- автосозданных и Down знал стабильные имена.

SET @is_nullable := (SELECT IS_NULLABLE FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'material_reservation_ledger'
      AND COLUMN_NAME = 'order_id');
SET @sql := IF(@is_nullable = 'NO',
    'ALTER TABLE material_reservation_ledger MODIFY COLUMN order_id INT NULL',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

SET @need_col := (SELECT COUNT(*) = 0 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'material_reservation_ledger'
      AND COLUMN_NAME = 'run_id');
SET @sql := IF(@need_col,
    'ALTER TABLE material_reservation_ledger ADD COLUMN run_id INT NULL AFTER order_id',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

SET @need_col := (SELECT COUNT(*) = 0 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'material_reservation_ledger'
      AND COLUMN_NAME = 'lot_id');
SET @sql := IF(@need_col,
    'ALTER TABLE material_reservation_ledger ADD COLUMN lot_id INT NULL AFTER run_id',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

SET @need_idx := (SELECT COUNT(*) = 0 FROM information_schema.STATISTICS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'material_reservation_ledger'
      AND INDEX_NAME = 'idx_material_reservation_run');
SET @sql := IF(@need_idx,
    'ALTER TABLE material_reservation_ledger ADD INDEX idx_material_reservation_run (run_id)',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

SET @need_idx := (SELECT COUNT(*) = 0 FROM information_schema.STATISTICS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'material_reservation_ledger'
      AND INDEX_NAME = 'idx_material_reservation_lot');
SET @sql := IF(@need_idx,
    'ALTER TABLE material_reservation_ledger ADD INDEX idx_material_reservation_lot (lot_id)',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

SET @need_fk := (SELECT COUNT(*) = 0 FROM information_schema.TABLE_CONSTRAINTS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'material_reservation_ledger'
      AND CONSTRAINT_NAME = 'fk_material_reservation_run');
SET @sql := IF(@need_fk,
    'ALTER TABLE material_reservation_ledger ADD CONSTRAINT fk_material_reservation_run FOREIGN KEY (run_id) REFERENCES production_run (id) ON DELETE CASCADE',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

SET @need_fk := (SELECT COUNT(*) = 0 FROM information_schema.TABLE_CONSTRAINTS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'material_reservation_ledger'
      AND CONSTRAINT_NAME = 'fk_material_reservation_lot');
SET @sql := IF(@need_fk,
    'ALTER TABLE material_reservation_ledger ADD CONSTRAINT fk_material_reservation_lot FOREIGN KEY (lot_id) REFERENCES material_lot (id) ON DELETE SET NULL',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

SET @need_chk := (SELECT COUNT(*) = 0 FROM information_schema.TABLE_CONSTRAINTS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'material_reservation_ledger'
      AND CONSTRAINT_NAME = 'chk_material_reservation_owner_xor');
SET @sql := IF(@need_chk,
    'ALTER TABLE material_reservation_ledger ADD CONSTRAINT chk_material_reservation_owner_xor CHECK ((order_id IS NULL) <> (run_id IS NULL))',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

-- +migrate Down

-- ГВАРД ПЕРВЫМ, ДО ЛЮБОГО DDL, И ЭТО НЕ ОСТОРОЖНОСТЬ, А ЕДИНСТВЕННЫЙ КОРРЕКТНЫЙ ПОРЯДОК. Последний
-- шаг отката возвращает order_id в NOT NULL. У претензии прогона order_id пуст ПО ПОСТРОЕНИЮ (XOR),
-- поэтому на таких строках MODIFY упадёт — но упадёт он уже ПОСЛЕ того, как run_id со своим
-- значением дропнут, то есть после потери ровно тех данных, которые делали строку осмысленной.
-- Откат обязан отказать ДО первого DDL, пока владелец ещё записан.
--
-- SIGNAL в prepared-протоколе непреобразуем (проверено на 8.0.46, прецедент 0273/0281), поэтому
-- отказ — обращение к НЕСУЩЕСТВУЮЩЕЙ КОЛОНКЕ, чьё имя и есть сообщение: ERROR 1054,
-- детерминированно, без единого DDL. Идентификатор режется на 64 символах, поэтому текст короткий
-- и ASCII.
--
-- Что делать, если он сработал: такие претензии невыразимы в до-Ф5б реестре — у них нет заказа.
-- Закройте их release'ом и удалите строки прогонных претензий, либо не откатывайте. Молча свернуть
-- их в заказные нельзя: заказа, на который их свернуть, не существует.
--
-- Проверка существования КОЛОНКИ обязательна: файл должен быть перезапускаем после частичного
-- отката, а обращение к уже дропнутому run_id — это 1054, то есть отказ по неверной причине.
-- Условие смотрит на ОБА конца XOR: run_id заполнен ИЛИ order_id пуст. Второй дизъюнкт не
-- избыточен — он остаётся верным даже если CHECK кто-то снял руками, а именно он и описывает
-- строку, на которой упадёт восстановление NOT NULL.

SET @have_run_col := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'material_reservation_ledger'
      AND COLUMN_NAME = 'run_id');
SET @sql := IF(@have_run_col = 0, 'SELECT 0 INTO @blocking',
    'SELECT COUNT(*) INTO @blocking FROM material_reservation_ledger WHERE run_id IS NOT NULL OR order_id IS NULL');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

SET @sql := IF(@blocking = 0, 'SELECT 1',
    CONCAT('SELECT `0286 Down blocked: ', @blocking, ' run-owned reservations`'));
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

SET @has_chk := (SELECT COUNT(*) > 0 FROM information_schema.TABLE_CONSTRAINTS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'material_reservation_ledger'
      AND CONSTRAINT_NAME = 'chk_material_reservation_owner_xor');
SET @sql := IF(@has_chk,
    'ALTER TABLE material_reservation_ledger DROP CHECK chk_material_reservation_owner_xor',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

SET @has_fk := (SELECT COUNT(*) > 0 FROM information_schema.TABLE_CONSTRAINTS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'material_reservation_ledger'
      AND CONSTRAINT_NAME = 'fk_material_reservation_lot');
SET @sql := IF(@has_fk,
    'ALTER TABLE material_reservation_ledger DROP FOREIGN KEY fk_material_reservation_lot',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

SET @has_fk := (SELECT COUNT(*) > 0 FROM information_schema.TABLE_CONSTRAINTS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'material_reservation_ledger'
      AND CONSTRAINT_NAME = 'fk_material_reservation_run');
SET @sql := IF(@has_fk,
    'ALTER TABLE material_reservation_ledger DROP FOREIGN KEY fk_material_reservation_run',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

SET @has_idx := (SELECT COUNT(*) > 0 FROM information_schema.STATISTICS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'material_reservation_ledger'
      AND INDEX_NAME = 'idx_material_reservation_lot');
SET @sql := IF(@has_idx,
    'ALTER TABLE material_reservation_ledger DROP INDEX idx_material_reservation_lot',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

SET @has_idx := (SELECT COUNT(*) > 0 FROM information_schema.STATISTICS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'material_reservation_ledger'
      AND INDEX_NAME = 'idx_material_reservation_run');
SET @sql := IF(@has_idx,
    'ALTER TABLE material_reservation_ledger DROP INDEX idx_material_reservation_run',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

SET @has_col := (SELECT COUNT(*) > 0 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'material_reservation_ledger'
      AND COLUMN_NAME = 'lot_id');
SET @sql := IF(@has_col, 'ALTER TABLE material_reservation_ledger DROP COLUMN lot_id', 'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

SET @has_col := (SELECT COUNT(*) > 0 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'material_reservation_ledger'
      AND COLUMN_NAME = 'run_id');
SET @sql := IF(@has_col, 'ALTER TABLE material_reservation_ledger DROP COLUMN run_id', 'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

SET @is_nullable := (SELECT IS_NULLABLE FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'material_reservation_ledger'
      AND COLUMN_NAME = 'order_id');
SET @sql := IF(@is_nullable = 'YES',
    'ALTER TABLE material_reservation_ledger MODIFY COLUMN order_id INT NOT NULL',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;
