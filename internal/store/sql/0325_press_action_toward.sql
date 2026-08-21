-- +migrate Up

-- ВТО ПЕРЕСТАЁТ БЫТЬ ОДНИМ ГЛАГОЛОМ НА ЧЕТЫРЕ ПРИЁМА, И ЗАУТЮЖИВАНИЕ НАКОНЕЦ НАЗЫВАЕТ СТОРОНУ.
--
-- Подпись глагола press обещает «to one side / steam», а сказать, НА КАКУЮ сторону заутюжен
-- припуск, было нечем — только прозой в note. Проза не попадает ни в подпись, ни на печатный лист:
-- её нельзя ни проверить, ни отпечатать рядом с номером шага. Плюс сам PRESS оставался мешком из
-- четырёх разных приёмов (приутюжить, заутюжить, отпарить, финишное ВТО), и различить их в списке
-- шагов было нельзя вовсе.
--
-- ДВЕ НОВЫЕ КОЛОНКИ, ОБЕ NULLABLE И БЕЗ DEFAULT: NULL значит «НЕ СКАЗАНО». Явного «нет» у этих
-- двух не бывает — заутюживание либо названо, либо нет, — поэтому члена none в словарях НЕТ, в
-- отличие от seam_securing / hole_prep / reinforcement / peel_mode волны 0324. DEFAULT стёр бы
-- разницу между «технолог ответил» и «технолог молчит».
--
-- НИ ОДНО ИЗ ДВУХ ПОЛЕЙ НЕ REQUIRED САМО ПО СЕБЕ, и это принципиально. press_toward обязателен
-- ТОЛЬКО при press_action = 'to_one_side' — значении, которого ни одна сохранённая строка иметь не
-- может (колонки рождаются этим файлом). Ретроактивной обязательности не возникает нигде: старая
-- карточка сохраняется без единой правки. Само правило «toward только при to_one_side» живёт в Go
-- (internal/dto): двухколоночный CHECK проверял бы ВСЮ историю таблицы и отвечал бы голым 3819 без
-- поля и без слов.
--
-- PRESS_OPEN НЕ ТРОГАЕТСЯ. Глагол в проде и в подписанных карточках; разутюжка становится ОДНИМ ИЗ
-- значений press_action ('open'), но каноническая запись остаётся глаголом: пикер пишет PRESS_OPEN
-- и press_action не пишет вовсе. Чтение принимает оба написания, форма НИКОГДА не переписывает одно
-- в другое — два написания дают два разных кортежа в проекции дайджеста секции, и авто-канонизация
-- пометила бы подписанную карточку как «изменена после подписи» без единой человеческой правки.
--
-- «ПРОЧЕЕ» ШЕСТИ ОБЯЗАТЕЛЬНЫХ ДИСКРИМИНАТОРОВ (шаг 2). attach_method, print_method, trim_action,
-- cleaning_kind, coverage_mode и wet_process_kind — REQUIRED каждый на своём глаголе, UNKNOWN у них
-- отвергается, а выхода «прочее» не было ни у одного. Следствие вышло скверное: отсутствие своего
-- приёма не оставляло поле пустым, а ЗАСТАВЛЯЛО технолога выбрать ЧУЖОЙ — и это значение уходило
-- дальше в подписанный хвост дайджеста, в релизный снапшот и на печатный лист, после чего отличить
-- «выбрал за неимением своего» от честного ответа было бы нечем. Словари РАСШИРЯЮТСЯ (суперсет),
-- ни один токен не снимается: снятие ретроактивно проверяет всю историю и останавливает старт прода
-- на первой же строке со снятым токеном (память retroactive-check-halts-deploy).
--
-- ⚠️ ADD CONSTRAINT CHECK = ПОЛНАЯ КОПИЯ ТАБЛИЦЫ, и пятиминутный лимит прогона захардкожен в коде.
-- Поэтому копий здесь РОВНО ДВЕ, а не восемь: шаг 1 добавляет обе колонки и оба их CHECK'а ОДНИМ
-- ALTER'ом, шаг 2 пересоздаёт все шесть словарных CHECK'ов ТОЖЕ ОДНИМ. 0324 уже добавил к
-- tech_card_operation 32 CHECK'а, то есть копия этой таблицы по объёму заведомо приемлема.
--
-- НУЛЕВАЯ ВОЛНА ПРОТУХШИХ ПОДПИСЕЙ: обе колонки рождаются NULL на каждой существующей строке,
-- значит ни одной пары в хвосте «press» дайджеста, значит хвост не рождается, значит байты кортежа
-- существующего шага не меняются. Шесть расширенных словарей волну тоже не поднимают: суперсет не
-- меняет ни одного уже записанного значения.
--
-- ИДЕМПОТЕНТНОСТЬ: MySQL 8 автокоммитит DDL, поэтому падение в середине оставляет схему
-- полуприменённой без строки в gorp_migrations, и следующая загрузка перезапускает файл с начала.
-- Каждый шаг — под собственным гейтом information_schema, PREPARE / EXECUTE / DEALLOCATE ПО ОДНОМУ
-- оператору на строку (иначе multiStatements=true в контейнерных тестах маскирует поломку, которая
-- на проде валит старт). Комментарии `--` пишутся строго с пробелом после дефисов: без пробела
-- парсер sql-migrate уводит их в тело оператора.

-- 1. ДВЕ КОЛОНКИ И ДВА ИХ CHECK'А — ОДНИМ ALTER, одной копией таблицы. Гейт — колонка press_action:
--    она добавляется этим же ALTER'ом, значит её наличие и есть «шаг 1 уже прошёл». Оба CHECK'а
--    односоставные и начинаются с `IS NULL OR` — ровно шаблон 0324, — и написаны формой
--    REGEXP + STRCMP-гейт регистра: коллация колонки регистронезависима и на utf8mb3_general_ci
--    прода, и на utf8mb4_0900_ai_ci контейнера, поэтому без STRCMP 'FRONT' лежал бы в базе законно
--    и разъехался бы с токеном enum'а.
--    Ширины: press_action VARCHAR(16) при самом длинном токене 'to_one_side' (11),
--    press_toward VARCHAR(20) при самом длинном 'away_from_center' (16).
SET @press_action_col := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_operation'
      AND COLUMN_NAME = 'press_action');
SET @ddl := IF(@press_action_col = 0,
    'ALTER TABLE tech_card_operation
        ADD COLUMN press_action VARCHAR(16) NULL COMMENT ''ВТО: ЧТО ИМЕННО делаем; PRESS целиком, на PRESS_OPEN только open; НЕ required; NULL = не указано'',
        ADD COLUMN press_toward VARCHAR(20) NULL COMMENT ''ВТО: КУДА лёг припуск; законен ТОЛЬКО при press_action = to_one_side и там обязателен (правило в Go); NULL = не указано'',
        ADD CONSTRAINT chk_op_press_action CHECK (press_action IS NULL OR (press_action REGEXP ''^(press_flat|to_one_side|open|steam|final|ease_in|stretch|mould|other)$'' AND STRCMP(CAST(press_action AS BINARY), CAST(LOWER(press_action) AS BINARY)) = 0)),
        ADD CONSTRAINT chk_op_press_toward CHECK (press_toward IS NULL OR (press_toward REGEXP ''^(front|back|up|down|toward_center|away_from_center|sleeve|body|facing|shell|lining|side|other)$'' AND STRCMP(CAST(press_toward AS BINARY), CAST(LOWER(press_toward) AS BINARY)) = 0))',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- 2. ШЕСТЬ ОБЯЗАТЕЛЬНЫХ ДИСКРИМИНАТОРОВ ПОЛУЧАЮТ ВЫХОД `other` — ОДНИМ ALTER, одной копией.
--    Каждый CHECK пересоздаётся DROP + ADD с ТЕМ ЖЕ именем и списком-СУПЕРСЕТОМ: единственная
--    правка — дописанный последним токен other, порядок и написание остальных сохранены дословно.
--    ЯКОРЬ ГЕЙТА — ИМЕННО ЭТОТ ДОПИСАННЫЙ ТОКЕН, а не первый член списка: гейт обязан отвечать на
--    вопрос «расширение уже применено?», а не «CHECK вообще существует?».
--    Гейт требует, чтобы ВСЕ ШЕСТЬ были ещё старыми (= 6). Одиночный ALTER атомарен, поэтому
--    промежуточного состояния «часть расширена» не бывает, и счёт 6 либо 0 — единственные два.
--    Ни в одном из шести старых списков подстроки 'other' нет, поэтому LIKE ниже однозначен.
--    Ширины колонок трогать не надо: 'other' короче самого длинного токена в каждом из шести.
SET @kind_others := (SELECT COUNT(*) FROM information_schema.TABLE_CONSTRAINTS tc
    JOIN information_schema.CHECK_CONSTRAINTS cc
      ON cc.CONSTRAINT_SCHEMA = tc.CONSTRAINT_SCHEMA AND cc.CONSTRAINT_NAME = tc.CONSTRAINT_NAME
    WHERE tc.TABLE_SCHEMA = DATABASE() AND tc.TABLE_NAME = 'tech_card_operation'
      AND tc.CONSTRAINT_NAME IN ('chk_op_attach_method', 'chk_op_print_method', 'chk_op_trim_action',
                                 'chk_op_cleaning_kind', 'chk_op_coverage_mode', 'chk_op_wet_process_kind')
      AND cc.CHECK_CLAUSE NOT LIKE '%other%');
SET @ddl := IF(@kind_others = 6,
    'ALTER TABLE tech_card_operation
        DROP CHECK chk_op_attach_method,
        DROP CHECK chk_op_print_method,
        DROP CHECK chk_op_trim_action,
        DROP CHECK chk_op_cleaning_kind,
        DROP CHECK chk_op_coverage_mode,
        DROP CHECK chk_op_wet_process_kind,
        ADD CONSTRAINT chk_op_attach_method CHECK (attach_method IS NULL OR (attach_method REGEXP ''^(sew|prong_clinch|press_set|crimp|threaded|other)$'' AND STRCMP(CAST(attach_method AS BINARY), CAST(LOWER(attach_method) AS BINARY)) = 0)),
        ADD CONSTRAINT chk_op_print_method CHECK (print_method IS NULL OR (print_method REGEXP ''^(screen|dtf|heat_transfer|foil|laser_engrave|other)$'' AND STRCMP(CAST(print_method AS BINARY), CAST(LOWER(print_method) AS BINARY)) = 0)),
        ADD CONSTRAINT chk_op_trim_action CHECK (trim_action IS NULL OR (trim_action REGEXP ''^(trim_even|grade_layers|clip_concave|notch_convex|corner_diagonal|turn_and_shape|other)$'' AND STRCMP(CAST(trim_action AS BINARY), CAST(LOWER(trim_action) AS BINARY)) = 0)),
        ADD CONSTRAINT chk_op_cleaning_kind CHECK (cleaning_kind IS NULL OR (cleaning_kind REGEXP ''^(spot_clean|dust_lint|chalk_removal|adhesive_removal|other)$'' AND STRCMP(CAST(cleaning_kind AS BINARY), CAST(LOWER(cleaning_kind) AS BINARY)) = 0)),
        ADD CONSTRAINT chk_op_coverage_mode CHECK (coverage_mode IS NULL OR (coverage_mode REGEXP ''^(each_unit|sample_per_bundle|aql_plan|first_output|other)$'' AND STRCMP(CAST(coverage_mode AS BINARY), CAST(LOWER(coverage_mode) AS BINARY)) = 0)),
        ADD CONSTRAINT chk_op_wet_process_kind CHECK (wet_process_kind IS NULL OR (wet_process_kind REGEXP ''^(rinse|enzyme|garment_dye|softener|other)$'' AND STRCMP(CAST(wet_process_kind AS BINARY), CAST(LOWER(wet_process_kind) AS BINARY)) = 0))',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- +migrate Down
--
-- Down снимает РОВНО ТО, ЧТО ДОБАВИЛ ШАГ 1 — два CHECK'а и две колонки. Шесть расширенных словарей
-- НЕ СУЖАЮТСЯ ОБРАТНО, по тому же доводу, что и семь словарей 0324: сужение — это ADD CONSTRAINT с
-- коротким списком, а он ретроактивно проверяет ВСЮ таблицу и падает на первой же строке, где
-- технолог уже ответил 'other'. Схема застряла бы полуоткатанной.
--
-- Отсюда честный текст для того, кто держит палец над откатом: откат снимает под-глагол ВТО и
-- направление припуска, но НЕ отбирает у шести дискриминаторов выход «прочее». Down здесь —
-- инструмент разработки (up → down → up на одноразовой базе), а не машина времени для прода.

-- D1. Два CHECK'а и две колонки — одним ALTER, порядком, обратным шагу 1. Гейт тот же press_action:
--     пока колонка есть, откатывать есть что.
SET @press_action_back := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_operation'
      AND COLUMN_NAME = 'press_action');
SET @ddl := IF(@press_action_back = 1,
    'ALTER TABLE tech_card_operation
        DROP CHECK chk_op_press_toward,
        DROP CHECK chk_op_press_action,
        DROP COLUMN press_toward,
        DROP COLUMN press_action',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;
