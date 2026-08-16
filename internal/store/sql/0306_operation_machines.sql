-- +migrate Up

-- МАШИНКИ И РЕЖИМЫ ВТО. Шаг тех-карты перестаёт отвечать одним значением на два вопроса. Было:
-- operation_type = 'lockstitch' — это одновременно «что делаем» и «на чём». Стало: тип шага —
-- шесть глаголов (machine / press / press_open / fusing / handwork / other), а «на чём» — отдельная
-- ось: machine_type у машинного шага, press_equipment у утюжильного. Настройки, общие для всей
-- карточки, переезжают из двух колонок tech_card_construction в НОВУЮ ТАБЛИЦУ ПРОФИЛЕЙ:
-- профилей одного типа может быть сколько угодно («два одинаковых станка» — ответ владельца),
-- а строка `pressing` их не выражала в принципе, потому что была прозой.
--
-- ВСЕ ЧИСЛОВЫЕ ГРАНИЦЫ НИЖЕ — САНИТИ, НЕ СТАНДАРТ. 40..250 °C, 1..300 с, 1..100 Н/см², Nm 35..300,
-- 1..20 ниток, 0..20 мм ширины стежка существуют, чтобы поймать опечатку до цеха («14» вместо
-- «140», игла «9» вместо Nm 90), а не чтобы объявить, на что машина имеет право быть настроена.
-- Каждая граница продублирована Go-проверкой с читаемым сообщением: CHECK в одиночку отвечает
-- ошибкой 3819, где нет ни поля, ни слов (довод 0278/0289).
--
-- NULL ЗНАЧИТ «НАСЛЕДУЕТСЯ», И ЭТО НЕ НОЛЬ И НЕ 'none' (приём 0289). Поэтому у attachment_kind и
-- press_cloth есть отдельный токен 'none': без него шаг не мог бы ОТМЕНИТЬ лапку или
-- проутюжильник профиля — NULL уже занят смыслом «возьми из профиля».
--
-- ССЫЛКА ОПЕРАЦИИ НА ПРОФИЛЬ — МЯГКАЯ, ПО КЛЮЧУ, БЕЗ FK. profile_key — durable-ключ по образцу
-- bom_line_key / piece_line_key: клиент минтит ULID при создании строки, ключ переживает
-- full-replace, потому что клиент его round-trip-ит. FK с ON DELETE был бы ложью — список профилей
-- полностью переписывается каждым сохранением карточки, и ссылка на исчезнувший профиль означает
-- «наследовать нечего», а не «строка операции недействительна».
--
-- КЛЮЧ — ЭТО ИДЕНТИЧНОСТЬ, ПОЭТОМУ СРАВНЕНИЕ ДВОИЧНОЕ. У profile_key и у обеих ссылок шага
-- объявлено `COLLATE utf8mb4_bin`, а не коллация схемы по умолчанию (utf8mb3_general_ci на проде,
-- utf8mb4_0900_ai_ci в контейнере) — обе регистронезависимы. Без этой строки база и Go расходятся
-- в том, ЧТО СЧИТАТЬ ОДНИМ КЛЮЧОМ: Go сравнивает ключи побайтно (проверка дублей парка в
-- parseTechCardEquipmentDefaults и резолв ссылки шага в resolveProfileKey), а ci-коллация склеивала
-- бы 'abc…' и 'ABC…' в один. Расхождение стреляло дважды и в разные стороны: два профиля,
-- различающиеся только регистром, проходили проверку дублей в Go и падали на UNIQUE сырой 1062; а
-- ссылка шага, отличающаяся от ключа профиля регистром, проходила форматную проверку, НЕ находилась
-- при резолве в Go и молча уезжала в NULL — полная замена сохраняла эту отвязку навсегда.
-- Регистр НЕ нормализуется ни здесь, ни в dto: ключ минтит клиент, и переписывать чужую
-- идентичность сервер не вправе; правило сравнения приводится к Go, а не наоборот.
-- UNIQUE(tech_card_id, profile_key) собственной коллации не имеет — он наследует коллацию колонки,
-- поэтому одной правки колонки достаточно, отдельной правки индексу не нужно.
-- Именно utf8mb4_bin, а не `CHAR(26) BINARY`: атрибут BINARY в MySQL 8.0 объявлен deprecated
-- (предупреждение 1287) и брал бы коллацию от charset'а схемы, то есть на проде и в контейнере
-- разную. Смешанное сравнение utf8mb4_bin-колонки с utf8mb3_general_ci-колонкой проверено на 8.0.46:
-- 1267 не стреляет, побеждает _bin — то есть двоичность распространяется на сравнение, а не ломает его.
--
-- DOWN ЧЕСТНО LOSSY. Он возвращает КОЛОНКИ, но не содержимое: профили дропаются вместе с таблицей,
-- перенесённая в notes проза остаётся в notes, а pressing / overlock_thread_count возвращаются
-- ПУСТЫМИ. Цикл up → down → up сохраняет схему и не сохраняет данные — это записано здесь, чтобы
-- никто не принял Down за машину времени. Down НЕ воссоздаёт легаси-CHECK типа операции из 0076 и
-- НЕ сужает chk_op_attachment_kind обратно: и то и другое — ретроактивная проверка ВСЕЙ таблицы,
-- которая уронила бы старт прода на строках, уже переложенных в 'machine' / 'none'.
--
-- ИДЕМПОТЕНТНОСТЬ: MySQL 8 автокоммитит DDL, поэтому падение в середине оставляет схему
-- полуприменённой без строки в gorp_migrations, и следующая загрузка перезапускает файл с начала.
-- Каждый шаг — под собственным гейтом information_schema, PREPARE / EXECUTE / DEALLOCATE по одному
-- оператору на строку (иначе multiStatements=true в контейнерных тестах маскирует поломку, которая
-- на проде валит старт). Файл в gorp_migrations значится ПОЛНЫМ ИМЕНЕМ — после первого прогона не
-- переименовывать.

-- 1. Профили оборудования карточки. Гейт — IF NOT EXISTS.
--
--    tech_card_id — SIGNED INT, а не INT UNSIGNED: родитель tech_card.id объявлен в 0067 как
--    `id INT PRIMARY KEY AUTO_INCREMENT`, то есть signed, и MySQL 8 отвергает внешний ключ между
--    колонками разной знаковости ошибкой 3780. Собственный id таблицы UNSIGNED — он ничей родитель.
--
--    Пара kind ↔ словарь equipment и правило «у machine-строки пусты press_*-колонки и наоборот»
--    проверяются в Go, а не двухколоночным CHECK: 3819 назвал бы колонку, которой оператор не
--    касался. Схема закрывает словари, Go закрывает раскладку. Токен 'other' законен под обоими
--    kind — коллизия безвредна, потому что «другое» и там и там значит «см. note».
CREATE TABLE IF NOT EXISTS tech_card_equipment_profile (
  id INT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
  tech_card_id INT NOT NULL COMMENT 'SIGNED: tech_card.id signed с 0067, UNSIGNED здесь = отказ FK 3780',
  profile_key CHAR(26) COLLATE utf8mb4_bin NOT NULL COMMENT 'durable ULID строки профиля; идентичность профиля, переживает full-replace. _bin: сравнение как в Go, побайтно — см. шапку',
  kind VARCHAR(8) NOT NULL COMMENT 'machine | press — какое из двух сообщений проводa породило строку',
  equipment VARCHAR(32) NOT NULL COMMENT 'токен machine-словаря ИЛИ press-словаря, по kind',
  label VARCHAR(64) NULL COMMENT 'имя для человека (оверлок у окна); В КЛЮЧ НЕ ВХОДИТ',
  thread_count TINYINT NULL COMMENT 'kind=machine: ниток на машине',
  needle_type VARCHAR(16) NULL COMMENT 'kind=machine: тип острия иглы',
  needle_size_nm SMALLINT NULL COMMENT 'kind=machine: номер иглы Nm (Nm 90 = лезвие 0.90 мм)',
  bed_type VARCHAR(16) NULL COMMENT 'kind=machine: платформа; ИДЕНТИЧНОСТЬ машины, на шаге override нет',
  automation VARCHAR(12) NULL COMMENT 'kind=machine: уровень автоматизации; идентичность машины',
  thread_tension VARCHAR(8) NULL COMMENT 'kind=machine: натяжение ОТНОСИТЕЛЬНО обычного для этой машины',
  thread_tension_note VARCHAR(64) NULL COMMENT 'kind=machine: уточнение к шкале натяжения, только вместе с ней',
  attachment_kind VARCHAR(24) NULL COMMENT 'kind=machine: лапка / приспособление по умолчанию; none законен',
  stitches_per_cm DECIMAL(5,2) NULL COMMENT 'kind=machine: плотность строчки, стежков на САНТИМЕТР',
  stitch_width_mm DECIMAL(4,1) NULL COMMENT 'kind=machine: ШИРИНА стежка (размах зигзага, ширина обмётки); НЕ отступ отстрочки',
  press_operation_type VARCHAR(16) NULL COMMENT 'kind=press: для какого процесса режим; NULL = универсальный',
  press_temperature_c SMALLINT NULL COMMENT 'kind=press: температура, °C',
  press_dwell_sec SMALLINT NULL COMMENT 'kind=press: выдержка, секунды',
  press_pressure_n_cm2 DECIMAL(5,1) NULL COMMENT 'kind=press: ДАВЛЕНИЕ НА МАТЕРИАЛ, Н/см² — единица в имени колонки',
  press_steam TINYINT(1) NULL COMMENT 'kind=press: пар; NULL = не задано, 0 = ЯВНОЕ без пара',
  press_cloth VARCHAR(24) NULL COMMENT 'kind=press: проутюжильник; none = явно без него',
  note VARCHAR(255) NULL COMMENT 'модель машины, расшифровка other',
  CONSTRAINT fk_equipment_profile_card FOREIGN KEY (tech_card_id) REFERENCES tech_card(id) ON DELETE CASCADE,
  CONSTRAINT uq_equipment_profile_key UNIQUE (tech_card_id, profile_key),
  CONSTRAINT chk_eqp_kind CHECK (kind REGEXP '^(machine|press)$'
    AND STRCMP(CAST(kind AS BINARY), CAST(LOWER(kind) AS BINARY)) = 0),
  CONSTRAINT chk_eqp_equipment CHECK (equipment REGEXP '^(lockstitch|lockstitch_double_needle|overlock|coverstitch|coverlock|chainstitch|blindstitch|zigzag|bartack|buttonhole|button_attach|embroidery|handstitch_imitation|hardware_attach|elastic_attach|binding_taping|zipper_setting|gathering|patch_pocket_auto|welt_pocket_auto|template_auto|collar_cuff_auto|sleeve_setting_auto|waistband_auto|iron|press|fusing_press|steam_dummy|steamer|other)$'
    AND STRCMP(CAST(equipment AS BINARY), CAST(LOWER(equipment) AS BINARY)) = 0),
  CONSTRAINT chk_eqp_needle_type CHECK (needle_type IS NULL OR (needle_type REGEXP '^(universal|ballpoint|stretch|jeans|leather|microtex|embroidery|other)$'
    AND STRCMP(CAST(needle_type AS BINARY), CAST(LOWER(needle_type) AS BINARY)) = 0)),
  CONSTRAINT chk_eqp_bed_type CHECK (bed_type IS NULL OR (bed_type REGEXP '^(flatbed|cylinder_bed|post_bed|feed_off_arm|other)$'
    AND STRCMP(CAST(bed_type AS BINARY), CAST(LOWER(bed_type) AS BINARY)) = 0)),
  CONSTRAINT chk_eqp_automation CHECK (automation IS NULL OR (automation REGEXP '^(basic|semi_auto|auto)$'
    AND STRCMP(CAST(automation AS BINARY), CAST(LOWER(automation) AS BINARY)) = 0)),
  CONSTRAINT chk_eqp_thread_tension CHECK (thread_tension IS NULL OR (thread_tension REGEXP '^(looser|normal|tighter|other)$'
    AND STRCMP(CAST(thread_tension AS BINARY), CAST(LOWER(thread_tension) AS BINARY)) = 0)),
  CONSTRAINT chk_eqp_attachment CHECK (attachment_kind IS NULL OR (attachment_kind REGEXP '^(binder|hemmer_folder|scroll_foot|zipper_foot|invisible_zipper_foot|edge_guide|piping_foot|elastic_attachment|teflon_foot|roller_foot|none|other)$'
    AND STRCMP(CAST(attachment_kind AS BINARY), CAST(LOWER(attachment_kind) AS BINARY)) = 0)),
  CONSTRAINT chk_eqp_press_op_type CHECK (press_operation_type IS NULL OR (press_operation_type REGEXP '^(press|press_open|fusing)$'
    AND STRCMP(CAST(press_operation_type AS BINARY), CAST(LOWER(press_operation_type) AS BINARY)) = 0)),
  CONSTRAINT chk_eqp_press_cloth CHECK (press_cloth IS NULL OR (press_cloth REGEXP '^(none|press_cloth|damp_press_cloth|teflon_sheet|other)$'
    AND STRCMP(CAST(press_cloth AS BINARY), CAST(LOWER(press_cloth) AS BINARY)) = 0)),
  CONSTRAINT chk_eqp_thread_count CHECK (thread_count IS NULL OR (thread_count >= 1 AND thread_count <= 20)),
  CONSTRAINT chk_eqp_needle_size CHECK (needle_size_nm IS NULL OR (needle_size_nm >= 35 AND needle_size_nm <= 300)),
  CONSTRAINT chk_eqp_stitches CHECK (stitches_per_cm IS NULL OR (stitches_per_cm >= 1 AND stitches_per_cm <= 20)),
  CONSTRAINT chk_eqp_stitch_width CHECK (stitch_width_mm IS NULL OR (stitch_width_mm >= 0 AND stitch_width_mm <= 20)),
  CONSTRAINT chk_eqp_temp CHECK (press_temperature_c IS NULL OR (press_temperature_c >= 40 AND press_temperature_c <= 250)),
  CONSTRAINT chk_eqp_dwell CHECK (press_dwell_sec IS NULL OR (press_dwell_sec >= 1 AND press_dwell_sec <= 300)),
  CONSTRAINT chk_eqp_pressure CHECK (press_pressure_n_cm2 IS NULL OR (press_pressure_n_cm2 >= 1 AND press_pressure_n_cm2 <= 100))
) ENGINE=InnoDB COMMENT 'Профили оборудования карточки: машинки и режимы ВТО, на которые ссылаются шаги';

-- 2. Пятнадцать колонок операции одним ALTER. Все NULL — вся существующая история проходит, потому
--    что каждый CHECK начинается с `IS NULL OR`.
SET @op_new := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_operation'
      AND COLUMN_NAME = 'machine_type');
SET @ddl := IF(@op_new = 0,
    'ALTER TABLE tech_card_operation
        ADD COLUMN machine_type VARCHAR(32) NULL COMMENT ''машинка машинного шага; NULL = шаг не машинный либо не указано'',
        ADD COLUMN machine_profile_key CHAR(26) COLLATE utf8mb4_bin NULL COMMENT ''мягкая ссылка на профиль карточки; FK намеренно нет, профили переписываются каждым сохранением. _bin: та же идентичность, что у profile_key'',
        ADD COLUMN thread_count TINYINT NULL COMMENT ''override числа ниток; NULL = наследуется от профиля'',
        ADD COLUMN needle_type VARCHAR(16) NULL COMMENT ''override типа иглы; NULL = наследуется'',
        ADD COLUMN needle_size_nm SMALLINT NULL COMMENT ''override номера иглы Nm; NULL = наследуется'',
        ADD COLUMN thread_tension VARCHAR(8) NULL COMMENT ''override натяжения (шкала относительно обычного); NULL = наследуется'',
        ADD COLUMN thread_tension_note VARCHAR(64) NULL COMMENT ''уточнение к шкале натяжения; без шкалы запрещено'',
        ADD COLUMN stitch_width_mm DECIMAL(4,1) NULL COMMENT ''ШИРИНА стежка, мм; НЕ путать с topstitch_width_mm (отступ от края)'',
        ADD COLUMN press_equipment VARCHAR(16) NULL COMMENT ''оборудование ВТО-шага; NULL = шаг не утюжильный либо не указано'',
        ADD COLUMN press_profile_key CHAR(26) COLLATE utf8mb4_bin NULL COMMENT ''мягкая ссылка на press-профиль карточки; _bin, как и machine_profile_key'',
        ADD COLUMN press_temperature_c SMALLINT NULL COMMENT ''override температуры, °C; NULL = наследуется'',
        ADD COLUMN press_dwell_sec SMALLINT NULL COMMENT ''override выдержки, с; NULL = наследуется'',
        ADD COLUMN press_pressure_n_cm2 DECIMAL(5,1) NULL COMMENT ''override давления, Н/см²; NULL = наследуется'',
        ADD COLUMN press_steam TINYINT(1) NULL COMMENT ''override пара; NULL = наследуется, 0 = ЯВНОЕ без пара'',
        ADD COLUMN press_cloth VARCHAR(24) NULL COMMENT ''override проутюжильника; none = явно без него, NULL = наследуется'',
        ADD CONSTRAINT chk_op_machine_type CHECK (machine_type IS NULL OR (machine_type REGEXP ''^(lockstitch|lockstitch_double_needle|overlock|coverstitch|coverlock|chainstitch|blindstitch|zigzag|bartack|buttonhole|button_attach|embroidery|handstitch_imitation|hardware_attach|elastic_attach|binding_taping|zipper_setting|gathering|patch_pocket_auto|welt_pocket_auto|template_auto|collar_cuff_auto|sleeve_setting_auto|waistband_auto|other)$'' AND STRCMP(CAST(machine_type AS BINARY), CAST(LOWER(machine_type) AS BINARY)) = 0)),
        ADD CONSTRAINT chk_op_press_equipment CHECK (press_equipment IS NULL OR (press_equipment REGEXP ''^(iron|press|fusing_press|steam_dummy|steamer|other)$'' AND STRCMP(CAST(press_equipment AS BINARY), CAST(LOWER(press_equipment) AS BINARY)) = 0)),
        ADD CONSTRAINT chk_op_needle_type CHECK (needle_type IS NULL OR (needle_type REGEXP ''^(universal|ballpoint|stretch|jeans|leather|microtex|embroidery|other)$'' AND STRCMP(CAST(needle_type AS BINARY), CAST(LOWER(needle_type) AS BINARY)) = 0)),
        ADD CONSTRAINT chk_op_thread_tension CHECK (thread_tension IS NULL OR (thread_tension REGEXP ''^(looser|normal|tighter|other)$'' AND STRCMP(CAST(thread_tension AS BINARY), CAST(LOWER(thread_tension) AS BINARY)) = 0)),
        ADD CONSTRAINT chk_op_press_cloth CHECK (press_cloth IS NULL OR (press_cloth REGEXP ''^(none|press_cloth|damp_press_cloth|teflon_sheet|other)$'' AND STRCMP(CAST(press_cloth AS BINARY), CAST(LOWER(press_cloth) AS BINARY)) = 0)),
        ADD CONSTRAINT chk_op_thread_count CHECK (thread_count IS NULL OR (thread_count >= 1 AND thread_count <= 20)),
        ADD CONSTRAINT chk_op_needle_size CHECK (needle_size_nm IS NULL OR (needle_size_nm >= 35 AND needle_size_nm <= 300)),
        ADD CONSTRAINT chk_op_stitch_width CHECK (stitch_width_mm IS NULL OR (stitch_width_mm >= 0 AND stitch_width_mm <= 20)),
        ADD CONSTRAINT chk_op_press_temp CHECK (press_temperature_c IS NULL OR (press_temperature_c >= 40 AND press_temperature_c <= 250)),
        ADD CONSTRAINT chk_op_press_dwell CHECK (press_dwell_sec IS NULL OR (press_dwell_sec >= 1 AND press_dwell_sec <= 300)),
        ADD CONSTRAINT chk_op_press_pressure CHECK (press_pressure_n_cm2 IS NULL OR (press_pressure_n_cm2 >= 1 AND press_pressure_n_cm2 <= 100))',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- 3. РЕГИСТР ДО СТРОГОГО СЛОВАРЯ. Старая проверка из 0076 — голый REGEXP без STRCMP, а коллация
--    колонки регистронезависима и на utf8mb3_general_ci прода, и на utf8mb4_0900_ai_ci контейнера.
--    Значит 'LOCKSTITCH' лежит в базе ЗАКОННО, и добавление строгого lowercase-CHECK-а шагом 6 без
--    этого UPDATE-а проверило бы ВСЮ таблицу и уронило старт прода (память
--    retroactive-check-halts-deploy). Идемпотентен: WHERE отбирает только строки не в нижнем
--    регистре, повторный прогон находит ноль. Старый ci-CHECK этим UPDATE-ом не нарушается.
UPDATE tech_card_operation SET operation_type = LOWER(operation_type)
WHERE STRCMP(CAST(operation_type AS BINARY), CAST(LOWER(operation_type) AS BINARY)) <> 0;

-- 4. Снять старый словарь типов операции. Он пришёл из 0076 БЕЗЫМЯННЫМ (позиционное
--    tech_card_operation_chk_N), поэтому ищется ПО СОДЕРЖИМОМУ CLAUSE, а не по имени и не по
--    CONSTRAINT_NAME LIKE. Сочетание 'handwork' + 'double_needle' однозначно: chk_op_machine_type
--    (шаг 2) содержит lockstitch_double_needle, но не handwork; chk_op_operation_type (шаг 6)
--    содержит handwork, но не double_needle. Значит и на первом прогоне, и на любом повторном
--    подзапрос видит либо ровно ту одну строку, либо ни одной.
--
--    БЕЗ LIMIT И БЕЗ ORDER BY НАМЕРЕННО: подзапрос, вернувший больше одной строки, падает ошибкой
--    1242, и это ШТАТНЫЙ СТОП-КРАН — «нашлось два кандидата» значит, что схема не та, которую этот
--    файл описывает, и дропать вслепую нельзя. NULL (уже дропнут) вырождается в 'SELECT 1'.
--
--    ПОРЯДОК: строго ДО шага 5, потому что токенов 'machine' и 'press' в старом словаре нет.
SET @old_chk := (SELECT c.CONSTRAINT_NAME
    FROM information_schema.CHECK_CONSTRAINTS c
    JOIN information_schema.TABLE_CONSTRAINTS t
      ON t.CONSTRAINT_SCHEMA = c.CONSTRAINT_SCHEMA AND t.CONSTRAINT_NAME = c.CONSTRAINT_NAME
    WHERE c.CONSTRAINT_SCHEMA = DATABASE() AND t.TABLE_NAME = 'tech_card_operation'
      AND c.CHECK_CLAUSE LIKE '%handwork%' AND c.CHECK_CLAUSE LIKE '%double_needle%');
SET @ddl := IF(@old_chk IS NULL, 'SELECT 1',
    CONCAT('ALTER TABLE tech_card_operation DROP CHECK ', @old_chk));
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- 5. ПЕРЕКЛАДКА ДЕВЯТИ ЛЕГАСИ-ТИПОВ в (machine, machine_type). Эта же таблица живёт в Go как
--    entity.LegacyOperationMachineType (её сверяет линт migrationlint), потому что у неё три
--    потребителя: dto канонизирует ею запись, эта миграция переносит ею историю, а компат-проекция
--    дайджеста ходит по ней обратно — и именно поэтому ни одна подписанная карточка не протухает.
--    'blindhem' → 'blindstitch' и 'double_needle' → 'lockstitch_double_needle' — единственные два
--    переименования; двухигольная машина заведена в словарь ровно затем, чтобы эта строка не
--    схлопнулась в прямострочную.
--    'fusing' / 'handwork' / 'other' / 'unknown' не трогаются — они остаются типами шага.
--    Идемпотентно: WHERE отбирает только старые токены, после первого прогона их нет.
UPDATE tech_card_operation SET
  machine_type = CASE operation_type
    WHEN 'lockstitch' THEN 'lockstitch'   WHEN 'double_needle' THEN 'lockstitch_double_needle'
    WHEN 'overlock' THEN 'overlock'       WHEN 'coverstitch' THEN 'coverstitch'
    WHEN 'chainstitch' THEN 'chainstitch' WHEN 'blindhem' THEN 'blindstitch'
    WHEN 'bartack' THEN 'bartack'         WHEN 'buttonhole' THEN 'buttonhole'
    WHEN 'button_attach' THEN 'button_attach' END,
  operation_type = 'machine'
WHERE operation_type IN ('lockstitch','double_needle','overlock','coverstitch',
  'chainstitch','blindhem','bartack','buttonhole','button_attach');

-- 6. Новый словарь типов операции — именованный и регистро-строгий. После шагов 3-5 вся история
--    проходит: в колонке остались только unknown / machine / press / press_open / fusing /
--    handwork / other, все в нижнем регистре.
SET @op_type_chk := (SELECT COUNT(*) FROM information_schema.TABLE_CONSTRAINTS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_operation'
      AND CONSTRAINT_NAME = 'chk_op_operation_type');
SET @ddl := IF(@op_type_chk = 0,
    'ALTER TABLE tech_card_operation ADD CONSTRAINT chk_op_operation_type CHECK (operation_type REGEXP ''^(unknown|machine|press|press_open|fusing|handwork|other)$'' AND STRCMP(CAST(operation_type AS BINARY), CAST(LOWER(operation_type) AS BINARY)) = 0)',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- 7. Расширение словаря лапок 0289 тремя токенами: teflon_foot, roller_foot и — главное — 'none'.
--    До профилей «без лапки» и «не указано» были одним фактом и оба писались NULL; теперь NULL
--    значит «возьми лапку профиля», и без токена шаг не смог бы её отменить.
--    Гейт — по СОДЕРЖИМОМУ существующего clause, а не по наличию имени: имя есть в обоих случаях.
--    walking_foot в словарь не идёт намеренно: промышленно это машина с шагающей лапкой (unison
--    feed) — свойство транспорта, а не сменное приспособление шага.
SET @att_old := (SELECT COUNT(*) FROM information_schema.CHECK_CONSTRAINTS
    WHERE CONSTRAINT_SCHEMA = DATABASE() AND CONSTRAINT_NAME = 'chk_op_attachment_kind'
      AND CHECK_CLAUSE NOT LIKE '%roller_foot%');
SET @ddl := IF(@att_old = 1,
    'ALTER TABLE tech_card_operation
        DROP CHECK chk_op_attachment_kind,
        ADD CONSTRAINT chk_op_attachment_kind CHECK (attachment_kind IS NULL OR (attachment_kind REGEXP ''^(binder|hemmer_folder|scroll_foot|zipper_foot|invisible_zipper_foot|edge_guide|piping_foot|elastic_attachment|teflon_foot|roller_foot|none|other)$'' AND STRCMP(CAST(attachment_kind AS BINARY), CAST(LOWER(attachment_kind) AS BINARY)) = 0))',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- 8. ПЕРЕЕЗД ДВУХ КОЛОНОК БЛОКА ДЕФОЛТОВ. Три шага под ОДНИМ гейтом «колонка pressing ещё есть»:
--    после 8c обе колонки исчезают, и статические 8a / 8b перестали бы даже парситься — поэтому
--    они динамические, а не потому, что им нужно что-то вычислять.
SET @has_pressing := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_construction'
      AND COLUMN_NAME = 'pressing');

-- 8a. Счётчик оверлочных ниток становится профилем машинки. Ключ детерминированный
--     ('LEGACYOVERLOCK' + 12 цифр id = ровно 26 alnum-символов, формат validatePatternLineKey), и
--     идемпотентность даёт NOT EXISTS по нему, а НЕ ON DUPLICATE KEY UPDATE: VALUES() в MySQL 8
--     deprecated, а alias-форма читалась бы как «перезаписать профиль, который технолог уже правил
--     руками». Повторный прогон обязан ничего не делать, а не восстанавливать старое число.
SET @ddl := IF(@has_pressing = 1,
    'INSERT INTO tech_card_equipment_profile
        (tech_card_id, profile_key, kind, equipment, thread_count)
     SELECT c.tech_card_id, CONCAT(''LEGACYOVERLOCK'', LPAD(c.tech_card_id, 12, ''0'')),
            ''machine'', ''overlock'', c.overlock_thread_count
     FROM tech_card_construction c
     WHERE c.overlock_thread_count IS NOT NULL
       AND NOT EXISTS (SELECT 1 FROM tech_card_equipment_profile p
         WHERE p.tech_card_id = c.tech_card_id
           AND p.profile_key = CONCAT(''LEGACYOVERLOCK'', LPAD(c.tech_card_id, 12, ''0'')))',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- 8b. Проза pressing переезжает в notes ПОД ДЕТЕРМИНИРОВАННЫМ ТЕГОМ. Гейт идемпотентности — сам
--     тег через NOT LIKE, а НЕ существование колонки: колонка жива всё время, пока идёт 8a-8b, и
--     повторный прогон файла, упавшего между 8b и 8c, дописал бы текст второй раз. Тег заодно
--     объясняет читателю карточки, откуда взялся абзац.
SET @ddl := IF(@has_pressing = 1,
    'UPDATE tech_card_construction
     SET notes = CONCAT_WS(''\n\n'', NULLIF(notes, ''''), CONCAT(''ВТО (перенос 0306): '', pressing))
     WHERE pressing IS NOT NULL AND pressing <> ''''
       AND (notes IS NULL OR notes NOT LIKE ''%ВТО (перенос 0306):%'')',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- ПУСТЫЕ СТРОКИ, А НЕ ГОЛЫЕ «--», В ЭТОМ БЛОКЕ. Парсер sql-migrate пропускает комментарий
-- только когда строка начинается с «-- » (с пробелом); голая «--» попадает в буфер оператора,
-- и до сих пор это сходило с рук лишь потому, что следующий оператор буфер сбрасывал. Здесь
-- оператора после блока НЕТ — он и был снят, — поэтому буфер доживал до «-- +migrate Down» и
-- ронял разбор ВСЕГО файла на старте: миграция применена давно, но парсятся все файлы каждый раз.
-- 8c. КОЛОНКИ НЕ СНОСЯТСЯ ЗДЕСЬ — СНЯТИЕ ОТЛОЖЕНО ОТДЕЛЬНОЙ МИГРАЦИЕЙ ПОСЛЕ СОАКА.

--     Снос был единственным разрушительным оператором во всём выкате, и он закрывал дорогу назад:
--     предыдущий бинарь ВСТАВЛЯЕТ обе колонки (`insertTechCardConstruction`), поэтому откат кнопкой
--     DO после такого деплоя ломал бы сохранение любой тех-карты — то есть отката бы не было вовсе.
--     Данные при этом уже перенесены шагами 8a и 8b, и колонки стоят пустыми и никем не читаемыми:
--     новый стор их не пишет, новый контракт их не знает. Цена ожидания — две мёртвые колонки,
--     цена спешки — невозможность вернуться.

--     Снос делает отдельная миграция, добавляемая ПОСЛЕ того, как прод отработает на новом бинаре;
--     её гейт — существование колонки, поэтому на бете (где 0306 уже применился в прежней редакции
--     и колонок нет) она выродится в no-op.

-- 9. СВИДЕТЕЛЬСТВО О МАСШТАБЕ. На бете перекладка шага 5 затрагивает 5 строк операций (4 lockstitch
--    и 1 unknown, который не двигается) и 2 строки construction. Прод не замерялся, и это записано
--    здесь именно потому, что замер не отменяет требования: каждый шаг выше обязан быть корректен
--    при ЛЮБЫХ данных, а не при тех, что видели на бете.

-- +migrate Down
--
-- Возврат схемы, не данных — см. шапку. Порядок обратный Up, гейты симметричные.

-- D1. Новый словарь типов операции. Легаси-CHECK 0076 НЕ воссоздаётся: в колонке уже лежат
--     'machine' / 'press', и ретроактивная узкая проверка уронила бы старт на первой же строке.
--     Повторный Up это переживает — его шаг 4 не найдёт ничего и пропустит, шаг 6 добавит заново.
SET @op_type_chk := (SELECT COUNT(*) FROM information_schema.TABLE_CONSTRAINTS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_operation'
      AND CONSTRAINT_NAME = 'chk_op_operation_type');
SET @ddl := IF(@op_type_chk = 1,
    'ALTER TABLE tech_card_operation DROP CHECK chk_op_operation_type', 'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- D2. Пятнадцать колонок и одиннадцать их проверок — одним ALTER. chk_op_attachment_kind ОСТАЁТСЯ
--     расширенным: сужение обратно проверило бы всю таблицу и упало на строках с 'none'.
SET @op_back := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_operation'
      AND COLUMN_NAME = 'machine_type');
SET @ddl := IF(@op_back = 1,
    'ALTER TABLE tech_card_operation
        DROP CHECK chk_op_press_pressure,
        DROP CHECK chk_op_press_dwell,
        DROP CHECK chk_op_press_temp,
        DROP CHECK chk_op_stitch_width,
        DROP CHECK chk_op_needle_size,
        DROP CHECK chk_op_thread_count,
        DROP CHECK chk_op_press_cloth,
        DROP CHECK chk_op_thread_tension,
        DROP CHECK chk_op_needle_type,
        DROP CHECK chk_op_press_equipment,
        DROP CHECK chk_op_machine_type,
        DROP COLUMN press_cloth,
        DROP COLUMN press_steam,
        DROP COLUMN press_pressure_n_cm2,
        DROP COLUMN press_dwell_sec,
        DROP COLUMN press_temperature_c,
        DROP COLUMN press_profile_key,
        DROP COLUMN press_equipment,
        DROP COLUMN stitch_width_mm,
        DROP COLUMN thread_tension_note,
        DROP COLUMN thread_tension,
        DROP COLUMN needle_size_nm,
        DROP COLUMN needle_type,
        DROP COLUMN thread_count,
        DROP COLUMN machine_profile_key,
        DROP COLUMN machine_type',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- D3. Две колонки блока дефолтов возвращаются ПУСТЫМИ: их содержимое уехало в notes и в профили и
--     назад не разливается. Тег «ВТО (перенос 0306):» в notes остаётся — стирать его значило бы
--     резать пользовательский текст по шаблону.
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

-- D4. Профили целиком.
DROP TABLE IF EXISTS tech_card_equipment_profile;
