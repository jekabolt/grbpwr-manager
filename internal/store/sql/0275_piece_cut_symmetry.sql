-- +migrate Up

-- КАК ДЕТАЛЬ КРОИТСЯ. Не воскрешение `mirrored` — новая колонка, потому что воскрешать нечего:
-- 0266 выполнила UPDATE … SET pieces_per_garment = ×2, mirrored = 0 WHERE mirrored = 1, то есть
-- после неё строк с единицей НЕТ ПО ПОСТРОЕНИЮ, а down там сознательно ничего не делает. «4 штуки»
-- и «2 зеркальные пары» с тех пор — одна и та же строка.
--
-- ПОЧЕМУ ОДНА КОЛОНКА, А НЕ «парность» + «сгиб». Количество после 0266 живёт в
-- pieces_per_garment целиком, и НИ ОДНО из трёх значений ничего не умножает (вернуть множитель =
-- вернуть тот самый баг тех-пака, из-за которого 0266 и писалась). Значит поле отвечает только на
-- вопрос «как связаны эти n панелей». А тогда состояния взаимно исключают друг друга:
--
--   * крой по сгибу — это объединение половины лекала с её ОТРАЖЕНИЕМ относительно линии сгиба,
--     поэтому контур такой детали зеркально-симметричен ПО ПОСТРОЕНИЮ;
--   * отражение симметричного контура конгруэнтно ему самому, значит «со сгибом И зеркальная
--     пара» — геометрически невозможная комбинация, а не редкая;
--   * а «со сгибом и нужна дважды» (манжеты) выражается как fold + pieces_per_garment = 2:
--     хиральность спрашивать не надо, предыдущий пункт её уже определил.
--
-- Два NULLable поля дали бы 16 комбинаций при 4 осмысленных, ДВА независимых «не размечено»,
-- умеющих разойтись, и тот же запрет пары fold+mirrored — то есть ту же теорему, но дважды.
--
-- NULL ЗНАЧИТ «НЕ РАЗМЕЧЕНО» И НИЧЕГО БОЛЬШЕ. Бэкфилла нет и быть не может: сигнала в данных не
-- осталось (см. выше), а DEFAULT 'identical' был бы утверждением «это одинаковые копии» про строку,
-- которая на самом деле парная — ровно тот брак, ради которого поле и заводится (полукомплект
-- лекал ⇒ 44 левых полочки и ноль правых). Неизвестное обязано читаться как неизвестное; поле
-- поэтому NULLable, а не NOT NULL DEFAULT.
--
-- Свободный текст в note («left + right», «single cut, on the fold» — см. internal/betaseed/plm.go)
-- НЕ парсится: эвристика по заметке — это то же самое утверждение, только с алиби. Она годится
-- ровно на подсказку в UI кампании Д2, и там она и живёт, ничего не записывая.
--
-- РЕТИРОВАННАЯ КОЛОНКА mirrored НЕ ТРОГАЕТСЯ И НЕ ДРОПАЕТСЯ. Она стоит в кортеже дайджеста
-- CONSTRUCTION (internal/dto/techcard_section_digest.go) замороженной константой false, а
-- json.Marshal кодирует кортеж ПОЗИЦИОННО: убрать элемент ломает отпечаток ровно так же, как
-- добавить безусловный. То есть дроп колонки — это «изменено с момента подписи» разом на ВСЕХ
-- утверждённых карточках, в момент выкатки, ради нуля функциональной пользы. Не убирать.
--
-- ИДЕМПОТЕНТНОСТЬ (CLAUDE.md): MySQL 8 автокоммитит DDL, поэтому падение в середине файла оставляет
-- схему полуприменённой без строки в gorp_migrations, и следующая загрузка перезапускает файл с
-- начала. Три шага, каждый под своей проверкой в information_schema, PREPARE/EXECUTE/DEALLOCATE по
-- одному стейтменту на строку. CHECK'и ИМЕНОВАНЫ — авто-имя <table>_chk_<n> позиционно, дропать его
-- нельзя.
--
-- БЕЗОПАСНОСТЬ ПРОТИВ ПРОДА: ADD COLUMN NULL ничего не переписывает, а оба CHECK'а валидируют
-- существующие строки при добавлении — и проходят тривиально, потому что на этот момент
-- cut_symmetry везде NULL, а обе проверки на NULL истинны. Таблица маленькая (детали одной
-- карточки), ALTER мгновенный.

SET @col_exists := (
    SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'tech_card_piece'
      AND COLUMN_NAME = 'cut_symmetry'
);
SET @ddl := IF(@col_exists = 0,
    'ALTER TABLE tech_card_piece ADD COLUMN cut_symmetry VARCHAR(16) NULL COMMENT ''как деталь кроится: identical (n конгруэнтных копий) | mirrored (n делится на две зеркальные половины) | fold (крой по сгибу, контур симметричен сам себе, поэтому парной такая деталь не бывает). NULL = НЕ РАЗМЕЧЕНО, это НЕ «обычная»'' AFTER pieces_per_garment',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- Словарь закрыт в схеме, а не только в Go: смысл значения — указание цеху, и строка, попавшая мимо
-- словаря ручным UPDATE'ом, читалась бы как «не размечено» нигде и как мусор везде. Дрифт словаря
-- между этим CHECK'ом и entity.ValidTechCardPieceCutSymmetries ловит
-- internal/store/migrationlint.TestPieceCutSymmetryDBCheckNoDrift — он грепает ИМЕННО этот REGEXP,
-- поэтому литерал шаблона намеренно БЕЗ префикса набора символов (см. соседний CHECK).
--
-- НАБОР СИМВОЛОВ ЛИТЕРАЛА-ШАБЛОНА (измерено на MySQL 8.0.46, оба пути):
--   * ПРЯМОЙ `ALTER TABLE … CHECK (col REGEXP '…')` под `SET NAMES latin1` замораживает шаблон как
--     _latin1 НАВСЕГДА — второй аргумент REGEXP не участвует в агрегации коллаций со столбцом и
--     наследует character_set_connection;
--   * этот же DDL через PREPARE/EXECUTE из пользовательской переменной (форма ниже) даёт _utf8mb4
--     при любом из latin1 / utf8mb3 / utf8mb4 на соединении — текст prepared-стейтмента приводится
--     к utf8mb4 парсером.
-- То есть безопасность здесь даёт САМА PREPARE-форма, требуемая правилом идемпотентности, а не
-- удача. Прямой REGEXP-CHECK в будущей миграции такой защиты не имеет — там префикс обязателен.
SET @chk_vocab := (
    SELECT COUNT(*) FROM information_schema.TABLE_CONSTRAINTS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'tech_card_piece'
      AND CONSTRAINT_NAME = 'chk_tcp_cut_symmetry'
);
SET @ddl := IF(@chk_vocab = 0,
    'ALTER TABLE tech_card_piece ADD CONSTRAINT chk_tcp_cut_symmetry CHECK (cut_symmetry IS NULL OR cut_symmetry REGEXP ''^(identical|mirrored|fold)$'')',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- ЗЕРКАЛЬНАЯ ПАРА ДЕЛИТСЯ ПОПОЛАМ — значит количество обязано быть чётным и не меньше двух.
-- Инвариант живёт в БД, потому что его нарушение не «некрасиво», а неразрешимо: развёртка раскладки
-- отдаёт движку flippedQuantity = n/2, и на n = 3 никакое правило округления не задано никем.
-- Go-слой повторяет ту же проверку с читаемым сообщением ДО записи
-- (entity.ValidatePieceCutSymmetry) — прецедент 0272: схема несёт инвариант, Go несёт формулировку.
-- Проверка ДВУХКОЛОНОЧНАЯ: она выстрелит и на UPDATE, который правит одно pieces_per_garment, — ещё
-- одна причина, по которой Go обязан отказать первым, иначе оператор получит сырой 3819 про колонку,
-- которую не трогал.
--
-- Литерал явно помечен _utf8mb4 (таблица создана с DEFAULT CHARSET=utf8mb4 — 0109). Здесь это
-- ПОДТВЕРЖДЕНИЕ, а не починка: операнд `<>` агрегирует коллацию со столбцом, поэтому и без префикса
-- он выходит _utf8mb4 (измерено). Префикс оставлен затем, чтобы выражение, которое MySQL хранит
-- НАВСЕГДА, читалось одинаково в файле и в SHOW CREATE TABLE, и чтобы копирование этой строки в
-- прямой (не PREPARE) ALTER не потеряло набор символов молча — см. измерение у соседнего CHECK.
SET @chk_pairs := (
    SELECT COUNT(*) FROM information_schema.TABLE_CONSTRAINTS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'tech_card_piece'
      AND CONSTRAINT_NAME = 'chk_tcp_mirrored_needs_even_count'
);
SET @ddl := IF(@chk_pairs = 0,
    'ALTER TABLE tech_card_piece ADD CONSTRAINT chk_tcp_mirrored_needs_even_count CHECK (cut_symmetry IS NULL OR cut_symmetry <> _utf8mb4''mirrored'' OR (pieces_per_garment >= 2 AND MOD(pieces_per_garment, 2) = 0))',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- +migrate Down

SET @chk_pairs := (
    SELECT COUNT(*) FROM information_schema.TABLE_CONSTRAINTS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'tech_card_piece'
      AND CONSTRAINT_NAME = 'chk_tcp_mirrored_needs_even_count'
);
SET @ddl := IF(@chk_pairs > 0,
    'ALTER TABLE tech_card_piece DROP CHECK chk_tcp_mirrored_needs_even_count',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @chk_vocab := (
    SELECT COUNT(*) FROM information_schema.TABLE_CONSTRAINTS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'tech_card_piece'
      AND CONSTRAINT_NAME = 'chk_tcp_cut_symmetry'
);
SET @ddl := IF(@chk_vocab > 0,
    'ALTER TABLE tech_card_piece DROP CHECK chk_tcp_cut_symmetry',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @col_exists := (
    SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'tech_card_piece'
      AND COLUMN_NAME = 'cut_symmetry'
);
SET @ddl := IF(@col_exists > 0,
    'ALTER TABLE tech_card_piece DROP COLUMN cut_symmetry',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;
