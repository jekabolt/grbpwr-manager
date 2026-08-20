-- +migrate Up

-- ВИДЫ ОПЕРАЦИЙ: ШАГ ПЕРЕСТАЁТ БЫТЬ ТОЛЬКО ШВОМ. До этой волны шаг тех-карты умел отвечать
-- четырьмя глаголами (machine / press / press_open / fusing / handwork / other), и всё, что цех
-- делает каждый день сверх шитья — поставить кнопку, напечатать, подрезать припуск, счистить
-- концы ниток, проверить, сложить, упаковать, отдать в мокрую обработку — писалось как OTHER плюс
-- проза в note. Проза не попадает ни в подпись, ни в цех: её нельзя ни проверить, ни отпечатать.
-- Девять новых глаголов приходят вместе со СВОИМИ настройками — 32 колонки ниже.
--
-- ВСЕ 32 КОЛОНКИ NULLABLE, И NULL ЗНАЧИТ «НЕ УКАЗАНО» — не ноль и не «нет» (приём 0289/0306).
-- Явное «нет» там, где оно есть, — отдельный токен none (seam_securing, hole_prep, reinforcement,
-- peel_mode). Именно поэтому ни одна из колонок не имеет DEFAULT: дефолт стёр бы разницу между
-- «технолог сказал none» и «технолог про это молчит».
--
-- ВСЕ ЧИСЛОВЫЕ ГРАНИЦЫ — САНИТИ, НЕ СТАНДАРТ (довод 0306). 1..12 игл, 100..750 °C, 8..64 стежка
-- цикла, 4..120 мм прорезки существуют, чтобы поймать опечатку до цеха, а не чтобы объявить, на
-- что машина имеет право быть настроена. Сужать их под паспорт конкретного вендора не надо.
-- Каждая граница продублирована Go-проверкой с читаемым сообщением: CHECK в одиночку отвечает
-- ошибкой 3819, где нет ни поля, ни слов.
--
-- КАЖДЫЙ CHECK ОДНОКОЛОНОЧНЫЙ И НАЧИНАЕТСЯ С `IS NULL OR`. Двухколоночных правил в схеме нет
-- намеренно: `needle_gauge_mm ⇒ needle_count >= 2`, `pitch_mm ⇒ placement_count >= 2`,
-- `foldback_mm ⇒ attach_method = threaded`, `air_temperature_c ⇒ ЯВНЫЙ seam_taping`, а также
-- отказ печатного блока при laser_engrave живут в Go (internal/dto), где у отказа есть путь поля
-- и слова. 3819 назвал бы колонку, которой оператор не касался.
--
-- ПОРЯДОК КОЛОНОК В ALTER — КАНОН. Тот же порядок обязан стоять в INSERT-списке и named-map стора,
-- в SELECT операций и в полях struct entity.TechCardOperation. Канон задаёт таблица §1 плана
-- (Ф2-ядро S→PL→H→P→W→T→F→C→Q→WP, затем дельта FA→S14→S17). Четыре параллельных исполнителя —
-- четыре шанса разъехаться; расхождение порядка ищется потом неделю.
--
-- TOPSTITCH_MODE РАСШИРЯЕТСЯ ФИЗИЧЕСКИ, И ЭТО ШАГ 1, А НЕ ПРИМЕЧАНИЕ. 0289 создал колонку
-- VARCHAR(8) под пару edge|width. Новый токен parallel_to_seam — РОВНО 16 символов. Без MODIFY
-- первое же сохранение даёт data-too-long при STRICT, а без STRICT — тихую обрезку до 'parallel_',
-- после которой строку завалит уже сам словарный CHECK. Поэтому MODIFY идёт СТРОГО ДО
-- пересоздания chk_op_topstitch_mode (шаг 6).
--
-- СЕМЬ СУЩЕСТВУЮЩИХ СЛОВАРНЫХ CHECK'ОВ ПЕРЕСОЗДАЮТСЯ СТРОГО НАДМНОЖЕСТВОМ. ADD CONSTRAINT CHECK
-- проверяет ВСЮ существующую историю таблицы, и сужение словаря останавливает старт прода на
-- первой же строке со снятым токеном (прецедент 0292, память retroactive-check-halts-deploy).
-- Ни один токен ни из одного словаря здесь не снимается — только добавляется. chk_bom_item_kind
-- пересоздаётся РОВНО ОДИН РАЗ, объединённым списком обеих фаз: два последовательных пересоздания
-- одного CHECK'а в одном файле — ловушка гейтов, второй прогон стал бы no-op с недостроенным
-- словарём. По той же причине ЯКОРЬ каждого гейта — ПОСЛЕДНИЙ добавляемый токен, а не первый.
--
-- НЕ ТРОГАЕМ ВОВСЕ: chk_op_press_equipment (conveyor_dryer отложен), chk_eqp_attachment,
-- chk_op_attachment_kind, chk_eqp_press_op_type. Словарь оснастки (фаза Ф1) в эту волну не входит:
-- лишний токен в chk_eqp_equipment провалил бы TestEquipmentUnionDBCheckNoDrift, который требует
-- ТОЧНОГО равенства словаря объединению MachineTypeTokens и PressEquipmentTokens. Если Ф1 уедет
-- раньше этого файла — её токены обязаны войти в ADD шага 7, иначе ADD окажется сужением.
--
-- НОВЫЕ CHECK'И ВЕШАЮТСЯ НА СВЕЖИЕ NULL-КОЛОНКИ, поэтому их ретроактивная проверка проходит
-- тривиально при любых данных. Проверено отдельно и то, чего это не касается: MODIFY шага 1 и
-- пересоздание chk_op_topstitch_mode перечитывают РЕАЛЬНЫЕ строки эпохи VARCHAR(8) ('edge',
-- 'width') — оба словаря надмножества, обе строки проходят.
--
-- НУЛЕВАЯ ВОЛНА ПРОТУХШИХ ПОДПИСЕЙ. Все 32 колонки рождаются NULL на каждой существующей строке,
-- поэтому ни один из 11 новых хвостов дайджеста не эмитится и байты кортежа существующего шага не
-- меняются. Новые токены operation_type / machine_type / topstitch_mode старым кодом записаны
-- быть не могли — их отвергал CHECK.
--
-- ИДЕМПОТЕНТНОСТЬ: MySQL 8 автокоммитит DDL, поэтому падение в середине оставляет схему
-- полуприменённой без строки в gorp_migrations, и следующая загрузка перезапускает файл с начала.
-- Каждый шаг — под собственным гейтом information_schema, PREPARE / EXECUTE / DEALLOCATE ПО ОДНОМУ
-- оператору на строку (иначе multiStatements=true в контейнерных тестах маскирует поломку, которая
-- на проде валит старт). Файл в gorp_migrations значится ПОЛНЫМ ИМЕНЕМ — после первого прогона не
-- переименовывать (урок sql-migrate renumber orphan).

-- 1. TOPSTITCH_MODE VARCHAR(8) -> VARCHAR(16). ПЕРВЫМ ШАГОМ ФАЙЛА и строго до шага 6.
--    Гейт — фактическая ширина колонки, а не наличие миграции: CHARACTER_MAXIMUM_LENGTH < 16
--    истинно ровно один раз. Позиция колонки (AFTER seam_allowance_mm из 0289) сохраняется —
--    MODIFY без указания места её не двигает. Существующие 'edge' / 'width' переживают
--    перечитывание живого chk_op_topstitch_mode: расширение VARCHAR словарь не сужает.
SET @ts_narrow := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_operation'
      AND COLUMN_NAME = 'topstitch_mode' AND CHARACTER_MAXIMUM_LENGTH < 16);
SET @ddl := IF(@ts_narrow = 1,
    'ALTER TABLE tech_card_operation
        MODIFY COLUMN topstitch_mode VARCHAR(16) NULL COMMENT ''edge = в край, width = на заданной ширине, in_ditch = в шов, parallel_to_seam = параллельно шву; NULL = отстрочки нет''',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- 2. ТРИДЦАТЬ ДВЕ КОЛОНКИ И ТРИДЦАТЬ ДВА ИХ CHECK'А — ОДНИМ ALTER. Гейт — колонка print_method:
--    она добавляется этим же ALTER'ом, значит её наличие и есть «шаг 2 уже прошёл». Порядок
--    колонок — канон §1, см. шапку. Семнадцать словарных CHECK'ов написаны формой
--    REGEXP + STRCMP-гейт регистра (0289/0306): коллация колонки регистронезависима и на
--    utf8mb3_general_ci прода, и на utf8mb4_0900_ai_ci контейнера, поэтому без STRCMP 'SEW' лежал
--    бы в базе законно и разъехался бы с токеном enum'а.
SET @op_kinds := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_operation'
      AND COLUMN_NAME = 'print_method');
SET @ddl := IF(@op_kinds = 0,
    'ALTER TABLE tech_card_operation
        ADD COLUMN needle_count TINYINT NULL COMMENT ''S1: игл в шаге, шт; MACHINE любого machine_type; NULL = не указано'',
        ADD COLUMN needle_gauge_mm DECIMAL(4,1) NULL COMMENT ''S2: расстановка игл, мм; законна только при needle_count >= 2 (правило в Go); NULL = не указано'',
        ADD COLUMN seam_securing VARCHAR(16) NULL COMMENT ''S3: закрепка шва; none = ЯВНО без закрепки, NULL = не указано'',
        ADD COLUMN row_spacing_mm DECIMAL(4,1) NULL COMMENT ''S4: расстояние между строчками, мм; NULL = не указано'',
        ADD COLUMN fullness_ratio DECIMAL(4,2) NULL COMMENT ''S5: коэффициент посадки/сборки; NULL = посадки и сборки нет'',
        ADD COLUMN placement_count TINYINT NULL COMMENT ''PL1: сколько раз повторяется шаг на изделии, шт; MACHINE | HARDWARE_SET | PRINT; NULL = один повтор'',
        ADD COLUMN pitch_mm DECIMAL(5,1) NULL COMMENT ''PL2: шаг раскладки повторов, мм; законен только при placement_count >= 2 (правило в Go); NULL = не указано'',
        ADD COLUMN attach_method VARCHAR(16) NULL COMMENT ''H1: способ установки фурнитуры; REQUIRED у HARDWARE_SET (обязательность объявляет глагол, не флаг)'',
        ADD COLUMN hole_prep VARCHAR(16) NULL COMMENT ''H2: подготовка отверстия; HARDWARE_SET и MACHINE+buttonhole|button_attach|bartack; none = ЯВНО без отверстия'',
        ADD COLUMN reinforcement VARCHAR(16) NULL COMMENT ''H3: усиление места установки; те же два глагола, что у hole_prep; none = ЯВНО без усиления'',
        ADD COLUMN foldback_mm DECIMAL(4,1) NULL COMMENT ''H6: подгиб под резьбовую фурнитуру, мм; законен только при attach_method = threaded (правило в Go)'',
        ADD COLUMN cycle_stitch_count SMALLINT NULL COMMENT ''H7: стежков в цикле автомата, шт; NULL = штатная программа машины, НЕ ноль'',
        ADD COLUMN print_method VARCHAR(16) NULL COMMENT ''P1: метод печати/нанесения; REQUIRED у PRINT. Гейт-колонка идемпотентности этой миграции'',
        ADD COLUMN peel_mode VARCHAR(8) NULL COMMENT ''P2: съём носителя; отвергается при print_method = laser_engrave; none = носителя нет, NULL = не указано'',
        ADD COLUMN second_press_sec TINYINT NULL COMMENT ''P3: второй прижим, с; отвергается при laser_engrave; NULL = второго прижима нет'',
        ADD COLUMN pressure_scale VARCHAR(8) NULL COMMENT ''P6: давление прижима, упорядоченная шкала; отвергается при laser_engrave; NULL = не указано'',
        ADD COLUMN air_temperature_c SMALLINT NULL COMMENT ''W1: температура горячего воздуха, °C; ТОЛЬКО MACHINE + ЯВНЫЙ machine_type = seam_taping'',
        ADD COLUMN feed_speed_m_min DECIMAL(3,1) NULL COMMENT ''W2: скорость подачи, м/мин; MACHINE + ЯВНЫЙ seam_taping | ultrasonic_welder'',
        ADD COLUMN trim_action VARCHAR(16) NULL COMMENT ''T1: что именно делает подрезка; REQUIRED у TRIM'',
        ADD COLUMN residual_allowance_mm DECIMAL(3,1) NULL COMMENT ''T2: припуск ПОСЛЕ подрезки, мм; это НЕ seam_allowance_mm шва; NULL = не указано'',
        ADD COLUMN residual_tail_max_mm DECIMAL(3,1) NULL COMMENT ''F3: допустимый остаток конца нитки, мм; NULL = стандарт цеха'',
        ADD COLUMN cleaning_kind VARCHAR(20) NULL COMMENT ''C1: что чистим; REQUIRED у CLEAN'',
        ADD COLUMN coverage_mode VARCHAR(20) NULL COMMENT ''Q1: охват контроля; REQUIRED у INSPECT'',
        ADD COLUMN wet_process_kind VARCHAR(16) NULL COMMENT ''WP1: вид мокрой обработки; REQUIRED у WET_PROCESS'',
        ADD COLUMN buttonhole_style VARCHAR(16) NULL COMMENT ''FA1: форма петли; MACHINE + ЯВНЫЙ machine_type = buttonhole'',
        ADD COLUMN cut_length_mm DECIMAL(4,1) NULL COMMENT ''FA2: длина прорезки петли, мм; MACHINE + ЯВНЫЙ buttonhole'',
        ADD COLUMN buttonhole_orientation VARCHAR(12) NULL COMMENT ''FA5: ориентация петли; MACHINE + ЯВНЫЙ buttonhole; точка — выноской, не здесь'',
        ADD COLUMN bartack_length_mm DECIMAL(4,1) NULL COMMENT ''FA7: длина закрепки, мм; MACHINE + ЯВНЫЙ buttonhole | bartack; границы — объединение двух санити'',
        ADD COLUMN attach_pattern VARCHAR(20) NULL COMMENT ''FA9: рисунок пришива пуговицы; MACHINE + ЯВНЫЙ button_attach'',
        ADD COLUMN zipper_application VARCHAR(16) NULL COMMENT ''FA13: способ втачивания молнии; MACHINE + ЯВНЫЙ zipper_setting'',
        ADD COLUMN binding_style VARCHAR(12) NULL COMMENT ''S14: тип окантовки; MACHINE + ЯВНЫЙ binding_taping'',
        ADD COLUMN label_attach_stitch VARCHAR(24) NULL COMMENT ''S17: как пристрочена этикетка; MACHINE любого machine_type'',
        ADD CONSTRAINT chk_op_needle_count CHECK (needle_count IS NULL OR (needle_count >= 1 AND needle_count <= 12)),
        ADD CONSTRAINT chk_op_needle_gauge CHECK (needle_gauge_mm IS NULL OR (needle_gauge_mm >= 1.6 AND needle_gauge_mm <= 25.4)),
        ADD CONSTRAINT chk_op_seam_securing CHECK (seam_securing IS NULL OR (seam_securing REGEXP ''^(none|backtack|condensed|latched)$'' AND STRCMP(CAST(seam_securing AS BINARY), CAST(LOWER(seam_securing) AS BINARY)) = 0)),
        ADD CONSTRAINT chk_op_row_spacing CHECK (row_spacing_mm IS NULL OR (row_spacing_mm >= 1 AND row_spacing_mm <= 30)),
        ADD CONSTRAINT chk_op_fullness_ratio CHECK (fullness_ratio IS NULL OR (fullness_ratio >= 0.6 AND fullness_ratio <= 4.0)),
        ADD CONSTRAINT chk_op_placement_count CHECK (placement_count IS NULL OR (placement_count >= 1 AND placement_count <= 99)),
        ADD CONSTRAINT chk_op_pitch CHECK (pitch_mm IS NULL OR (pitch_mm >= 5 AND pitch_mm <= 500)),
        ADD CONSTRAINT chk_op_attach_method CHECK (attach_method IS NULL OR (attach_method REGEXP ''^(sew|prong_clinch|press_set|crimp|threaded)$'' AND STRCMP(CAST(attach_method AS BINARY), CAST(LOWER(attach_method) AS BINARY)) = 0)),
        ADD CONSTRAINT chk_op_hole_prep CHECK (hole_prep IS NULL OR (hole_prep REGEXP ''^(none|prong_pierce|awl_pierce|punch)$'' AND STRCMP(CAST(hole_prep AS BINARY), CAST(LOWER(hole_prep) AS BINARY)) = 0)),
        ADD CONSTRAINT chk_op_reinforcement CHECK (reinforcement IS NULL OR (reinforcement REGEXP ''^(none|fusible_patch|fabric_stay|tape|seam_catch|other)$'' AND STRCMP(CAST(reinforcement AS BINARY), CAST(LOWER(reinforcement) AS BINARY)) = 0)),
        ADD CONSTRAINT chk_op_foldback CHECK (foldback_mm IS NULL OR (foldback_mm >= 10 AND foldback_mm <= 80)),
        ADD CONSTRAINT chk_op_cycle_stitch_count CHECK (cycle_stitch_count IS NULL OR (cycle_stitch_count >= 8 AND cycle_stitch_count <= 64)),
        ADD CONSTRAINT chk_op_print_method CHECK (print_method IS NULL OR (print_method REGEXP ''^(screen|dtf|heat_transfer|foil|laser_engrave)$'' AND STRCMP(CAST(print_method AS BINARY), CAST(LOWER(print_method) AS BINARY)) = 0)),
        ADD CONSTRAINT chk_op_peel_mode CHECK (peel_mode IS NULL OR (peel_mode REGEXP ''^(none|hot|warm|cold)$'' AND STRCMP(CAST(peel_mode AS BINARY), CAST(LOWER(peel_mode) AS BINARY)) = 0)),
        ADD CONSTRAINT chk_op_second_press CHECK (second_press_sec IS NULL OR (second_press_sec >= 1 AND second_press_sec <= 30)),
        ADD CONSTRAINT chk_op_pressure_scale CHECK (pressure_scale IS NULL OR (pressure_scale REGEXP ''^(light|medium|firm)$'' AND STRCMP(CAST(pressure_scale AS BINARY), CAST(LOWER(pressure_scale) AS BINARY)) = 0)),
        ADD CONSTRAINT chk_op_air_temp CHECK (air_temperature_c IS NULL OR (air_temperature_c >= 100 AND air_temperature_c <= 750)),
        ADD CONSTRAINT chk_op_feed_speed CHECK (feed_speed_m_min IS NULL OR (feed_speed_m_min >= 0.3 AND feed_speed_m_min <= 10.0)),
        ADD CONSTRAINT chk_op_trim_action CHECK (trim_action IS NULL OR (trim_action REGEXP ''^(trim_even|grade_layers|clip_concave|notch_convex|corner_diagonal|turn_and_shape)$'' AND STRCMP(CAST(trim_action AS BINARY), CAST(LOWER(trim_action) AS BINARY)) = 0)),
        ADD CONSTRAINT chk_op_residual_allowance CHECK (residual_allowance_mm IS NULL OR (residual_allowance_mm >= 1 AND residual_allowance_mm <= 10)),
        ADD CONSTRAINT chk_op_residual_tail CHECK (residual_tail_max_mm IS NULL OR (residual_tail_max_mm >= 1 AND residual_tail_max_mm <= 10)),
        ADD CONSTRAINT chk_op_cleaning_kind CHECK (cleaning_kind IS NULL OR (cleaning_kind REGEXP ''^(spot_clean|dust_lint|chalk_removal|adhesive_removal)$'' AND STRCMP(CAST(cleaning_kind AS BINARY), CAST(LOWER(cleaning_kind) AS BINARY)) = 0)),
        ADD CONSTRAINT chk_op_coverage_mode CHECK (coverage_mode IS NULL OR (coverage_mode REGEXP ''^(each_unit|sample_per_bundle|aql_plan|first_output)$'' AND STRCMP(CAST(coverage_mode AS BINARY), CAST(LOWER(coverage_mode) AS BINARY)) = 0)),
        ADD CONSTRAINT chk_op_wet_process_kind CHECK (wet_process_kind IS NULL OR (wet_process_kind REGEXP ''^(rinse|enzyme|garment_dye|softener)$'' AND STRCMP(CAST(wet_process_kind AS BINARY), CAST(LOWER(wet_process_kind) AS BINARY)) = 0)),
        ADD CONSTRAINT chk_op_buttonhole_style CHECK (buttonhole_style IS NULL OR (buttonhole_style REGEXP ''^(straight|eyelet|round_end|other)$'' AND STRCMP(CAST(buttonhole_style AS BINARY), CAST(LOWER(buttonhole_style) AS BINARY)) = 0)),
        ADD CONSTRAINT chk_op_cut_length CHECK (cut_length_mm IS NULL OR (cut_length_mm >= 4 AND cut_length_mm <= 120)),
        ADD CONSTRAINT chk_op_buttonhole_orientation CHECK (buttonhole_orientation IS NULL OR (buttonhole_orientation REGEXP ''^(horizontal|vertical|angled)$'' AND STRCMP(CAST(buttonhole_orientation AS BINARY), CAST(LOWER(buttonhole_orientation) AS BINARY)) = 0)),
        ADD CONSTRAINT chk_op_bartack_length CHECK (bartack_length_mm IS NULL OR (bartack_length_mm >= 1 AND bartack_length_mm <= 40)),
        ADD CONSTRAINT chk_op_attach_pattern CHECK (attach_pattern IS NULL OR (attach_pattern REGEXP ''^(cross_x|parallel|square|u_shape|other)$'' AND STRCMP(CAST(attach_pattern AS BINARY), CAST(LOWER(attach_pattern) AS BINARY)) = 0)),
        ADD CONSTRAINT chk_op_zipper_application CHECK (zipper_application IS NULL OR (zipper_application REGEXP ''^(centered|lapped|invisible|exposed|fly|separating_cf|in_seam_pocket|other)$'' AND STRCMP(CAST(zipper_application AS BINARY), CAST(LOWER(zipper_application) AS BINARY)) = 0)),
        ADD CONSTRAINT chk_op_binding_style CHECK (binding_style IS NULL OR (binding_style REGEXP ''^(raw|single_fold|double_fold)$'' AND STRCMP(CAST(binding_style AS BINARY), CAST(LOWER(binding_style) AS BINARY)) = 0)),
        ADD CONSTRAINT chk_op_label_attach_stitch CHECK (label_attach_stitch IS NULL OR (label_attach_stitch REGEXP ''^(four_sides|two_sides_top_bottom|two_sides_left_right|one_edge|caught_in_seam|corners_tack|other)$'' AND STRCMP(CAST(label_attach_stitch AS BINARY), CAST(LOWER(label_attach_stitch) AS BINARY)) = 0))',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- 3. Словарь глаголов шага: +9 (hardware_set, print, trim, thread_trim, clean, inspect, fold,
--    pack, wet_process). У этого CHECK'а НЕТ `IS NULL OR` — operation_type объявлен NOT NULL, и
--    живая проверка 0306 написана без него. Форма сохранена дословно: дописать `IS NULL OR`
--    значило бы поменять смысл проверки заодно со словарём.
--    FOLD и PACK не несут ни одной колонки шага — глагол сам по себе есть факт.
SET @chk_op_type := (SELECT COUNT(*) FROM information_schema.TABLE_CONSTRAINTS tc
    JOIN information_schema.CHECK_CONSTRAINTS cc
      ON cc.CONSTRAINT_SCHEMA = tc.CONSTRAINT_SCHEMA AND cc.CONSTRAINT_NAME = tc.CONSTRAINT_NAME
    WHERE tc.TABLE_SCHEMA = DATABASE() AND tc.TABLE_NAME = 'tech_card_operation'
      AND tc.CONSTRAINT_NAME = 'chk_op_operation_type' AND cc.CHECK_CLAUSE NOT LIKE '%wet_process%');
SET @ddl := IF(@chk_op_type = 1,
    'ALTER TABLE tech_card_operation
        DROP CHECK chk_op_operation_type,
        ADD CONSTRAINT chk_op_operation_type CHECK (operation_type REGEXP ''^(unknown|machine|press|press_open|fusing|handwork|other|hardware_set|print|trim|thread_trim|clean|inspect|fold|pack|wet_process)$'' AND STRCMP(CAST(operation_type AS BINARY), CAST(LOWER(operation_type) AS BINARY)) = 0)',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- 4. Словарь машинок шага: +2 (seam_taping, ultrasonic_welder). Легаси-двойника у этих двух
--    нет, поэтому в дайджесте они идут обычным machine-хвостом, как coverlock сегодня.
SET @chk_op_machine := (SELECT COUNT(*) FROM information_schema.TABLE_CONSTRAINTS tc
    JOIN information_schema.CHECK_CONSTRAINTS cc
      ON cc.CONSTRAINT_SCHEMA = tc.CONSTRAINT_SCHEMA AND cc.CONSTRAINT_NAME = tc.CONSTRAINT_NAME
    WHERE tc.TABLE_SCHEMA = DATABASE() AND tc.TABLE_NAME = 'tech_card_operation'
      AND tc.CONSTRAINT_NAME = 'chk_op_machine_type' AND cc.CHECK_CLAUSE NOT LIKE '%ultrasonic_welder%');
SET @ddl := IF(@chk_op_machine = 1,
    'ALTER TABLE tech_card_operation
        DROP CHECK chk_op_machine_type,
        ADD CONSTRAINT chk_op_machine_type CHECK (machine_type IS NULL OR (machine_type REGEXP ''^(lockstitch|lockstitch_double_needle|overlock|coverstitch|coverlock|chainstitch|blindstitch|zigzag|bartack|buttonhole|button_attach|embroidery|handstitch_imitation|hardware_attach|elastic_attach|binding_taping|zipper_setting|gathering|patch_pocket_auto|welt_pocket_auto|template_auto|collar_cuff_auto|sleeve_setting_auto|waistband_auto|other|seam_taping|ultrasonic_welder)$'' AND STRCMP(CAST(machine_type AS BINARY), CAST(LOWER(machine_type) AS BINARY)) = 0))',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- 5. Проутюжильник шага: +1 (silicone_paper). Термотрансферу нужен именно он, а не ткань.
SET @chk_op_cloth := (SELECT COUNT(*) FROM information_schema.TABLE_CONSTRAINTS tc
    JOIN information_schema.CHECK_CONSTRAINTS cc
      ON cc.CONSTRAINT_SCHEMA = tc.CONSTRAINT_SCHEMA AND cc.CONSTRAINT_NAME = tc.CONSTRAINT_NAME
    WHERE tc.TABLE_SCHEMA = DATABASE() AND tc.TABLE_NAME = 'tech_card_operation'
      AND tc.CONSTRAINT_NAME = 'chk_op_press_cloth' AND cc.CHECK_CLAUSE NOT LIKE '%silicone_paper%');
SET @ddl := IF(@chk_op_cloth = 1,
    'ALTER TABLE tech_card_operation
        DROP CHECK chk_op_press_cloth,
        ADD CONSTRAINT chk_op_press_cloth CHECK (press_cloth IS NULL OR (press_cloth REGEXP ''^(none|press_cloth|damp_press_cloth|teflon_sheet|other|silicone_paper)$'' AND STRCMP(CAST(press_cloth AS BINARY), CAST(LOWER(press_cloth) AS BINARY)) = 0))',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- 6. Режим отстрочки: +2 (in_ditch, parallel_to_seam). СТРОГО ПОСЛЕ шага 1 — до расширения
--    колонки этот словарь описывал бы значение, которое в неё физически не влезает.
--    in_ditch ширины не имеет (отвергает width_mm), parallel_to_seam её ТРЕБУЕТ, и там она значит
--    отступ ОТ ШВА, а не от края. Оба правила — в parseTopstitch, не здесь.
SET @chk_op_topstitch := (SELECT COUNT(*) FROM information_schema.TABLE_CONSTRAINTS tc
    JOIN information_schema.CHECK_CONSTRAINTS cc
      ON cc.CONSTRAINT_SCHEMA = tc.CONSTRAINT_SCHEMA AND cc.CONSTRAINT_NAME = tc.CONSTRAINT_NAME
    WHERE tc.TABLE_SCHEMA = DATABASE() AND tc.TABLE_NAME = 'tech_card_operation'
      AND tc.CONSTRAINT_NAME = 'chk_op_topstitch_mode' AND cc.CHECK_CLAUSE NOT LIKE '%parallel_to_seam%');
SET @ddl := IF(@chk_op_topstitch = 1,
    'ALTER TABLE tech_card_operation
        DROP CHECK chk_op_topstitch_mode,
        ADD CONSTRAINT chk_op_topstitch_mode CHECK (topstitch_mode IS NULL OR (topstitch_mode REGEXP ''^(edge|width|in_ditch|parallel_to_seam)$'' AND STRCMP(CAST(topstitch_mode AS BINARY), CAST(LOWER(topstitch_mode) AS BINARY)) = 0))',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- 7. Парк оборудования карточки: +2 машинных токена, ровно те же, что в шаге 4. Словарь
--    профиля — объединение машинных и утюжильных токенов с ЕДИНСТВЕННЫМ 'other' (после steamer);
--    TestEquipmentUnionDBCheckNoDrift проверяет это равенством множеств и падает на дубле 'other'.
--    Было 30 токенов, стало 32. `IS NULL OR` здесь нет — equipment объявлен NOT NULL в 0306.
SET @chk_eqp_equip := (SELECT COUNT(*) FROM information_schema.TABLE_CONSTRAINTS tc
    JOIN information_schema.CHECK_CONSTRAINTS cc
      ON cc.CONSTRAINT_SCHEMA = tc.CONSTRAINT_SCHEMA AND cc.CONSTRAINT_NAME = tc.CONSTRAINT_NAME
    WHERE tc.TABLE_SCHEMA = DATABASE() AND tc.TABLE_NAME = 'tech_card_equipment_profile'
      AND tc.CONSTRAINT_NAME = 'chk_eqp_equipment' AND cc.CHECK_CLAUSE NOT LIKE '%ultrasonic_welder%');
SET @ddl := IF(@chk_eqp_equip = 1,
    'ALTER TABLE tech_card_equipment_profile
        DROP CHECK chk_eqp_equipment,
        ADD CONSTRAINT chk_eqp_equipment CHECK (equipment REGEXP ''^(lockstitch|lockstitch_double_needle|overlock|coverstitch|coverlock|chainstitch|blindstitch|zigzag|bartack|buttonhole|button_attach|embroidery|handstitch_imitation|hardware_attach|elastic_attach|binding_taping|zipper_setting|gathering|patch_pocket_auto|welt_pocket_auto|template_auto|collar_cuff_auto|sleeve_setting_auto|waistband_auto|seam_taping|ultrasonic_welder|iron|press|fusing_press|steam_dummy|steamer|other)$'' AND STRCMP(CAST(equipment AS BINARY), CAST(LOWER(equipment) AS BINARY)) = 0)',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- 8. Проутюжильник профиля: +1 (silicone_paper), тем же списком, что в шаге 5. Два словаря
--    press_cloth — шага и профиля — обязаны совпадать: шаг наследует значение профиля.
SET @chk_eqp_cloth := (SELECT COUNT(*) FROM information_schema.TABLE_CONSTRAINTS tc
    JOIN information_schema.CHECK_CONSTRAINTS cc
      ON cc.CONSTRAINT_SCHEMA = tc.CONSTRAINT_SCHEMA AND cc.CONSTRAINT_NAME = tc.CONSTRAINT_NAME
    WHERE tc.TABLE_SCHEMA = DATABASE() AND tc.TABLE_NAME = 'tech_card_equipment_profile'
      AND tc.CONSTRAINT_NAME = 'chk_eqp_press_cloth' AND cc.CHECK_CLAUSE NOT LIKE '%silicone_paper%');
SET @ddl := IF(@chk_eqp_cloth = 1,
    'ALTER TABLE tech_card_equipment_profile
        DROP CHECK chk_eqp_press_cloth,
        ADD CONSTRAINT chk_eqp_press_cloth CHECK (press_cloth IS NULL OR (press_cloth REGEXP ''^(none|press_cloth|damp_press_cloth|teflon_sheet|other|silicone_paper)$'' AND STRCMP(CAST(press_cloth AS BINARY), CAST(LOWER(press_cloth) AS BINARY)) = 0))',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- 9. Виды позиций BOM: +2 (seam_sealing_tape для проклейки швов, embroidery_stabilizer для
--    вышивки). ОДИН блок с объединённым списком обеих фаз — см. шапку про ловушку гейтов.
--    Токен wet_chemical (фаза свойств) сюда НЕ добавляется: его enum ещё не выкачен, а лишний
--    токен в словаре БД без записи в bomKindHomeSection немедленно уронил бы TestBomKindDBCheckNoDrift.
--    Было 52 токена, стало 54; самый длинный — embroidery_stabilizer (21) при колонке VARCHAR(24).
SET @chk_bom_kind := (SELECT COUNT(*) FROM information_schema.TABLE_CONSTRAINTS tc
    JOIN information_schema.CHECK_CONSTRAINTS cc
      ON cc.CONSTRAINT_SCHEMA = tc.CONSTRAINT_SCHEMA AND cc.CONSTRAINT_NAME = tc.CONSTRAINT_NAME
    WHERE tc.TABLE_SCHEMA = DATABASE() AND tc.TABLE_NAME = 'tech_card_bom_item'
      AND tc.CONSTRAINT_NAME = 'chk_bom_item_kind' AND cc.CHECK_CLAUSE NOT LIKE '%embroidery_stabilizer%');
SET @ddl := IF(@chk_bom_kind = 1,
    'ALTER TABLE tech_card_bom_item
        DROP CHECK chk_bom_item_kind,
        ADD CONSTRAINT chk_bom_item_kind CHECK (kind IS NULL OR (kind REGEXP ''^(zipper|zipper_slider|button|snap|rivet|eyelet|hook_and_bar|snap_hook|buckle|strap_adjuster|ring|toggle|cord_stopper|cord_end|magnet|chain|elastic|drawcord|binding|tape|piping|webbing|hook_loop|boning|lace|ribbing|print|embroidery|applique|patch|heat_transfer|rhinestone|sequin|stud|foil|laser|sewing_thread|topstitch_thread|overlock_thread|buttonhole_thread|embroidery_thread|elastic_thread|polybag|carton|hanger|hangtag_string|sticker|tissue|dust_bag|garment_case|insert_card|other|seam_sealing_tape|embroidery_stabilizer)$'' AND STRCMP(CAST(kind AS BINARY), CAST(LOWER(kind) AS BINARY)) = 0))',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- 10. СВИДЕТЕЛЬСТВО О МАСШТАБЕ. Ни один шаг файла не переписывает данные — ни одного UPDATE'а
--     в файле нет вовсе. Шаг 2 — чистый ADD COLUMN на NULL-колонках, шаги 3..9 — DROP+ADD словарей строго
--     надмножеством, шаг 1 — расширение VARCHAR. Значит стоимость файла не зависит от объёма
--     таблиц и одинакова на бете и на проде: это перестройка метаданных, а не проход по строкам.

-- +migrate Down
--
-- Down снимает РОВНО ТО, ЧТО ДОБАВИЛ ШАГ 2 — 32 CHECK'а и 32 колонки. Всё остальное остаётся:
--
--   * СЕМЬ СЛОВАРНЫХ CHECK'ОВ (шаги 3..9) НЕ СУЖАЮТСЯ ОБРАТНО. Сужение — это ADD CONSTRAINT с
--     коротким списком, а он ретроактивно проверяет ВСЮ таблицу и падает на первой же строке с
--     'wet_process' / 'seam_taping' / 'in_ditch' / 'seam_sealing_tape'. К этому моменту колонки
--     волны уже дропнуты, и схема застревает полуоткатанной (прецедент 0306+0311).
--   * MODIFY ШАГА 1 НЕ ОТКАТЫВАЕТСЯ. VARCHAR(16) -> VARCHAR(8) — усечение данных, а строка со
--     значением 'parallel_to_seam' к моменту отката существовать УЖЕ МОГЛА.
--
-- Отсюда честный текст, который должен прочитать любой, кто держит палец над откатом:
-- ОТКАТ НА ДО-ВОЛНОВЫЙ БИНАРЬ ЗАКРЫТ. Down возвращает схему шага, но не возвращает словари, и
-- старый код, встретив шаг с operation_type = 'print', отвергнет карточку целиком. Down здесь —
-- инструмент разработки (up → down → up на одноразовой базе), а не машина времени для прода.

-- D1. Тридцать два CHECK'а и тридцать две колонки — одним ALTER, порядком, обратным шагу 2.
--     Гейт тот же print_method: пока колонка есть, откатывать есть что.
SET @op_kinds_back := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_operation'
      AND COLUMN_NAME = 'print_method');
SET @ddl := IF(@op_kinds_back = 1,
    'ALTER TABLE tech_card_operation
        DROP CHECK chk_op_label_attach_stitch,
        DROP CHECK chk_op_binding_style,
        DROP CHECK chk_op_zipper_application,
        DROP CHECK chk_op_attach_pattern,
        DROP CHECK chk_op_bartack_length,
        DROP CHECK chk_op_buttonhole_orientation,
        DROP CHECK chk_op_cut_length,
        DROP CHECK chk_op_buttonhole_style,
        DROP CHECK chk_op_wet_process_kind,
        DROP CHECK chk_op_coverage_mode,
        DROP CHECK chk_op_cleaning_kind,
        DROP CHECK chk_op_residual_tail,
        DROP CHECK chk_op_residual_allowance,
        DROP CHECK chk_op_trim_action,
        DROP CHECK chk_op_feed_speed,
        DROP CHECK chk_op_air_temp,
        DROP CHECK chk_op_pressure_scale,
        DROP CHECK chk_op_second_press,
        DROP CHECK chk_op_peel_mode,
        DROP CHECK chk_op_print_method,
        DROP CHECK chk_op_cycle_stitch_count,
        DROP CHECK chk_op_foldback,
        DROP CHECK chk_op_reinforcement,
        DROP CHECK chk_op_hole_prep,
        DROP CHECK chk_op_attach_method,
        DROP CHECK chk_op_pitch,
        DROP CHECK chk_op_placement_count,
        DROP CHECK chk_op_fullness_ratio,
        DROP CHECK chk_op_row_spacing,
        DROP CHECK chk_op_seam_securing,
        DROP CHECK chk_op_needle_gauge,
        DROP CHECK chk_op_needle_count,
        DROP COLUMN label_attach_stitch,
        DROP COLUMN binding_style,
        DROP COLUMN zipper_application,
        DROP COLUMN attach_pattern,
        DROP COLUMN bartack_length_mm,
        DROP COLUMN buttonhole_orientation,
        DROP COLUMN cut_length_mm,
        DROP COLUMN buttonhole_style,
        DROP COLUMN wet_process_kind,
        DROP COLUMN coverage_mode,
        DROP COLUMN cleaning_kind,
        DROP COLUMN residual_tail_max_mm,
        DROP COLUMN residual_allowance_mm,
        DROP COLUMN trim_action,
        DROP COLUMN feed_speed_m_min,
        DROP COLUMN air_temperature_c,
        DROP COLUMN pressure_scale,
        DROP COLUMN second_press_sec,
        DROP COLUMN peel_mode,
        DROP COLUMN print_method,
        DROP COLUMN cycle_stitch_count,
        DROP COLUMN foldback_mm,
        DROP COLUMN reinforcement,
        DROP COLUMN hole_prep,
        DROP COLUMN attach_method,
        DROP COLUMN pitch_mm,
        DROP COLUMN placement_count,
        DROP COLUMN fullness_ratio,
        DROP COLUMN row_spacing_mm,
        DROP COLUMN seam_securing,
        DROP COLUMN needle_gauge_mm,
        DROP COLUMN needle_count',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;
