-- +migrate Up

-- ТРИ ЛОЖНЫХ РАСЩЕПЛЕНИЯ НА КОЛОНКАХ, КОТОРЫЕ ЖИВУТ НА ПРОДЕ ГОДАМИ.
--
-- ОТДЕЛЬНЫЙ ФАЙЛ ОТ 0327 НАМЕРЕННО, и это не стилистика. В 0327 «переносить нечего» доказывается
-- СХЕМОЙ: прод стоит на 0323, колонок волн 0324/0325 там физически нет, и ноль не может протухнуть
-- между замером и выкаткой. Здесь всё наоборот: machine_type заполнен у 92 из 105 строк прода,
-- tech_card_piece живёт с 0067, а профили оборудования владелец заводит руками. «Ноль» у трёх
-- снимаемых токенов — ЗАМЕР, снимок минуты, и минута между замером и выкаткой принадлежит не
-- автору файла. Отдельный файл даёт отдельный откат: если замер протухнет здесь, 0327 всё равно
-- уедет.
--
--   machine_type      снят `hardware_attach`  — он кодировал СПОСОБ КРЕПЛЕНИЯ в поле «на чём».
--   (шаг и парк)                                «Пришивная» — это attach_method = sew, а машина у
--                                               такого шага называется по имени (button_attach,
--                                               lockstitch, bartack). Пока оба написания жили
--                                               рядом, КАЖДОЕ БЫЛО НЕПОЛНЫМ: HARDWARE_SET + sew не
--                                               мог назвать машину, MACHINE + hardware_attach не
--                                               мог назвать способ. Технолог, ставящий пришивную
--                                               кнопку, обязан был выбрать, что потерять. Вместе с
--                                               этим файлом machine_type стал законен на глаголе
--                                               HARDWARE_SET (правило в Go) — шаг называет обе оси
--                                               сразу.
--   fusing_mode       снят `seam_allowance`   — он и `strip` кладут ОДНУ И ТУ ЖЕ полосу вдоль среза
--   (tech_card_piece)                           и различались ТОЛЬКО источником её ширины. Это
--                                               дословно width/edge отстрочки (0326). Ширина стала
--                                               ОПЦИОНАЛЬНОЙ у `strip`: пусто = эталон припуска
--                                               карточки (иначе цеха, 0277), число =
--                                               переопределение. Связь с эталоном не теряется, а
--                                               укрепляется — раньше его читал только один из двух
--                                               режимов.
--   thread_tension    снят `other`            — «другое, чем слабее / нормально / туже» не бывает:
--   (шаг и профиль)                             шкала УПОРЯДОЧЕНА, и «другое» её разрывает. Тот же
--                                               довод дом уже применил дважды — к
--                                               TechCardAutomationLevel и к TechCardPressureScale.
--                                               Что имелось в виду — «у меня есть конкретное
--                                               число», и число живёт в thread_tension_note,
--                                               законном рядом с ЛЮБОЙ ступенью.
--
-- ЧЕТЫРЕ CHECK'А, А НЕ ТРИ. И machine_type, и thread_tension стоят ДВАЖДЫ — на шаге и в парке
-- оборудования, — и это два РАЗНЫХ констрейнта с разными именами. Сузить один и забыть второй
-- значило бы развести словари: парк продолжил бы принимать токен, которого нет ни в контракте, ни
-- на шаге, а дрейф-тест профиля смотрит на свой якорь и остался бы зелёным.
--
-- ЗАМЕР ОБЕИХ БАЗ (2026-08-21 13:40 UTC, непосредственно перед выкаткой; команды приведены
-- дословно, чтобы их можно было повторить, а не поверить).
--
--   SELECT id FROM gorp_migrations ORDER BY id DESC LIMIT 1;
--     ПРОД (grbpwr):      0323_file_role_per_project.sql   (2026-08-18 22:14:56)
--     БЕТА (grbpwr_beta): 0326_topstitch_drop_width.sql    (2026-08-21 13:30:08)
--
--   SELECT COUNT(*), SUM(machine_type = 'hardware_attach'), SUM(machine_type IS NOT NULL),
--          SUM(thread_tension = 'other'), SUM(thread_tension IS NOT NULL)
--     FROM tech_card_operation;
--     ПРОД: 105 строк — machine_type заполнен у 92, `hardware_attach` НЕ ВСТРЕЧАЕТСЯ НИ РАЗУ;
--           thread_tension не заполнен ни у одной строки.
--     БЕТА: 11 строк — machine_type заполнен у 9, `hardware_attach` 0; thread_tension пуст весь.
--
--   SELECT COUNT(*), SUM(equipment = 'hardware_attach'), SUM(equipment IS NOT NULL),
--          SUM(thread_tension = 'other'), SUM(thread_tension IS NOT NULL)
--     FROM tech_card_equipment_profile;
--     ПРОД: 2 профиля — equipment заполнен у обоих, `hardware_attach` 0; thread_tension заполнен у
--           ОДНОГО, и это НЕ `other`.
--     БЕТА: 0 профилей.
--
--   SELECT COUNT(*), SUM(fusing_mode = 'seam_allowance'), SUM(fusing_mode IS NOT NULL)
--     FROM tech_card_piece;
--     ПРОД: 114 деталей, fusing_mode НЕ ЗАПОЛНЕН НИ У ОДНОЙ.
--     БЕТА: 33 детали, то же самое. Колонка родилась в 0304 и не приняла ни одного значения.
--
--   SELECT COUNT(*) FROM tech_card_signoff;  ПРОД 0, БЕТА 0
--   SELECT COUNT(*) FROM tech_card_release;  ПРОД 0, БЕТА 2
--
-- ⚠️ ЭТО СУЖЕНИЕ СЛОВАРНЫХ CHECK'ОВ, И ПЕРЕНОСИТЬ ТУТ НЕЧЕГО — а значит файл ОБЯЗАН УПАСТЬ ЧЕСТНО,
-- если замер протух. Он и падает: `ADD CONSTRAINT ... CHECK` в MySQL 8 перечитывает ВСЮ ИСТОРИЮ
-- таблицы, и одна строка со снятым токеном даёт 3819 С ИМЕНЕМ КОНСТРЕЙНТА, applied=0, строки в
-- gorp_migrations нет, приложение не стартует. Это громко и неприятно — и это правильный исход:
-- переносить `hardware_attach` было бы НЕКУДА (машина у такого шага называется по имени, и какая
-- именно — знает только технолог), а `other` натяжения означал «у меня есть число», которого в
-- колонке нет.
--
-- ⚠️ ОТДЕЛЬНО ПРО chk_tcp_fusing_mode: НА ДВУХ БАЗАХ ОН СЕГОДНЯ РАЗНЫЙ. На проде клауза несёт
-- STRCMP-гейт регистра, на бете — НЕТ:
--   ПРОД: fusing_mode IS NULL OR (fused = TRUE AND fusing_mode IN (...) AND STRCMP(...) = 0)
--   БЕТА: fusing_mode IS NULL OR (fused = TRUE AND fusing_mode IN (...))
-- Причина в гейте самой 0304: он проверял ФАКТ СУЩЕСТВОВАНИЯ констрейнта по имени, а не его
-- содержимое, поэтому бета получила первую редакцию файла (без STRCMP) и уже не увидела
-- исправленную. Этот файл пересоздаёт констрейнт СВОЕЙ клаузой и тем самым СВОДИТ ОБЕ БАЗЫ к
-- одному определению — с гейтом регистра. Гейт ниже поэтому смотрит на СОДЕРЖИМОЕ (наличие
-- снимаемого токена), а не на имя: по имени он был бы слеп ровно к тому расхождению, которое чинит.
--
-- ОТПЕЧАТКИ НЕ ДВИГАЮТСЯ. machine_type уезжает в дайджест ХВОСТОВОЙ ПАРОЙ «machine_type,
-- значение» — пара рождается только у заполненного поля, и снятие ЧЛЕНА словаря значение
-- заполненных строк не меняет (там 92 честные машины, ни одна из которых не `hardware_attach`).
-- fusing_mode уезжает парой «fusing, режим, ширина» только у РАЗМЕЧЕННОЙ детали, а размеченных нет
-- ни одной. thread_tension живёт в ВТО/машинном хвосте по тому же правилу. Голова кортежа не
-- трогается ни одной из трёх правок.
--
-- Номера и имена снятых членов ЗАРЕЗЕРВИРОВАНЫ в proto — и номером, и именем.

-- 1. ШАГ ТЕХ-КАРТЫ: ДВА СЛОВАРЯ ОДНИМ ALTER'ОМ, ОДНОЙ КОПИЕЙ ТАБЛИЦЫ.
--
-- ADD CONSTRAINT CHECK в 8.0 поддержан только ALGORITHM=COPY, поэтому переписать заодно COMMENT
-- колонки machine_type стоит ноль сверх уже оплаченной копии. MODIFY стоит ПЕРВЫМ и до ADD
-- CONSTRAINT — тот же порядок, что в 0324 и 0327. Тип повторён дословно из 0306, ни одна ширина не
-- меняется.
--
-- ГЕЙТ — ПО СОДЕРЖИМОМУ ОБЕИХ КЛАУЗ. `hardware_attach` встречается в списке машин ровно один раз, а
-- `|other|` в клаузе натяжения — тоже один (список кончается на нём, поэтому проба пишется как
-- '%|other)%', с закрывающей скобкой альтернации справа). Сумма 2 = ничего ещё не применено; после
-- успеха она равна 0, и повторный прогон не делает ничего.
SET @fs_op_pending := (
      (SELECT COUNT(*) FROM information_schema.TABLE_CONSTRAINTS tc
         JOIN information_schema.CHECK_CONSTRAINTS cc
           ON cc.CONSTRAINT_SCHEMA = tc.CONSTRAINT_SCHEMA AND cc.CONSTRAINT_NAME = tc.CONSTRAINT_NAME
        WHERE tc.TABLE_SCHEMA = DATABASE() AND tc.TABLE_NAME = 'tech_card_operation'
          AND tc.CONSTRAINT_NAME = 'chk_op_machine_type' AND cc.CHECK_CLAUSE LIKE '%hardware_attach%')
    + (SELECT COUNT(*) FROM information_schema.TABLE_CONSTRAINTS tc
         JOIN information_schema.CHECK_CONSTRAINTS cc
           ON cc.CONSTRAINT_SCHEMA = tc.CONSTRAINT_SCHEMA AND cc.CONSTRAINT_NAME = tc.CONSTRAINT_NAME
        WHERE tc.TABLE_SCHEMA = DATABASE() AND tc.TABLE_NAME = 'tech_card_operation'
          AND tc.CONSTRAINT_NAME = 'chk_op_thread_tension' AND cc.CHECK_CLAUSE LIKE '%|other)%'));
SET @ddl := IF(@fs_op_pending = 2,
    'ALTER TABLE tech_card_operation
        MODIFY COLUMN machine_type VARCHAR(32) NULL COMMENT ''на чём идёт шаг; REQUIRED у MACHINE, законен и у HARDWARE_SET (0328); NULL = не указано'',
        DROP CHECK chk_op_machine_type,
        DROP CHECK chk_op_thread_tension,
        ADD CONSTRAINT chk_op_machine_type CHECK (machine_type IS NULL OR (machine_type REGEXP ''^(lockstitch|lockstitch_double_needle|overlock|coverstitch|coverlock|chainstitch|blindstitch|zigzag|bartack|buttonhole|button_attach|embroidery|handstitch_imitation|elastic_attach|binding_taping|zipper_setting|gathering|patch_pocket_auto|welt_pocket_auto|template_auto|collar_cuff_auto|sleeve_setting_auto|waistband_auto|other|seam_taping|ultrasonic_welder)$'' AND STRCMP(CAST(machine_type AS BINARY), CAST(LOWER(machine_type) AS BINARY)) = 0)),
        ADD CONSTRAINT chk_op_thread_tension CHECK (thread_tension IS NULL OR (thread_tension REGEXP ''^(looser|normal|tighter)$'' AND STRCMP(CAST(thread_tension AS BINARY), CAST(LOWER(thread_tension) AS BINARY)) = 0))',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- 2. ПАРК ОБОРУДОВАНИЯ: ТЕ ЖЕ ДВА СЛОВАРЯ, ВТОРАЯ ТАБЛИЦА, ВТОРОЙ ALTER.
--
-- Своя таблица — своя копия, объединить с шагом нельзя. Список `equipment` — это ОБЪЕДИНЕНИЕ машин
-- и прессов (профиль хранит машину при kind='machine' и пресс при kind='press'), поэтому из него
-- уходит ровно один член и ровно из машинной половины; `other` в нём остаётся ОДИН на обе половины,
-- как и было, — дубль в альтернации был бы приглашением развести два списка молча.
SET @fs_eqp_pending := (
      (SELECT COUNT(*) FROM information_schema.TABLE_CONSTRAINTS tc
         JOIN information_schema.CHECK_CONSTRAINTS cc
           ON cc.CONSTRAINT_SCHEMA = tc.CONSTRAINT_SCHEMA AND cc.CONSTRAINT_NAME = tc.CONSTRAINT_NAME
        WHERE tc.TABLE_SCHEMA = DATABASE() AND tc.TABLE_NAME = 'tech_card_equipment_profile'
          AND tc.CONSTRAINT_NAME = 'chk_eqp_equipment' AND cc.CHECK_CLAUSE LIKE '%hardware_attach%')
    + (SELECT COUNT(*) FROM information_schema.TABLE_CONSTRAINTS tc
         JOIN information_schema.CHECK_CONSTRAINTS cc
           ON cc.CONSTRAINT_SCHEMA = tc.CONSTRAINT_SCHEMA AND cc.CONSTRAINT_NAME = tc.CONSTRAINT_NAME
        WHERE tc.TABLE_SCHEMA = DATABASE() AND tc.TABLE_NAME = 'tech_card_equipment_profile'
          AND tc.CONSTRAINT_NAME = 'chk_eqp_thread_tension' AND cc.CHECK_CLAUSE LIKE '%|other)%'));
SET @ddl := IF(@fs_eqp_pending = 2,
    'ALTER TABLE tech_card_equipment_profile
        DROP CHECK chk_eqp_equipment,
        DROP CHECK chk_eqp_thread_tension,
        ADD CONSTRAINT chk_eqp_equipment CHECK (equipment REGEXP ''^(lockstitch|lockstitch_double_needle|overlock|coverstitch|coverlock|chainstitch|blindstitch|zigzag|bartack|buttonhole|button_attach|embroidery|handstitch_imitation|elastic_attach|binding_taping|zipper_setting|gathering|patch_pocket_auto|welt_pocket_auto|template_auto|collar_cuff_auto|sleeve_setting_auto|waistband_auto|seam_taping|ultrasonic_welder|iron|press|fusing_press|steam_dummy|steamer|other)$'' AND STRCMP(CAST(equipment AS BINARY), CAST(LOWER(equipment) AS BINARY)) = 0),
        ADD CONSTRAINT chk_eqp_thread_tension CHECK (thread_tension IS NULL OR (thread_tension REGEXP ''^(looser|normal|tighter)$'' AND STRCMP(CAST(thread_tension AS BINARY), CAST(LOWER(thread_tension) AS BINARY)) = 0))',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- 3. ДЕТАЛЬ КРОЯ: СУЖЕНИЕ СЛОВАРЯ РЕЖИМА И РАСШИРЕНИЕ ПРАВИЛА ШИРИНЫ — ОДНИМ ALTER'ОМ.
--
-- ДВА КОНСТРЕЙНТА ОБЯЗАНЫ ДВИГАТЬСЯ ВМЕСТЕ, и порядок между ними здесь не важен только потому, что
-- они в одном операторе. chk_tcp_fusing_mode СУЖАЕТСЯ (уходит `seam_allowance`), а
-- chk_tcp_fusing_width РАСШИРЯЕТСЯ: до сих пор он требовал ширину при `strip` БЕЗУСЛОВНО
-- (`fusing_mode <=> 'strip' AND fusing_width_mm IS NOT NULL AND ...`), и без его правки снятие
-- `seam_allowance` отняло бы у технолога единственный способ сказать «полосой по эталону». Новая
-- клауза требует от числа только диапазон, когда оно есть, — и по-прежнему запрещает число при
-- любом другом режиме. Расширение ретроактивно не отвергает ничего.
--
-- COMMENT'ы ОБЕИХ КОЛОНОК переписываются здесь же: оба называли снятый режим и правило, которого
-- больше нет. Типы повторены дословно из 0304 (VARCHAR(16) и DECIMAL(6,1)), ни одна ширина не
-- меняется — самый длинный оставшийся токен `strip` короче снятого.
--
-- ГЕЙТ — по наличию `seam_allowance` в клаузе режима. Эта подстрока встречается там ровно один раз
-- и только как член списка IN; имя колонки её не содержит. Проба по ИМЕНИ констрейнта была бы
-- слепа к расхождению двух баз, описанному в шапке.
SET @fs_fusing_pending := (SELECT COUNT(*) FROM information_schema.TABLE_CONSTRAINTS tc
    JOIN information_schema.CHECK_CONSTRAINTS cc
      ON cc.CONSTRAINT_SCHEMA = tc.CONSTRAINT_SCHEMA AND cc.CONSTRAINT_NAME = tc.CONSTRAINT_NAME
    WHERE tc.TABLE_SCHEMA = DATABASE() AND tc.TABLE_NAME = 'tech_card_piece'
      AND tc.CONSTRAINT_NAME = 'chk_tcp_fusing_mode' AND cc.CHECK_CLAUSE LIKE '%seam_allowance%');
SET @ddl := IF(@fs_fusing_pending = 1,
    'ALTER TABLE tech_card_piece
        MODIFY COLUMN fusing_mode VARCHAR(16) NULL COMMENT ''как дублируется: full | strip; NULL = не размечено, читатель разворачивает в full'',
        MODIFY COLUMN fusing_width_mm DECIMAL(6,1) NULL COMMENT ''ширина клеевой полосы, мм; только при fusing_mode = strip и там НЕОБЯЗАТЕЛЬНА: пусто = эталон припуска карточки (иначе цеха, 0277)'',
        DROP CHECK chk_tcp_fusing_mode,
        DROP CHECK chk_tcp_fusing_width,
        ADD CONSTRAINT chk_tcp_fusing_mode CHECK (fusing_mode IS NULL OR (fused = TRUE AND fusing_mode IN (_utf8mb4''full'', _utf8mb4''strip'') AND STRCMP(CAST(fusing_mode AS BINARY), CAST(LOWER(fusing_mode) AS BINARY)) = 0)),
        ADD CONSTRAINT chk_tcp_fusing_width CHECK (
          (fusing_mode <=> _utf8mb4''strip'' AND (fusing_width_mm IS NULL OR (fusing_width_mm > 0 AND fusing_width_mm <= 100)))
          OR (NOT (fusing_mode <=> _utf8mb4''strip'') AND fusing_width_mm IS NULL))',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- +migrate Down

-- Down ВОЗВРАЩАЕТ СЛОВАРИ, а не записи, и по тем же доводам, что Down 0327 и 0325. Расширение
-- альтернации безопасно всегда; обратного переноса здесь нет и не требуется — переноса не было и в
-- Up.
--
-- chk_tcp_fusing_width СУЖАЕТСЯ ОБРАТНО, и это единственное сужение во всём откате. Оно законно
-- ровно до тех пор, пока ни одна деталь не успела воспользоваться новым правом «полоса без числа»:
-- такая строка отвергнется прежней клаузой, ALTER упадёт на 3819, и схема застрянет
-- полуоткатанной. Это честная цена, и её надо знать заранее — Down здесь инструмент разработки
-- (up → down → up на одноразовой базе), а не машина времени для прода.
--
-- chk_tcp_fusing_mode возвращается В РЕДАКЦИИ ПРОДА (со STRCMP), а не беты: бетина клауза была
-- первой редакцией 0304, которую её собственный гейт по имени не дал заменить. Возвращать дефект
-- ради дословности отката незачем — откат обязан вернуть РАБОТОСПОСОБНОСТЬ прежнему коду, а гейт
-- регистра прежнему коду не мешает.
--
-- Гейт зеркальный: откатывать есть что, только пока `seam_allowance` в клаузе НЕТ.
SET @fs_fusing_applied := (SELECT COUNT(*) FROM information_schema.TABLE_CONSTRAINTS tc
    JOIN information_schema.CHECK_CONSTRAINTS cc
      ON cc.CONSTRAINT_SCHEMA = tc.CONSTRAINT_SCHEMA AND cc.CONSTRAINT_NAME = tc.CONSTRAINT_NAME
    WHERE tc.TABLE_SCHEMA = DATABASE() AND tc.TABLE_NAME = 'tech_card_piece'
      AND tc.CONSTRAINT_NAME = 'chk_tcp_fusing_mode' AND cc.CHECK_CLAUSE NOT LIKE '%seam_allowance%');
SET @ddl := IF(@fs_fusing_applied = 1,
    'ALTER TABLE tech_card_piece
        MODIFY COLUMN fusing_mode VARCHAR(16) NULL COMMENT ''как дублируется: full | seam_allowance | strip; NULL = не размечено, читатель разворачивает в full'',
        MODIFY COLUMN fusing_width_mm DECIMAL(6,1) NULL COMMENT ''ширина клеевой полосы, мм; только и обязательно при fusing_mode = strip'',
        DROP CHECK chk_tcp_fusing_mode,
        DROP CHECK chk_tcp_fusing_width,
        ADD CONSTRAINT chk_tcp_fusing_mode CHECK (fusing_mode IS NULL OR (fused = TRUE AND fusing_mode IN (_utf8mb4''full'', _utf8mb4''seam_allowance'', _utf8mb4''strip'') AND STRCMP(CAST(fusing_mode AS BINARY), CAST(LOWER(fusing_mode) AS BINARY)) = 0)),
        ADD CONSTRAINT chk_tcp_fusing_width CHECK (
          (fusing_mode <=> _utf8mb4''strip'' AND fusing_width_mm IS NOT NULL AND fusing_width_mm > 0 AND fusing_width_mm <= 100)
          OR (NOT (fusing_mode <=> _utf8mb4''strip'') AND fusing_width_mm IS NULL))',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @fs_eqp_applied := (SELECT COUNT(*) FROM information_schema.TABLE_CONSTRAINTS tc
    JOIN information_schema.CHECK_CONSTRAINTS cc
      ON cc.CONSTRAINT_SCHEMA = tc.CONSTRAINT_SCHEMA AND cc.CONSTRAINT_NAME = tc.CONSTRAINT_NAME
    WHERE tc.TABLE_SCHEMA = DATABASE() AND tc.TABLE_NAME = 'tech_card_equipment_profile'
      AND tc.CONSTRAINT_NAME = 'chk_eqp_equipment' AND cc.CHECK_CLAUSE NOT LIKE '%hardware_attach%');
SET @ddl := IF(@fs_eqp_applied = 1,
    'ALTER TABLE tech_card_equipment_profile
        DROP CHECK chk_eqp_equipment,
        DROP CHECK chk_eqp_thread_tension,
        ADD CONSTRAINT chk_eqp_equipment CHECK (equipment REGEXP ''^(lockstitch|lockstitch_double_needle|overlock|coverstitch|coverlock|chainstitch|blindstitch|zigzag|bartack|buttonhole|button_attach|embroidery|handstitch_imitation|hardware_attach|elastic_attach|binding_taping|zipper_setting|gathering|patch_pocket_auto|welt_pocket_auto|template_auto|collar_cuff_auto|sleeve_setting_auto|waistband_auto|seam_taping|ultrasonic_welder|iron|press|fusing_press|steam_dummy|steamer|other)$'' AND STRCMP(CAST(equipment AS BINARY), CAST(LOWER(equipment) AS BINARY)) = 0),
        ADD CONSTRAINT chk_eqp_thread_tension CHECK (thread_tension IS NULL OR (thread_tension REGEXP ''^(looser|normal|tighter|other)$'' AND STRCMP(CAST(thread_tension AS BINARY), CAST(LOWER(thread_tension) AS BINARY)) = 0))',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @fs_op_applied := (SELECT COUNT(*) FROM information_schema.TABLE_CONSTRAINTS tc
    JOIN information_schema.CHECK_CONSTRAINTS cc
      ON cc.CONSTRAINT_SCHEMA = tc.CONSTRAINT_SCHEMA AND cc.CONSTRAINT_NAME = tc.CONSTRAINT_NAME
    WHERE tc.TABLE_SCHEMA = DATABASE() AND tc.TABLE_NAME = 'tech_card_operation'
      AND tc.CONSTRAINT_NAME = 'chk_op_machine_type' AND cc.CHECK_CLAUSE NOT LIKE '%hardware_attach%');
SET @ddl := IF(@fs_op_applied = 1,
    'ALTER TABLE tech_card_operation
        MODIFY COLUMN machine_type VARCHAR(32) NULL COMMENT ''на чём идёт шаг; REQUIRED у MACHINE; NULL = не указано'',
        DROP CHECK chk_op_machine_type,
        DROP CHECK chk_op_thread_tension,
        ADD CONSTRAINT chk_op_machine_type CHECK (machine_type IS NULL OR (machine_type REGEXP ''^(lockstitch|lockstitch_double_needle|overlock|coverstitch|coverlock|chainstitch|blindstitch|zigzag|bartack|buttonhole|button_attach|embroidery|handstitch_imitation|hardware_attach|elastic_attach|binding_taping|zipper_setting|gathering|patch_pocket_auto|welt_pocket_auto|template_auto|collar_cuff_auto|sleeve_setting_auto|waistband_auto|other|seam_taping|ultrasonic_welder)$'' AND STRCMP(CAST(machine_type AS BINARY), CAST(LOWER(machine_type) AS BINARY)) = 0)),
        ADD CONSTRAINT chk_op_thread_tension CHECK (thread_tension IS NULL OR (thread_tension REGEXP ''^(looser|normal|tighter|other)$'' AND STRCMP(CAST(thread_tension AS BINARY), CAST(LOWER(thread_tension) AS BINARY)) = 0))',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;
