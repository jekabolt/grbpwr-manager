-- Полоса DESIGN: расширение словаря `tech_card_media.kind` тремя значениями — side_l, side_r,
-- render.
--
-- ПОЧЕМУ ЭТО ОТДЕЛЬНЫЙ ФАЙЛ, А НЕ ЧАСТЬ 0345. Это ЕДИНСТВЕННЫЙ COPY всей волны: ADD CHECK
-- перестраивает таблицу целиком, а весь прогон цепочки миграций живёт под пятиминутным потолком,
-- захардкоженным в коде. Всё остальное в волне — новые пустые таблицы и INSTANT ADD COLUMN.
--
-- ЗАМЕР СНЯТ 2026-08-30, И ОН СНИМАЕТ ТРЕВОГУ. На проде `tech_card_media` — **32 строки,
-- 0.02 МБ данных, 0.03 МБ индексов**; на бете — 7 строк. Копия такой таблицы это миллисекунды, и
-- пятиминутный потолок всего прогона цепочки не рядом. Порог «копия не укладывается в минуту»
-- недостижим на два порядка.
--
-- ФАЙЛ ВСЁ РАВНО ОСТАВЛЕН ОТДЕЛЬНЫМ, и это не забытый хвост. Он единственный в волне, чья цена
-- растёт вместе с таблицей: сегодня строк 32, но `tech_card_media` — это плиты и мудборды каждой
-- заведённой карточки, и в момент, когда расширение словаря понадобится СНОВА, замер придётся
-- снимать заново. Отделённый файл вынимается из волны одним движением (удалить файл; ни один
-- другой файл волны его не читает и от него не зависит) — пусть эта дверь останется открытой.
--
-- Как замер снимался, чтобы следующий не искал: кластер пускает по списку доверенных источников,
-- и с машины разработки он доступен ТОЛЬКО ПОД VPN. Без него `mysql` отваливается с
-- `ERROR 2003 … (60)` — это firewall, а не пароль, и пароль при этом берётся нормально через
-- `doctl databases connection <cluster> --format Password --no-header`.
--
-- Проверка «не сузится ли что-то» не нужна: набор значений только РАСШИРЯЕТСЯ, ни одна
-- существующая строка новую проверку не провалит.
--
-- ЧТО ЗА ЗНАЧЕНИЯ. `side_l`/`side_r` — боковые виды: без них флэт бока некуда положить в матрице
-- видов студии. `render` — принятый рендер, лежащий в `category='technical'`, то есть уходящий
-- наружу. ВАЖНО ДЛЯ ЧИТАТЕЛЯ: у `render` в Ф0 нет ни одного писателя — акта «принять рендер» в
-- этой волне не появляется. Значение заводится АВАНСОМ и сознательно: COPY таблицы мы уже платим
-- за `side_l`/`side_r`, и третье значение сверху бесплатно, а вторая перестройка таблицы через
-- фазу — нет. Это не забытый хвост.
--
-- `DROP CHECK` ИДЁТ ПО ЯВНОМУ ИМЕНИ `chk_tech_card_media_kind` (его дал 0073, заменив авто-имя
-- `tech_card_media_chk_1`). Дроп по авто-имени `<table>_chk_<n>` запрещён красным тестом
-- `internal/store/migrationlint/idempotency_test.go:72-74`: такие имена раздаются позиционно при
-- создании таблицы и разъезжаются по истории схемы. Второй, авто-именованный CHECK на этой таблице
-- (`category`, 0092) файл не трогает.
--
-- ИДЕМПОТЕНТНОСТЬ: ALTER охраняется по `information_schema.CHECK_CONSTRAINTS` — по НАЛИЧИЮ
-- значения `render` в тексте проверки, поэтому повторный прогон после половинчатого применения
-- ничего не делает. Ветка «проверки нет вовсе» тоже предусмотрена: тогда constraint просто
-- добавляется. `PREPARE`/`EXECUTE`/`DEALLOCATE` — каждый своей строкой (прод ходит без
-- `multiStatements`).
--
-- DOWN ЗАВЕДОМО ТЕРЯЕТ ДАННЫЕ, И ЭТО ОСОЗНАННО. Узкий CHECK не примет строки с новыми
-- значениями, поэтому Down СНАЧАЛА переводит их в `detail` — вид, который существует во всех
-- редакциях словаря и не врёт про категорию. Что теряется: различение левого и правого бока и
-- пометка «это принятый рендер». Альтернатива — упавший откат, то есть заклиненный старт.

-- +migrate Up

SET @tcm_kind_wide := (SELECT COUNT(*) FROM information_schema.CHECK_CONSTRAINTS
    WHERE CONSTRAINT_SCHEMA = DATABASE()
      AND CONSTRAINT_NAME = 'chk_tech_card_media_kind'
      AND CHECK_CLAUSE LIKE '%render%');
SET @tcm_kind_exists := (SELECT COUNT(*) FROM information_schema.CHECK_CONSTRAINTS
    WHERE CONSTRAINT_SCHEMA = DATABASE()
      AND CONSTRAINT_NAME = 'chk_tech_card_media_kind');
SET @ddl := IF(@tcm_kind_wide = 1,
    'SELECT 1',
    IF(@tcm_kind_exists = 1,
        'ALTER TABLE tech_card_media
            DROP CHECK chk_tech_card_media_kind,
            ADD CONSTRAINT chk_tech_card_media_kind
                CHECK (kind REGEXP ''^(front|back|detail|lining|preview|moodboard|reference|swatch|side_l|side_r|render)$'')',
        'ALTER TABLE tech_card_media
            ADD CONSTRAINT chk_tech_card_media_kind
                CHECK (kind REGEXP ''^(front|back|detail|lining|preview|moodboard|reference|swatch|side_l|side_r|render)$'')'));
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- +migrate Down

-- ОСОЗНАННАЯ ПОТЕРЯ: без этого UPDATE узкий CHECK не примет живые строки и откат упадёт.
UPDATE tech_card_media SET kind = 'detail' WHERE kind IN ('side_l', 'side_r', 'render');

SET @tcm_kind_wide_down := (SELECT COUNT(*) FROM information_schema.CHECK_CONSTRAINTS
    WHERE CONSTRAINT_SCHEMA = DATABASE()
      AND CONSTRAINT_NAME = 'chk_tech_card_media_kind'
      AND CHECK_CLAUSE LIKE '%render%');
SET @ddl_down := IF(@tcm_kind_wide_down = 1,
    'ALTER TABLE tech_card_media
        DROP CHECK chk_tech_card_media_kind,
        ADD CONSTRAINT chk_tech_card_media_kind
            CHECK (kind REGEXP ''^(front|back|detail|lining|preview|moodboard|reference|swatch)$'')',
    'SELECT 1');
PREPARE stmt FROM @ddl_down;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;
