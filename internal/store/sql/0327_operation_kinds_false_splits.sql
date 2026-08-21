-- +migrate Up

-- ДЕВЯТЬ ЛОЖНЫХ РАСЩЕПЛЕНИЙ В СЛОВАРЯХ ВОЛН 0324 И 0325, СНЯТЫЕ ОДНИМ ФАЙЛОМ.
--
-- Ложное расщепление — это два члена словаря, между которыми технолог выбирает не по содержанию
-- работы, а наугад: оба ответа истинны, и разница уезжает в подпись, в релизный снапшот и на
-- печатный лист молча. Эталон класса — `width`/`edge` отстрочки (0326). Здесь их девять сразу, все
-- на колонках, заведённых 0324 и 0325, и все — с одним и тем же диагнозом.
--
--   press_action        снят `open`            — второе написание разутюжки. Первое и единственное
--                                                теперь — ГЛАГОЛ press_open, за которым стоят живые
--                                                строки прода. Два написания давали два разных
--                                                кортежа в проекции дайджеста CONSTRUCTION.
--   hole_prep           снят `prong_pierce`    — это и есть `none`, сказанный другими словами.
--                                                Колонка спрашивает про ПОДГОТОВИТЕЛЬНЫЙ шаг, а при
--                                                обоих ответах его нет. Вдобавок член выводился из
--                                                соседнего поля (верен ровно при attach_method =
--                                                prong_clinch).
--   reinforcement       снят `fusible_patch`,  — способ у них ОДИН (подложка под место установки),
--                       снят `fabric_stay`,      различались они МАТЕРИАЛОМ, то есть ровно тем, что
--                       заведён `patch`          по объявлению этого же словаря живёт строкой BOM.
--   peel_mode           снят `none`            — целиком выводился из print_method. Гравировка
--                                                отвергает peel_mode вся, шелкография — тоже (новое
--                                                правило Go этой же волны), и собственного факта у
--                                                члена не осталось. На шелкографии он был истинен
--                                                ОДНОВРЕМЕННО с «не указано».
--   pressure_scale      КОЛОНКА СНЯТА ЦЕЛИКОМ  — это был прижим ВТО-блока (press_pressure_n_cm2),
--                                                сказанный словом вместо числа, на шаге, где
--                                                ВТО-блок законен. В форме печатного шага стояли
--                                                два контрола прижима подряд без единого правила
--                                                взаимного исключения, а шапка сообщения
--                                                TechCardOperationPrint обещала, что ВТО-факты
--                                                печатного шага здесь не дублируются.
--   zipper_application  снят `separating_cf`,  — оба отвечали не на вопрос словаря. Словарь про
--                       снят `in_seam_pocket`    ИСПОЛНЕНИЕ (как закрыта лента); «в шве кармана» —
--                                                это zone = pocket, а zone на шаге ОБЯЗАТЕЛЕН;
--                                                «разъёмная по борту» — это zone = closure плюс
--                                                разъёмный артикул строкой BOM.
--   cleaning_kind       снят `chalk_removal`,  — оба это `spot_clean` с названным веществом, то
--                       снят `adhesive_removal`  есть ответ на ось «что это за след», которую сам
--                                                словарь объявил отложенной. Вещество — прозой в
--                                                note шага.
--   coverage_mode       снят `first_output`    — не охват, а ПОВОД. Первую единицу смотрят
--                                                сплошняком, то есть `each_unit` и `first_output`
--                                                истинны одновременно, а колонка одна.
--   press_toward        снят `side`            — на боковом шве это то же самое, что
--                                                `away_from_center`, а на плечевом «к боку» не
--                                                значит ничего.
--
-- ЗАМЕР ОБЕИХ БАЗ (2026-08-21 13:40 UTC, непосредственно перед выкаткой; команды приведены ниже
-- дословно, чтобы их можно было повторить, а не поверить).
--
--   SELECT id FROM gorp_migrations ORDER BY id DESC LIMIT 1;
--     ПРОД (grbpwr):      0323_file_role_per_project.sql   (применена 2026-08-18 22:14:56)
--     БЕТА (grbpwr_beta): 0326_topstitch_drop_width.sql    (применена 2026-08-21 13:30:08)
--
--   SELECT COLUMN_NAME FROM information_schema.COLUMNS WHERE TABLE_SCHEMA = DATABASE()
--     AND TABLE_NAME = 'tech_card_operation' AND COLUMN_NAME IN (...девять колонок...);
--     ПРОД: press_action, press_toward, pressure_scale, hole_prep, reinforcement, peel_mode,
--           zipper_application, cleaning_kind, coverage_mode — НЕ СУЩЕСТВУЮТ НИ ОДНОЙ.
--     БЕТА: существуют все девять.
--
-- ЭТО НЕ СНИМОК МИНУТЫ, А СВОЙСТВО СХЕМЫ, и в этом всё отличие от 0326. Прод стоит на 0323, то
-- есть колонок волн 0324 и 0325 там ФИЗИЧЕСКИ НЕТ. Владелец, работающий на проде прямо сейчас, не
-- может записать значение в колонку, которой нет, и клиент не может её прислать. Ноль здесь
-- доказывается information_schema, а не COUNT(*), и протухнуть между замером и выкаткой не может:
-- 0324, 0325, 0326 и этот файл приедут на прод ОДНИМ стартом, и к моменту, когда до него дойдёт
-- очередь, колонки будут ровно секунду как созданы и пусты по построению.
--
--   SELECT COUNT(*), SUM(press_action = 'open'), SUM(hole_prep = 'prong_pierce'),
--          SUM(peel_mode = 'none'), SUM(zipper_application IN ('separating_cf','in_seam_pocket')),
--          SUM(cleaning_kind IN ('chalk_removal','adhesive_removal')),
--          SUM(coverage_mode = 'first_output'),
--          SUM(reinforcement IN ('fusible_patch','fabric_stay')), SUM(press_toward = 'side'),
--          SUM(pressure_scale IS NOT NULL) FROM tech_card_operation;
--     БЕТА: 11 строк, и ВСЕ ДЕВЯТЬ КОЛОНОК ПУСТЫ ЦЕЛИКОМ — ни одного не-NULL значения ни в одной.
--     ПРОД: запрос неисполним, колонок нет (см. выше).
--
--   SELECT COUNT(*) FROM tech_card_signoff;  ПРОД 0, БЕТА 0
--   SELECT COUNT(*) FROM tech_card_release;  ПРОД 0, БЕТА 2
--
-- ⚠️ ВОСЕМЬ ИЗ ДЕВЯТИ ПРАВОК — СУЖЕНИЕ СЛОВАРНОГО CHECK'А, А НЕ РАСШИРЕНИЕ. `ADD CONSTRAINT CHECK`
-- в MySQL 8 перечитывает ВСЮ ИСТОРИЮ таблицы, а не будущие записи: одна строка со снятым токеном —
-- и ALTER падает на 3819, строка в gorp_migrations не появляется, приложение не стартует. Это уже
-- однажды остановило старт прода. Здесь сужение законно по двум причинам сразу: у восьми словарей
-- переносить нечего (доказано схемой прода и полной пустотой беты), а у девятого — reinforcement —
-- перенос СТОИТ ВЫШЕ СУЖЕНИЯ В ЭТОМ ЖЕ ФАЙЛЕ и не теряет ни одного факта.
--
-- ПОЧЕМУ У reinforcement ПЕРЕНОС ЕСТЬ, А У ОСТАЛЬНЫХ ВОСЬМИ — НЕТ. Перенос законен только там, где
-- у снимаемого члена есть цель, приземляющая ТОТ ЖЕ факт. У `fusible_patch` и `fabric_stay` она
-- есть — `patch`, — и перенос стирает написание, а не смысл: материал подложки по объявлению
-- словаря и так живёт строкой BOM. У остальных восьми цели нет ни одной. `open` приземлился бы в
-- ГЛАГОЛ press_open, а глагол — позиция 2 ГОЛОВЫ кортежа дайджеста, и такой UPDATE сдвинул бы
-- отпечаток CONSTRUCTION у переехавших карточек; `first_output` не выражается охватом вовсе;
-- `chalk_removal` без своего вещества стал бы просто `spot_clean`, потеряв «мел». Поэтому там
-- обоснование другое и оно проверяемое — колонки на проде нет, на бете она пуста, — а файл обязан
-- УПАСТЬ ЧЕСТНО, если это перестанет быть правдой. Он и падает: 3819 с именем констрейнта.
--
-- ГОЛОВА КОРТЕЖА ДАЙДЖЕСТА НЕ ДВИГАЕТСЯ НИ ОДНОЙ ИЗ ДЕВЯТИ ПРАВОК. Все девять колонок уезжают в
-- дайджест ХВОСТОВЫМИ ПАРАМИ «имя колонки, значение» (0325), а пара рождается только у
-- ЗАПОЛНЕННОГО поля. Все девять пусты у всех до единой сохранённых строк, значит ни одна строка
-- сегодня этих пар не эмитит и снятие их байты не двигает.
--
-- Номера и имена снятых членов ЗАРЕЗЕРВИРОВАНЫ в proto — и номером, и именем. Номер без имени
-- позволил бы вернуть имя новому номеру; имя без номера позволило бы отдать номер новому смыслу, и
-- старый клиент прочитал бы его как прежний член — молча, без единой ошибки на проводе.

-- ШАГ 1 ИЗ 3. reinforcement ПОЛУЧАЕТ `patch`, ОСТАВАЯСЬ ПРИ ОБОИХ СТАРЫХ ЧЛЕНАХ.
--
-- Отдельным ALTER'ом, и это не небрежность, а несущий порядок. UPDATE шага 2 обязан стоять ВЫШЕ
-- сужения — иначе до него не дошло бы, ALTER упал бы на первой же строке со снятым токеном, — но
-- сам он законен только тогда, когда действующий в эту секунду CHECK уже принимает `patch`. В 0326
-- этой цены не было (`edge` был членом и старого словаря), здесь она есть, и платится она одной
-- лишней копией таблицы на 105 строк прода. Расширение альтернации безопасно всегда: оно ничего не
-- отвергает ретроактивно.
--
-- ГЕЙТ — ПО СОДЕРЖИМОМУ КЛАУЗЫ, а не по факту существования констрейнта: он есть и до, и после
-- файла. Пробой служит отсутствие `patch` в CHECK_CLAUSE. Подстроки `patch` в старом списке
-- (none|fusible_patch|fabric_stay|tape|seam_catch|other) нет ВНЕ `fusible_patch`, поэтому проба
-- пишется как '%|patch|%' — с обеих сторон разделителем альтернации.
SET @chk_op_reinforcement_wide := (SELECT COUNT(*) FROM information_schema.TABLE_CONSTRAINTS tc
    JOIN information_schema.CHECK_CONSTRAINTS cc
      ON cc.CONSTRAINT_SCHEMA = tc.CONSTRAINT_SCHEMA AND cc.CONSTRAINT_NAME = tc.CONSTRAINT_NAME
    WHERE tc.TABLE_SCHEMA = DATABASE() AND tc.TABLE_NAME = 'tech_card_operation'
      AND tc.CONSTRAINT_NAME = 'chk_op_reinforcement' AND cc.CHECK_CLAUSE NOT LIKE '%|patch|%');
SET @ddl := IF(@chk_op_reinforcement_wide = 1,
    'ALTER TABLE tech_card_operation
        DROP CHECK chk_op_reinforcement,
        ADD CONSTRAINT chk_op_reinforcement CHECK (reinforcement IS NULL OR (reinforcement REGEXP ''^(none|patch|fusible_patch|fabric_stay|tape|seam_catch|other)$'' AND STRCMP(CAST(reinforcement AS BINARY), CAST(LOWER(reinforcement) AS BINARY)) = 0))',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- ШАГ 2 ИЗ 3. ПЕРЕНОС ДВУХ ПИСЬМЕН ОДНОГО СПОСОБА В ОДИН ЧЛЕН.
--
-- ГЕЙТ НЕ НУЖЕН — оператор идемпотентен сам по себе: повторный прогон не находит ни одной строки
-- (после первого их не осталось) и не пишет ничего. Обернуть его в IF по information_schema значило
-- бы завести второе, более хрупкое условие вместо точного, которое уже стоит в WHERE.
UPDATE tech_card_operation SET reinforcement = 'patch'
 WHERE reinforcement IN ('fusible_patch', 'fabric_stay');

-- ШАГ 3 ИЗ 3. ВОСЕМЬ СУЖЕНИЙ, ОДНО ОКОНЧАТЕЛЬНОЕ СУЖЕНИЕ reinforcement И СНЯТИЕ КОЛОНКИ — ОДНИМ
-- ALTER'ОМ, ОДНОЙ КОПИЕЙ ТАБЛИЦЫ.
--
-- ADD CONSTRAINT CHECK в 8.0 поддержан только ALGORITHM=COPY, то есть таблица копируется целиком;
-- из спецификаций одного оператора движок берёт самую строгую. Значит переписать заодно COMMENT
-- трёх колонок (они называли снятые члены и правила, которых больше нет) стоит ровно НОЛЬ сверх
-- уже оплаченной копии, а разбить девять правок на девять ALTER'ов значило бы заплатить за копию
-- девять раз. MODIFY стоят ПЕРВЫМИ и до ADD CONSTRAINT — тот же порядок, что в 0324 и 0326:
-- констрейнт нельзя писать против колонки, описанной иначе, чем он допускает. Типы повторены
-- дословно из 0324/0325, потому что MODIFY COLUMN обязан назвать тип целиком; ни одна ширина не
-- меняется (сужение словаря самый длинный токен не удлиняет).
--
-- ГЕЙТ — СУММА ДЕВЯТИ ТОЧНЫХ ПРОБ, а не одна общая. Общая («хоть один констрейнт ещё старый»)
-- ответила бы «да» и на полу-применённом состоянии, которого у одиночного ALTER'а не бывает, но
-- главное — она не отличила бы девять разных списков друг от друга. Каждая проба ищет СВОЙ
-- снимаемый токен в СВОЕЙ клаузе, и каждый из них встречается там ровно один раз:
--   `|open|`          — в press_action встречается однажды и только как член альтернации;
--   `prong_pierce`    — подстрока уникальна во всей схеме;
--   `(none|`          — в peel_mode список начинается с него; у hole_prep `none` ОСТАЁТСЯ, и проба
--                       поэтому привязана к имени констрейнта, а не к одному лишь тексту;
--   `separating_cf`, `chalk_removal`, `first_output`, `fusible_patch` — подстроки уникальны;
--   `|side|`          — в press_toward однажды; в press_action `to_one_side` стоит без правого `|`
--                       после `side`, но проба и не смотрит в чужой констрейнт;
--   pressure_scale    — проба по СУЩЕСТВОВАНИЮ КОЛОНКИ: её CHECK уходит вместе с ней, и «клауза
--                       без токена» о снятой колонке ничего сказать не может.
-- Сумма 9 = ничего ещё не применено. После успеха она равна 0, и повторный прогон не делает ничего.
SET @fs_pending := (
      (SELECT COUNT(*) FROM information_schema.TABLE_CONSTRAINTS tc
         JOIN information_schema.CHECK_CONSTRAINTS cc
           ON cc.CONSTRAINT_SCHEMA = tc.CONSTRAINT_SCHEMA AND cc.CONSTRAINT_NAME = tc.CONSTRAINT_NAME
        WHERE tc.TABLE_SCHEMA = DATABASE() AND tc.TABLE_NAME = 'tech_card_operation'
          AND tc.CONSTRAINT_NAME = 'chk_op_press_action' AND cc.CHECK_CLAUSE LIKE '%|open|%')
    + (SELECT COUNT(*) FROM information_schema.TABLE_CONSTRAINTS tc
         JOIN information_schema.CHECK_CONSTRAINTS cc
           ON cc.CONSTRAINT_SCHEMA = tc.CONSTRAINT_SCHEMA AND cc.CONSTRAINT_NAME = tc.CONSTRAINT_NAME
        WHERE tc.TABLE_SCHEMA = DATABASE() AND tc.TABLE_NAME = 'tech_card_operation'
          AND tc.CONSTRAINT_NAME = 'chk_op_press_toward' AND cc.CHECK_CLAUSE LIKE '%|side|%')
    + (SELECT COUNT(*) FROM information_schema.TABLE_CONSTRAINTS tc
         JOIN information_schema.CHECK_CONSTRAINTS cc
           ON cc.CONSTRAINT_SCHEMA = tc.CONSTRAINT_SCHEMA AND cc.CONSTRAINT_NAME = tc.CONSTRAINT_NAME
        WHERE tc.TABLE_SCHEMA = DATABASE() AND tc.TABLE_NAME = 'tech_card_operation'
          AND tc.CONSTRAINT_NAME = 'chk_op_hole_prep' AND cc.CHECK_CLAUSE LIKE '%prong_pierce%')
    + (SELECT COUNT(*) FROM information_schema.TABLE_CONSTRAINTS tc
         JOIN information_schema.CHECK_CONSTRAINTS cc
           ON cc.CONSTRAINT_SCHEMA = tc.CONSTRAINT_SCHEMA AND cc.CONSTRAINT_NAME = tc.CONSTRAINT_NAME
        WHERE tc.TABLE_SCHEMA = DATABASE() AND tc.TABLE_NAME = 'tech_card_operation'
          AND tc.CONSTRAINT_NAME = 'chk_op_reinforcement' AND cc.CHECK_CLAUSE LIKE '%fusible_patch%')
    + (SELECT COUNT(*) FROM information_schema.TABLE_CONSTRAINTS tc
         JOIN information_schema.CHECK_CONSTRAINTS cc
           ON cc.CONSTRAINT_SCHEMA = tc.CONSTRAINT_SCHEMA AND cc.CONSTRAINT_NAME = tc.CONSTRAINT_NAME
        WHERE tc.TABLE_SCHEMA = DATABASE() AND tc.TABLE_NAME = 'tech_card_operation'
          AND tc.CONSTRAINT_NAME = 'chk_op_peel_mode' AND cc.CHECK_CLAUSE LIKE '%(none|%')
    + (SELECT COUNT(*) FROM information_schema.TABLE_CONSTRAINTS tc
         JOIN information_schema.CHECK_CONSTRAINTS cc
           ON cc.CONSTRAINT_SCHEMA = tc.CONSTRAINT_SCHEMA AND cc.CONSTRAINT_NAME = tc.CONSTRAINT_NAME
        WHERE tc.TABLE_SCHEMA = DATABASE() AND tc.TABLE_NAME = 'tech_card_operation'
          AND tc.CONSTRAINT_NAME = 'chk_op_zipper_application' AND cc.CHECK_CLAUSE LIKE '%separating_cf%')
    + (SELECT COUNT(*) FROM information_schema.TABLE_CONSTRAINTS tc
         JOIN information_schema.CHECK_CONSTRAINTS cc
           ON cc.CONSTRAINT_SCHEMA = tc.CONSTRAINT_SCHEMA AND cc.CONSTRAINT_NAME = tc.CONSTRAINT_NAME
        WHERE tc.TABLE_SCHEMA = DATABASE() AND tc.TABLE_NAME = 'tech_card_operation'
          AND tc.CONSTRAINT_NAME = 'chk_op_cleaning_kind' AND cc.CHECK_CLAUSE LIKE '%chalk_removal%')
    + (SELECT COUNT(*) FROM information_schema.TABLE_CONSTRAINTS tc
         JOIN information_schema.CHECK_CONSTRAINTS cc
           ON cc.CONSTRAINT_SCHEMA = tc.CONSTRAINT_SCHEMA AND cc.CONSTRAINT_NAME = tc.CONSTRAINT_NAME
        WHERE tc.TABLE_SCHEMA = DATABASE() AND tc.TABLE_NAME = 'tech_card_operation'
          AND tc.CONSTRAINT_NAME = 'chk_op_coverage_mode' AND cc.CHECK_CLAUSE LIKE '%first_output%')
    + (SELECT COUNT(*) FROM information_schema.COLUMNS
        WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_operation'
          AND COLUMN_NAME = 'pressure_scale'));
SET @ddl := IF(@fs_pending = 9,
    'ALTER TABLE tech_card_operation
        MODIFY COLUMN hole_prep VARCHAR(16) NULL COMMENT ''H2: готовим ли отверстие ОТДЕЛЬНЫМ шагом; HARDWARE_SET и MACHINE+buttonhole|button_attach|bartack; none = ЯВНО без отдельного отверстия, в том числе когда фурнитура прокалывает сама'',
        MODIFY COLUMN peel_mode VARCHAR(8) NULL COMMENT ''P2: съём носителя; отвергается при print_method = laser_engrave и screen (носителя у них нет); NULL = не указано'',
        MODIFY COLUMN press_action VARCHAR(16) NULL COMMENT ''ВТО: ЧТО ИМЕННО делаем; только на PRESS; на PRESS_OPEN отвергается — разутюжка выражается САМИМ глаголом; НЕ required; NULL = не указано'',
        DROP CHECK chk_op_press_action,
        DROP CHECK chk_op_press_toward,
        DROP CHECK chk_op_hole_prep,
        DROP CHECK chk_op_reinforcement,
        DROP CHECK chk_op_peel_mode,
        DROP CHECK chk_op_zipper_application,
        DROP CHECK chk_op_cleaning_kind,
        DROP CHECK chk_op_coverage_mode,
        DROP CHECK chk_op_pressure_scale,
        DROP COLUMN pressure_scale,
        ADD CONSTRAINT chk_op_press_action CHECK (press_action IS NULL OR (press_action REGEXP ''^(press_flat|to_one_side|steam|final|ease_in|stretch|mould|other)$'' AND STRCMP(CAST(press_action AS BINARY), CAST(LOWER(press_action) AS BINARY)) = 0)),
        ADD CONSTRAINT chk_op_press_toward CHECK (press_toward IS NULL OR (press_toward REGEXP ''^(front|back|up|down|toward_center|away_from_center|sleeve|body|facing|shell|lining|other)$'' AND STRCMP(CAST(press_toward AS BINARY), CAST(LOWER(press_toward) AS BINARY)) = 0)),
        ADD CONSTRAINT chk_op_hole_prep CHECK (hole_prep IS NULL OR (hole_prep REGEXP ''^(none|awl_pierce|punch)$'' AND STRCMP(CAST(hole_prep AS BINARY), CAST(LOWER(hole_prep) AS BINARY)) = 0)),
        ADD CONSTRAINT chk_op_reinforcement CHECK (reinforcement IS NULL OR (reinforcement REGEXP ''^(none|patch|tape|seam_catch|other)$'' AND STRCMP(CAST(reinforcement AS BINARY), CAST(LOWER(reinforcement) AS BINARY)) = 0)),
        ADD CONSTRAINT chk_op_peel_mode CHECK (peel_mode IS NULL OR (peel_mode REGEXP ''^(hot|warm|cold)$'' AND STRCMP(CAST(peel_mode AS BINARY), CAST(LOWER(peel_mode) AS BINARY)) = 0)),
        ADD CONSTRAINT chk_op_zipper_application CHECK (zipper_application IS NULL OR (zipper_application REGEXP ''^(centered|lapped|invisible|exposed|fly|other)$'' AND STRCMP(CAST(zipper_application AS BINARY), CAST(LOWER(zipper_application) AS BINARY)) = 0)),
        ADD CONSTRAINT chk_op_cleaning_kind CHECK (cleaning_kind IS NULL OR (cleaning_kind REGEXP ''^(spot_clean|dust_lint|other)$'' AND STRCMP(CAST(cleaning_kind AS BINARY), CAST(LOWER(cleaning_kind) AS BINARY)) = 0)),
        ADD CONSTRAINT chk_op_coverage_mode CHECK (coverage_mode IS NULL OR (coverage_mode REGEXP ''^(each_unit|sample_per_bundle|aql_plan|other)$'' AND STRCMP(CAST(coverage_mode AS BINARY), CAST(LOWER(coverage_mode) AS BINARY)) = 0))',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- +migrate Down

-- Down ВОЗВРАЩАЕТ СЛОВАРИ И КОЛОНКУ — И НЕ ВОЗВРАЩАЕТ ЗАПИСИ. Расширение альтернации безопасно
-- всегда, поэтому сам ALTER пройдёт при любом состоянии данных; но обратного UPDATE'а `patch` в
-- два прежних написания здесь НЕТ И БЫТЬ НЕ МОЖЕТ — признака, который отличал бы клеевую заплатку
-- от тканевой подложки, в таблице после переноса не осталось ни одного, и обратный оператор
-- переписал бы заодно те `patch`, которые технолог выбрал честно уже после выкатки.
--
-- Поэтому reinforcement возвращается СУПЕРСЕТОМ — список 0324 ПЛЮС `patch`, — а не списком 0324
-- дословно. Довод тот же, по которому Down 0325 не сужает шесть расширенных словарей обратно:
-- сужение это ADD CONSTRAINT с коротким списком, и он упал бы на первой же строке, где стоит
-- значение, которого в коротком списке нет. Старый бинарь `patch` не пишет никогда, а суперсет
-- ретроактивно не отвергает ничего — то есть откат делает ровно то, ради чего он нужен: возвращает
-- работоспособность прежнему коду.
--
-- Колонка pressure_scale возвращается ПУСТОЙ. Это не потеря: на день выкатки она была пуста на
-- бете и не существовала на проде — восстанавливать нечего по построению.
--
-- ПОЛНОГО ОТКАТА У ЭТОГО ФАЙЛА НЕТ, и рассчитывать на него при планировании выкатки нельзя. Down
-- здесь — инструмент разработки (up → down → up на одноразовой базе), а не машина времени.
--
-- Гейт зеркальный и по тому же принципу: откатывать есть что, только пока колонки pressure_scale
-- НЕТ. Она снимается последним ALTER'ом Up и возвращается первым ALTER'ом Down, то есть её
-- отсутствие и есть «Up применён».
SET @fs_applied := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_operation'
      AND COLUMN_NAME = 'pressure_scale');
SET @ddl := IF(@fs_applied = 0,
    'ALTER TABLE tech_card_operation
        MODIFY COLUMN hole_prep VARCHAR(16) NULL COMMENT ''H2: подготовка отверстия; HARDWARE_SET и MACHINE+buttonhole|button_attach|bartack; none = ЯВНО без отверстия'',
        MODIFY COLUMN peel_mode VARCHAR(8) NULL COMMENT ''P2: съём носителя; отвергается при print_method = laser_engrave; none = носителя нет, NULL = не указано'',
        MODIFY COLUMN press_action VARCHAR(16) NULL COMMENT ''ВТО: ЧТО ИМЕННО делаем; PRESS целиком, на PRESS_OPEN только open; НЕ required; NULL = не указано'',
        ADD COLUMN pressure_scale VARCHAR(8) NULL COMMENT ''P6: давление прижима, упорядоченная шкала; отвергается при laser_engrave; NULL = не указано'' AFTER second_press_sec,
        DROP CHECK chk_op_press_action,
        DROP CHECK chk_op_press_toward,
        DROP CHECK chk_op_hole_prep,
        DROP CHECK chk_op_reinforcement,
        DROP CHECK chk_op_peel_mode,
        DROP CHECK chk_op_zipper_application,
        DROP CHECK chk_op_cleaning_kind,
        DROP CHECK chk_op_coverage_mode,
        ADD CONSTRAINT chk_op_press_action CHECK (press_action IS NULL OR (press_action REGEXP ''^(press_flat|to_one_side|open|steam|final|ease_in|stretch|mould|other)$'' AND STRCMP(CAST(press_action AS BINARY), CAST(LOWER(press_action) AS BINARY)) = 0)),
        ADD CONSTRAINT chk_op_press_toward CHECK (press_toward IS NULL OR (press_toward REGEXP ''^(front|back|up|down|toward_center|away_from_center|sleeve|body|facing|shell|lining|side|other)$'' AND STRCMP(CAST(press_toward AS BINARY), CAST(LOWER(press_toward) AS BINARY)) = 0)),
        ADD CONSTRAINT chk_op_hole_prep CHECK (hole_prep IS NULL OR (hole_prep REGEXP ''^(none|prong_pierce|awl_pierce|punch)$'' AND STRCMP(CAST(hole_prep AS BINARY), CAST(LOWER(hole_prep) AS BINARY)) = 0)),
        ADD CONSTRAINT chk_op_reinforcement CHECK (reinforcement IS NULL OR (reinforcement REGEXP ''^(none|patch|fusible_patch|fabric_stay|tape|seam_catch|other)$'' AND STRCMP(CAST(reinforcement AS BINARY), CAST(LOWER(reinforcement) AS BINARY)) = 0)),
        ADD CONSTRAINT chk_op_peel_mode CHECK (peel_mode IS NULL OR (peel_mode REGEXP ''^(none|hot|warm|cold)$'' AND STRCMP(CAST(peel_mode AS BINARY), CAST(LOWER(peel_mode) AS BINARY)) = 0)),
        ADD CONSTRAINT chk_op_zipper_application CHECK (zipper_application IS NULL OR (zipper_application REGEXP ''^(centered|lapped|invisible|exposed|fly|separating_cf|in_seam_pocket|other)$'' AND STRCMP(CAST(zipper_application AS BINARY), CAST(LOWER(zipper_application) AS BINARY)) = 0)),
        ADD CONSTRAINT chk_op_cleaning_kind CHECK (cleaning_kind IS NULL OR (cleaning_kind REGEXP ''^(spot_clean|dust_lint|chalk_removal|adhesive_removal|other)$'' AND STRCMP(CAST(cleaning_kind AS BINARY), CAST(LOWER(cleaning_kind) AS BINARY)) = 0)),
        ADD CONSTRAINT chk_op_coverage_mode CHECK (coverage_mode IS NULL OR (coverage_mode REGEXP ''^(each_unit|sample_per_bundle|aql_plan|first_output|other)$'' AND STRCMP(CAST(coverage_mode AS BINARY), CAST(LOWER(coverage_mode) AS BINARY)) = 0)),
        ADD CONSTRAINT chk_op_pressure_scale CHECK (pressure_scale IS NULL OR (pressure_scale REGEXP ''^(light|medium|firm)$'' AND STRCMP(CAST(pressure_scale AS BINARY), CAST(LOWER(pressure_scale) AS BINARY)) = 0))',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;
