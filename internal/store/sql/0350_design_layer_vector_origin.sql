-- Полоса DESIGN, волна 2, кусок 3 из 3: ОТКУДА У СЛОЯ ВЗЯЛСЯ ВЕКТОР — и признак «выбран» у кадра.
--
-- ─── 1. ПРОИСХОЖДЕНИЕ СЛОЯ (W-9) ───────────────────────────────────────────────────────────────
--
-- Владелец: флэты генерятся в РАСТРЕ, для правки переводятся в ВЕКТОР. Сегодня `design_edit_layer`
-- (0343) знает про слой ровно одно — что это калька поверх `base_media_id` (или поверх пустоты), и
-- молча предполагает, что штрихи нарисовал человек. Появляются два новых способа, которыми слой
-- рождается, и оба неотличимы от рисования:
--   * ВЕКТОРИЗАЦИЯ растрового флэта (`imageToImage` вектор-моделью Recraft, не `vectorize`);
--   * ИМПОРТ ЧУЖОГО SVG (тяжёлую правку делают в Illustrator и приносят обратно).
--
-- Различать их обязательно, и вот почему — а не «для полноты». Векторизация ПЛАТНАЯ и оставляет
-- строку прогона; импорт бесплатен и никакой строки не оставляет; рисование — ни то, ни другое.
-- Экран, который не может сказать, откуда взялись эти кривые, не может ни объяснить трату, ни
-- предложить «перевекторизовать заново», ни предупредить «это чужой файл, твои правки поверх него».
-- Провенанс кадра репо уже хранит колонкой, а не догадкой (`design_picture.source_class`, 0340) —
-- слой получает такую же.
--
-- `source_media_id` И `source_picture_id` — ДВА ПОЛЯ, И ЭТО НЕ РАСЩЕПЛЕНИЕ ОДНОГО. Они отвечают на
-- разные вопросы и заполняются в разных сочетаниях:
--   * `source_media_id` — ГДЕ ЛЕЖИТ САМ SVG. Решение разведки дословно: «media держит исходный SVG,
--     `strokes` — редактируемую проекцию, `source_media_id` их связывает». Без него исходник
--     недостижим: `strokes` это ПРОЕКЦИЯ, и обратно в байты, которые принёс человек, она не
--     разворачивается — `download SVG` в ARTIFACTS отдавал бы пересобранный файл вместо принесённого;
--   * `source_picture_id` — ИЗ КАКОГО РАСТРА вектор получен. Это происхождение, а не хранилище:
--     после векторизации человек вправе спросить «а что было на входе» и сравнить.
-- Слой векторизации несёт ОБА (растр-источник и SVG-результат), импортированный — только первое,
-- нарисованный — ни одного. Три сочетания из четырёх осмысленны; один признак их не выразил бы.
--
-- ─── FK-ПОЛИТИКА: ДВЕ ССЫЛКИ, ДВА РАЗНЫХ ОТВЕТА ────────────────────────────────────────────────
--
-- `source_media_id → media(id) ON DELETE RESTRICT` — ВЛАДЕЮЩАЯ ссылка, ровно как `base_media_id` в
-- 0343. Довод тот же и он проверяется мысленным экспериментом со стёртым файлом: без исходного SVG
-- слой, помеченный `imported_svg`, теряет то единственное, что делает его импортированным, а
-- медиатека до удаления показывала бы файл СВОБОДНЫМ. Отказ в удалении здесь не побочный эффект, а
-- сообщение: файл держит незавершённая правка.
--
--   ⚠️ СЛЕДСТВИЕ, КОТОРОЕ ОБЯЗАН ЗАКРЫТЬ КОД, А НЕ ЭТА МИГРАЦИЯ: `TestMediaUsageRegistryCoversSchema`
--   (`internal/store/media_usage_integration_test.go:148-166`) диффит РЕЕСТР против ВСЕХ живых FK в
--   `media(id)` из `information_schema.KEY_COLUMN_USAGE`. Новый ключ обязан получить строку в
--   `mediaRefRegistry` (`internal/store/content/media_usage.go:67`, рядом с
--   `design_edit_layer.base_media_id` на строке 230) — иначе тест краснеет, а `GetMediaUsage`
--   отвечает «файл свободен» про файл, который на самом деле неудаляем. Файл реестра принадлежит
--   ДРУГОМУ владельцу и в этой задаче не правится намеренно.
--
-- `source_picture_id → design_picture(id) ON DELETE SET NULL` — ПРОВЕНАНС, не владение. Три
-- варианта, и выбран третий:
--   * RESTRICT запрещён общим правилом волны (шапка 0340): обе таблицы каскадятся от `tech_card`, а
--     `DeleteTechCard` — ОДИН голый `DELETE FROM tech_card`, где 1451 показал бы человеку «still
--     referenced by another record» и не дал бы ничего, что можно удалить;
--   * ГОЛЫЙ KEY (как у `design_reference.media_id`, 0347) оставил бы висящее число: экран написал
--     бы «векторизовано из картинки 812», которой нет, и это ХУЖЕ отсутствия ответа;
--   * SET NULL говорит правду: растр-источник исчез, происхождение неизвестно, слой продолжает
--     работать — у него есть собственные `strokes`. Прецедент дословный и уже проверенный на стенде
--     — `design_bench_slot.picture_id → design_picture ON DELETE SET NULL` (0341) и
--     `design_sheet_version_plate.slot_id` (0342:80): каскад удаления карточки сносит обе таблицы, а
--     SET NULL по дороге 1451 не поднимает.
--
-- ─── 2. `design_picture.selected` — «выбран» (W-12) ─────────────────────────────────────────────
--
-- Владелец: «можно маркать 3D-рендеры как выбранные». Сегодня у картинки есть ровно один
-- персистентный глагол — `hidden_at`, и он про ДРУГОЕ: спрятать значит убрать с глаз, выбрать —
-- поднять над остальными. Использовать `hidden_at` наоборот («выбран = не спрятан») означало бы, что
-- любой новый кадр рождается выбранным.
--
-- ПОЧЕМУ `TINYINT(1)`, А НЕ ПАРА `selected_at` / `selected_by` ПО ОБРАЗЦУ `hidden_at`/`hidden_by`.
-- Прятание однонаправленно и подотчётно — «кто убрал это с глаз» вопрос к человеку, поэтому там
-- момент и автор. Выбор ЩЁЛКАЕТ туда-обратно сколько угодно раз; штамп времени на нём завёл бы
-- провенанс, которого никто не поддерживает: при снятии галки его пришлось бы обнулять, и первая же
-- забытая ветка кода оставила бы «выбрано 14:41» на невыбранном кадре. Прецедент формы — соседняя
-- колонка той же таблицы, `mixed_input TINYINT(1) NOT NULL DEFAULT 0` (0340).
--
-- КОЛОНКА РОД-АГНОСТИЧНА ПО ПОСТРОЕНИЮ: схема не запрещает пометить флэт. Сужение «выбирать можно
-- только `kind='threed'`» — решение Go, и это НЕ забывчивость: выразить его в схеме можно было бы
-- только ретроактивным CHECK, которого в этой полосе нет ни одного (он копирует таблицу целиком и
-- проверяет всю историю под пятиминутным потолком прогона).
--
-- НЕСКОЛЬКО ВЫБРАННЫХ КАДРОВ ЗАКОННЫ — UNIQUE нет намеренно. Владелец говорит во множественном
-- числе («маркать 3D-рендеры»), а частичный уникальный индекс «один выбранный на карточку» в MySQL
-- не выражается вовсе.
--
-- ─── ЦЕНА ──────────────────────────────────────────────────────────────────────────────────────
--
-- `origin` и `selected` — INSTANT `ADD COLUMN` (NOT NULL с дефолтом, в конец таблицы). Две колонки
-- со ссылками едут ОДНИМ оператором вместе со своим индексом и ключом (приём 0323:29-40): так не
-- возникает половинчатого состояния «колонка есть, ключа нет», а MySQL 8 выполняет один ALTER
-- атомарно. Плата названа вслух: смешанный оператор теряет INSTANT и идёт INPLACE с перестройкой
-- таблицы. Она здесь бесплатна — `design_edit_layer` на проде НЕ СУЩЕСТВУЕТ (прод стоит на 0339), а
-- на бете заведена 0343 и почти пуста; пятиминутный потолок всего прогона
-- (`internal/store/store.go:236`) не рядом. `design_picture` в этом файле только ДОПОЛНЯЕТСЯ
-- колонкой — ни одного индекса на ней не трогается.
--
-- НИ ОДНОГО `ADD CONSTRAINT … CHECK`. `origin` — VARCHAR без проверки, по общему правилу полосы.
--
-- ОКНО «СТАРЫЙ БИНАРЬ × НОВАЯ СХЕМА» БЕЗОПАСНО: хендл стора — `d.Unsafe()`
-- (`internal/store/store.go:252,397`), лишние колонки в `SELECT *` молча игнорируются сканером.
-- Обратная сторона названа вслух: до появления полей в `entity.DesignEditLayer` и
-- `entity.DesignPicture` новые колонки читаются как нули МОЛЧА.
--
-- ИДЕМПОТЕНТНОСТЬ: MySQL авто-коммитит DDL пооператорно, поэтому падение посреди файла оставляет
-- схему полуприменённой БЕЗ строки в `gorp_migrations`, и следующий старт прогонит файл с начала.
-- Каждый оператор — под собственным гейтом `information_schema.COLUMNS`. `PREPARE` / `EXECUTE` /
-- `DEALLOCATE` — каждый своей строкой: прод ходит БЕЗ `multiStatements`, а контейнерный тест эту
-- поломку маскирует.

-- +migrate Up

-- 1. Происхождение слоя.
SET @del_origin := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'design_edit_layer'
      AND COLUMN_NAME = 'origin');
SET @ddl := IF(@del_origin = 0,
    'ALTER TABLE design_edit_layer
        ADD COLUMN origin VARCHAR(16) NOT NULL DEFAULT ''drawn''
            COMMENT ''drawn|vectorized|imported_svg — откуда у слоя вектор. DEFAULT drawn правдив для всех строк до 0350: других способов родиться у слоя не было. Словарь растёт, CHECK намеренно нет''',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- 2. Где лежит сам SVG. Колонка + индекс + ВЛАДЕЮЩИЙ ключ одним оператором.
--    Тип INT (знаковый) согласован с media(id) и с base_media_id 0343: расхождение знаковости даёт
--    3780 на создании FK.
SET @del_src_media := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'design_edit_layer'
      AND COLUMN_NAME = 'source_media_id');
SET @ddl := IF(@del_src_media = 0,
    'ALTER TABLE design_edit_layer
        ADD COLUMN source_media_id INT NULL
            COMMENT ''исходный SVG слоя (импортированный либо привезённый векторизацией); strokes — его редактируемая ПРОЕКЦИЯ, обратно в байты не разворачивается. FK RESTRICT: ссылка ВЛАДЕЮЩАЯ, обязана быть в mediaRefRegistry'',
        ADD KEY idx_design_edit_layer_source_media (source_media_id),
        ADD CONSTRAINT fk_design_edit_layer_source_media FOREIGN KEY (source_media_id)
            REFERENCES media(id) ON DELETE RESTRICT',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- 3. Из какого растра получен вектор. SET NULL — провенанс, а не владение (см. шапку).
--    Тип INT UNSIGNED согласован с design_picture.id.
SET @del_src_pic := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'design_edit_layer'
      AND COLUMN_NAME = 'source_picture_id');
SET @ddl := IF(@del_src_pic = 0,
    'ALTER TABLE design_edit_layer
        ADD COLUMN source_picture_id INT UNSIGNED NULL
            COMMENT ''растр, из которого получен вектор; SET NULL при исчезновении картинки — слой работоспособен и без родословной, а висящее число соврало бы'',
        ADD KEY idx_design_edit_layer_source_picture (source_picture_id),
        ADD CONSTRAINT fk_design_edit_layer_source_picture FOREIGN KEY (source_picture_id)
            REFERENCES design_picture(id) ON DELETE SET NULL',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- 4. Признак «выбран» у кадра.
SET @dp_selected := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'design_picture'
      AND COLUMN_NAME = 'selected');
SET @ddl := IF(@dp_selected = 0,
    'ALTER TABLE design_picture
        ADD COLUMN selected TINYINT(1) NOT NULL DEFAULT 0
            COMMENT ''1 = кадр помечен выбранным (W-12, 3D). Это НЕ обратная сторона hidden_at: спрятать — убрать с глаз, выбрать — поднять над остальными. Выбранных может быть несколько, UNIQUE намеренно нет''',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- +migrate Down
--
-- Порядок обратный Up, гейты симметричные: падение посреди отката оставляет состояние, с которого
-- повтор продолжает. Откат теряет провенанс слоёв и все пометки «выбран» — другого честного
-- поведения у DROP COLUMN нет.
--
-- ВНЕШНИЙ КЛЮЧ СНИМАЕТСЯ ТЕМ ЖЕ ОПЕРАТОРОМ, ЧТО И КОЛОНКА, а текст оператора собирается ПО ФАКТУ:
-- если предыдущий откат успел упасть между шагами, ключа может уже не быть, и его упоминание
-- заклинило бы откат навсегда (приём и ловушка — 0323, Down).
--
-- ОДНОКОЛОНОЧНЫЕ ИНДЕКСЫ ЯВНО НЕ СНИМАЮТСЯ, И ЭТО ВЕРНО ИМЕННО ЗДЕСЬ: MySQL уносит индекс вместе с
-- колонкой, когда снимаются ВСЕ его колонки. Ловушка 0323 (индекс переживает колонку, теряя её из
-- состава) касается только МНОГОКОЛОНОЧНЫХ — здесь таких нет.

SET @dp_selected_down := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'design_picture'
      AND COLUMN_NAME = 'selected');
SET @ddl_down := IF(@dp_selected_down = 1,
    'ALTER TABLE design_picture DROP COLUMN selected',
    'SELECT 1');
PREPARE stmt FROM @ddl_down;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @del_src_pic_down := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'design_edit_layer'
      AND COLUMN_NAME = 'source_picture_id');
SET @del_src_pic_fk := (SELECT COUNT(*) FROM information_schema.TABLE_CONSTRAINTS
    WHERE CONSTRAINT_SCHEMA = DATABASE() AND TABLE_NAME = 'design_edit_layer'
      AND CONSTRAINT_NAME = 'fk_design_edit_layer_source_picture' AND CONSTRAINT_TYPE = 'FOREIGN KEY');
SET @ddl_down := IF(@del_src_pic_down = 1,
    CONCAT('ALTER TABLE design_edit_layer',
        IF(@del_src_pic_fk > 0, ' DROP FOREIGN KEY fk_design_edit_layer_source_picture,', ''),
        ' DROP COLUMN source_picture_id'),
    'SELECT 1');
PREPARE stmt FROM @ddl_down;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @del_src_media_down := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'design_edit_layer'
      AND COLUMN_NAME = 'source_media_id');
SET @del_src_media_fk := (SELECT COUNT(*) FROM information_schema.TABLE_CONSTRAINTS
    WHERE CONSTRAINT_SCHEMA = DATABASE() AND TABLE_NAME = 'design_edit_layer'
      AND CONSTRAINT_NAME = 'fk_design_edit_layer_source_media' AND CONSTRAINT_TYPE = 'FOREIGN KEY');
SET @ddl_down := IF(@del_src_media_down = 1,
    CONCAT('ALTER TABLE design_edit_layer',
        IF(@del_src_media_fk > 0, ' DROP FOREIGN KEY fk_design_edit_layer_source_media,', ''),
        ' DROP COLUMN source_media_id'),
    'SELECT 1');
PREPARE stmt FROM @ddl_down;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @del_origin_down := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'design_edit_layer'
      AND COLUMN_NAME = 'origin');
SET @ddl_down := IF(@del_origin_down = 1,
    'ALTER TABLE design_edit_layer DROP COLUMN origin',
    'SELECT 1');
PREPARE stmt FROM @ddl_down;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;
