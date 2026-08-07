-- +migrate Up

-- ЧТО ЭТО ЗА ПОЗИЦИЯ (kind) — вторая половина классификации строки спецификации, зеркало
-- НАЗНАЧЕНИЯ (purpose, 0265). purpose отвечает «для чего ткань», kind — «что это за предмет».
-- Пересечься на одной строке они не могут: purpose легален только на метраже
-- (fabric/lining/interlining/insulation), kind — только на всём остальном, кроме этикеток.
--
-- ПОЧЕМУ ОТДЕЛЬНАЯ ОСЬ, А НЕ НОВЫЕ СЕКЦИИ. Ровно тот же аргумент, что в 0265, только с другой
-- стороны: `section` несущая — она задаёт группу закупки и отчётность, и молния, кнопка и люверс
-- все честно section='hardware'. Разнести их по секциям значит поменять поведение секции ради
-- признака, который поведения не имеет вовсе.
--
-- ПОЧЕМУ ОДИН ПЛОСКИЙ СЛОВАРЬ, А НЕ ПО СЛОВАРЮ НА СЕМЬЮ. Значение уже подразумевает свою семью —
-- `zipper` бывает только фурнитурой, — так что словарь на семью записал бы секцию ДВАЖДЫ и позволил
-- двум копиям разойтись. Поэтому пара «вид ↔ его домашняя секция» лежит ДАННЫМИ в
-- entity.bomKindHomeSection, и ValidTechCardBomKinds выведен из ключей этой же таблицы.
--
-- ПОЧЕМУ ПАРА ПРОВЕРЯЕТСЯ В GO, А CHECK ЗАКРЫВАЕТ ТОЛЬКО СЛОВАРЬ. Двухколоночный CHECK
-- (kind ↔ section) выстрелил бы сырым MySQL 3819 на UPDATE'е, который правит ОДНУ секцию, и назвал
-- бы оператору колонку, которой тот не касался, — тот же довод, по которому 0275 держит читаемую
-- формулировку в Go рядом со схемным инвариантом. Здесь схема закрывает словарь (строка, пришедшая
-- мимо приложения, иначе читалась бы как мусор нигде и как значение везде), а пару проверяет store
-- рядом с проверкой назначения, с указанием секции и способа исправить.
--
-- ПОЧЕМУ label ИСКЛЮЧЁН НАМЕРЕННО. Словарём этикеток уже владеет tech_card_label.label_type
-- (main|size|care|origin|flag|hangtag|barcode|special, 0070). Несколько строк-спецификаций этикетки
-- законно ссылаются на ОДИН bom_item_id (материал, на котором они печатаются, 0174/§2.8), а
-- labelsProjection уже хеширует label_type в ПОДПИСАННЫЙ дайджест LABELS. Значит `kind` на
-- section='label' был бы вторым, неподписанным ответом на вопрос, на который ответ уже есть и уже
-- подписан, — и два ответа разошлись бы молча. Единственный владелец словаря этикеток — label_type.
--
-- ПОЧЕМУ НИЧЕГО НЕ БЭКФИЛЛИТСЯ. NULL на каждой существующей строке — это факт, а не пробел:
-- сигнала в данных нет (name свободный текст на десяти языках сразу), а угаданный вид читался бы
-- как утверждение и был бы принят на веру. NULL значит «ещё не классифицировали».
--
-- Идемпотентность (CLAUDE.md): MySQL 8 автокоммитит DDL, поэтому падение в середине файла оставляет
-- схему полуприменённой без строки в gorp_migrations, и следующая загрузка перезапускает файл с
-- начала. Обе колонки и оба CHECK'а едут ОДНИМ ALTER (MySQL 8, атомарный DDL: всё или ничего) под
-- проверкой наличия `kind`, поэтому повтор — no-op, а не «duplicate column». CHECK'и ИМЕНОВАНЫ:
-- авто-имя <table>_chk_<n> позиционно и дрейфует по истории схемы, дропать его нельзя.
--
-- БЕЗОПАСНОСТЬ ПРОТИВ ПРОДА: ADD COLUMN NULL ничего не переписывает, оба CHECK'а валидируют
-- существующие строки при добавлении и проходят тривиально — на этот момент обе колонки везде NULL,
-- а обе проверки на NULL истинны.
--
-- ДВЕ ЛОВУШКИ SQL, унаследованные от 0265 дословно, обе выглядят правильно и обе молчат:
--
-- 1. `kind = ''other''` при kind IS NULL даёт NULL, а CHECK со значением NULL MySQL считает
--    ВЫПОЛНЕННЫМ. То есть очевидная запись `kind_note IS NULL OR kind = ''other''` ловит дырку вида
--    kind='zipper' и пропускает дырку вида kind IS NULL — а NULL это состояние КАЖДОЙ строки до
--    этой миграции. Нужен NULL-безопасный `<=>`, иначе примечание становится теневым видом ровно
--    там, где вида ещё нет.
-- 2. REGEXP наследует коллацию столбца, а под utf8mb3_general_ci прода (и utf8mb4_0900_ai_ci
--    контейнерных тестов) она регистронезависима, так что голый шаблон принял бы 'ZIPPER' — и такая
--    строка не попала бы потом ни в одну группу, а на первом же сохранении вкладки, которая поля не
--    шлёт, ушла бы в store и была бы отвергнута как неизвестный вид на карточке, которую оператор
--    не правил. `REGEXP BINARY` тут НЕ подходит: под utf8mb4_0900_ai_ci MySQL отвечает 3995
--    «Character set … cannot be used in conjunction with binary». Портируемо между utf8mb3 прода и
--    utf8mb4 тестов — сравнить байты через STRCMP с LOWER.
--
-- Шаблон намеренно БЕЗ префикса набора символов: TestBomKindDBCheckNoDrift грепает его как
-- `REGEXP` + кавычки, и `_utf8mb4` между ними заставил бы тест не найти список значений вместо
-- того, чтобы их сравнить. Набор символов здесь и так utf8mb4 — его даёт сама форма
-- PREPARE/EXECUTE, требуемая правилом идемпотентности (измерение — в шапке 0275).

SET @col_exists := (
    SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'tech_card_bom_item'
      AND COLUMN_NAME = 'kind'
);
SET @ddl := IF(@col_exists = 0,
    'ALTER TABLE tech_card_bom_item ADD COLUMN kind VARCHAR(24) NULL COMMENT ''что это за позиция: закрытый словарь для НЕметражных секций (hardware/thread/packaging/trim/decoration/other); NULL = ещё не классифицировали (никогда не угадывается). Метраж классифицируется через purpose, этикетки — через tech_card_label.label_type'' AFTER is_sample, ADD COLUMN kind_note VARCHAR(255) NULL COMMENT ''free-text note, legal only when kind = other'' AFTER kind, ADD CONSTRAINT chk_bom_item_kind CHECK (kind IS NULL OR (kind REGEXP ''^(zipper|zipper_slider|button|snap|rivet|eyelet|hook_and_bar|snap_hook|buckle|strap_adjuster|ring|toggle|cord_stopper|cord_end|magnet|chain|elastic|drawcord|binding|tape|piping|webbing|hook_loop|boning|lace|ribbing|print|embroidery|applique|patch|heat_transfer|rhinestone|sequin|stud|foil|laser|sewing_thread|topstitch_thread|overlock_thread|buttonhole_thread|embroidery_thread|elastic_thread|polybag|carton|hanger|hangtag_string|sticker|tissue|dust_bag|garment_case|insert_card|other)$'' AND STRCMP(CAST(kind AS BINARY), CAST(LOWER(kind) AS BINARY)) = 0)), ADD CONSTRAINT chk_bom_item_kind_note CHECK (kind_note IS NULL OR kind <=> ''other'')',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- +migrate Down
SET @col_exists := (
    SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'tech_card_bom_item'
      AND COLUMN_NAME = 'kind'
);
SET @ddl := IF(@col_exists = 1,
    'ALTER TABLE tech_card_bom_item DROP CONSTRAINT chk_bom_item_kind_note, DROP CONSTRAINT chk_bom_item_kind, DROP COLUMN kind_note, DROP COLUMN kind',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;
