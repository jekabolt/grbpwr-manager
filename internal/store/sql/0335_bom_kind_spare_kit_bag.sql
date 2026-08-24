-- +migrate Up

-- ПАКЕТИК С ЗАПАСНОЙ ФУРНИТУРОЙ. Волна «счётные нормы» заводит запас числом на слоте
-- (spare_qty), и у этого числа обязан быть предмет, в котором запас физически едет с изделием.
-- Сегодня такой строки спецификации назвать НЕЧЕМ: `polybag` неразличим — карточка законно несёт
-- и пакет для самого изделия, и пакетик для запаски, и оба читаются одним словом. Значит проверки
-- готовности («запас есть, а пакетика нет» / «пакетик есть, класть в него нечего») пришлось бы
-- вешать на греп по свободному тексту имени, то есть на десять языков сразу.
--
-- ЭТО НЕ ЛОЖНОЕ РАСЩЕПЛЕНИЕ. У члена есть поведение, которого нет ни у одного соседа по секции:
-- его существование связано со `spare_qty` ДРУГОЙ строки той же карточки, и именно его ищут
-- проверки готовности. Признак, у которого нет ни одного собственного правила, в этот словарь бы
-- не попал — ровно этим доводом 0278 отказал `kind` на этикетках.
--
-- ПОЧЕМУ ЭТО НЕ ДУБЛЬ AUX_SUBTYPE. Здесь ДВА разных субъекта, и прецедент уже стоит парой
-- `dust_bag` (0278) / AUX_SUBTYPE_DUST_BAG (0173):
--   * tech_card_bom_item.kind = 'spare_kit_bag' — «что это за ЗАКУПОЧНАЯ ПОЗИЦИЯ строки»;
--   * tech_card.aux_subtype = 'spare_kit_bag'   — «что ПРОИЗВОДИТ эта вспомогательная карточка».
-- Слово ОДНО на обе стороны: предмет один и тот же, а расходятся написания только у пар с разными
-- предметами (hangtag_string, insert_card, carton). VARCHAR(16) держит: токен 13 символов.
-- Пакетик и покупают готовым, и шьют сами (подтверждено владельцем 2026-08-24), и обе ветки
-- сходятся в ОДНОЙ строке спецификации: 0174 говорит дословно, что материал вспомогательного
-- компонента потребляется «существующим путём BOM». Без члена в aux_subtype свой пакетик уехал бы
-- в AUX_SUBTYPE_OTHER — свалку, из которой его уже не отличить.
--
-- ПОЧЕМУ `tote_bag` ЕДЕТ ТЕМ ЖЕ ФАЙЛОМ. Асимметрия существующая и не наша: 0255 завела
-- AUX_SUBTYPE_TOTE_BAG = 12, но словарь `kind` (0278) шоппера не знает, хотя знает `dust_bag` и
-- `garment_case`. То есть шоппер сегодня нельзя назвать строкой спецификации — а именно строка
-- спецификации и есть единственное место, где вспомогательный компонент стоит денег. Чинить это
-- отдельной миграцией было бы дороже ровно в два раза: ADD CONSTRAINT CHECK в MySQL 8 поддержан
-- ТОЛЬКО алгоритмом COPY (замер в шапке 0324), то есть каждый такой файл КОПИРУЕТ таблицу
-- целиком. Два повода — один проход.
--
-- ЗАМЕР ПЕРЕД ВЫКАТКОЙ ОБЯЗАТЕЛЕН, и он не формальность: лимит на одну миграцию — пять минут, он
-- захардкожен в коде, а стоимость COPY линейна по числу строк перестраиваемой таблицы. Перед
-- выкаткой снять (см. tmp/plans/countable-norms/MEASURE.sql, всегда с ЯВНОЙ базой — прод
-- обновляется вручную):
--     SELECT COUNT(*) AS bom_rows   FROM tech_card_bom_item;   -- шаг 1
--     SELECT COUNT(*) AS card_rows  FROM tech_card;            -- шаг 2
-- Ориентир из 0324, замеренный на одноразовом MySQL 8.0.46: 400 тыс. строк tech_card_bom_item —
-- около 1 с wall-clock на ALTER, то есть запас до потолка трёхзначный. tech_card на порядки
-- меньше. Если замер вдруг покажет миллионы строк — не выкатывать вслепую, а мерить ALTER на
-- копии.
--
-- САМИ СТРОКИ ПРОХОДЯТ ТРИВИАЛЬНО. Оба CHECK'а только РАСШИРЯЮТ словарь, ни один токен не
-- снимается, ни одного UPDATE'а в файле нет. Ретроактивная проверка (а ADD CONSTRAINT проверяет
-- ВСЮ историю таблицы) поэтому не может отвергнуть ни одной существующей строки — цена здесь
-- только в копировании, не в риске отказа. По той же причине сюда НЕ добавляется недостающий
-- регистровый страж на aux_subtype: это было бы СУЖЕНИЕ, оно проверяется ретроактивно и на первой
-- же строке с историческим 'Dust_Bag' остановило бы старт прода. Расширение и сужение не ездят
-- одним файлом.
--
-- НИЧЕГО НЕ БЭКФИЛЛИТСЯ, тем же доводом, что в 0278 и 0255: сигнала в данных нет (имя — свободный
-- текст), а угаданный вид читался бы как утверждение и был бы принят на веру. Существующие
-- полибэги НЕ перечитываются как пакетики с запаской, существующие dust_bag'и — как шопперы.
--
-- ДВЕ ЛОВУШКИ SQL, перенесённые из 0278/0324 ДОСЛОВНО. Обе выглядят правильно и обе молчат:
--
-- 1. REGEXP наследует коллацию столбца, а она регистронезависима и на utf8mb3 прода, и на
--    utf8mb4_0900_ai_ci контейнерных тестов — так что голый шаблон принял бы 'SPARE_KIT_BAG'.
--    Такая строка не попала бы потом ни в одну группу, а на первом же сохранении вкладки, которая
--    поля не шлёт, ушла бы в store и была бы отвергнута как неизвестный вид на карточке, которую
--    оператор не правил. `REGEXP BINARY` тут НЕ подходит: под utf8mb4_0900_ai_ci MySQL отвечает
--    3995 «Character set … cannot be used in conjunction with binary». Портируемо между utf8mb3 и
--    utf8mb4 — сравнить байты через STRCMP с LOWER.
-- 2. Шаблон НАМЕРЕННО без префикса набора символов: TestBomKindDBCheckNoDrift грепает его как
--    `REGEXP` + кавычки, и `_utf8mb4` между ними заставил бы тест не найти список значений вместо
--    того, чтобы их сравнить. Набор символов здесь и так utf8mb4 — его даёт сама форма
--    PREPARE/EXECUTE, требуемая правилом идемпотентности (измерение — в шапке 0275).
--
-- ИДЕМПОТЕНТНОСТЬ. MySQL 8 автокоммитит DDL, поэтому падение в середине файла оставляет схему
-- полуприменённой без строки в gorp_migrations, и следующая загрузка перезапускает файл с начала.
-- Каждый шаг поэтому спрашивает information_schema и выполняется через PREPARE/EXECUTE/DEALLOCATE
-- (по одному оператору в строке — прод ходит без multiStatements). DROP + ADD едут ОДНИМ ALTER'ом:
-- в MySQL 8 DDL атомарен, поэтому окна, в котором колонка осталась бы вовсе без словарного
-- CHECK'а, не возникает — а двухшаговая форма (сначала DROP, отдельно ADD) такое окно открывает,
-- и оно живёт до следующей загрузки. Оба CHECK'а ИМЕНОВАНЫ и дропаются по стабильному имени:
-- авто-имя <table>_chk_<n> позиционно и дрейфует по истории схемы.
--
-- ВЛАДЕНИЕ СПИСКОМ ПЕРЕЕЗЖАЕТ СЮДА. Дрейф-тесты читают ФАЙЛ, ВЛАДЕЮЩИЙ ТЕКУЩИМ СПИСКОМ ТОКЕНОВ:
-- для chk_bom_item_kind это был 0324, для chk_tech_card_aux_subtype — 0255. После этого файла оба
-- якоря указывают на 0335 (TestBomKindDBCheckNoDrift и TestBomKindDBCheckIsCaseClosed в
-- internal/store/migrationlint, TestTechCardAuxSubtypeDBCheckNoDrift там же). Оставить якорь на
-- прежнем файле значило бы сверять entity с УЖЕ ПЕРЕПИСАННЫМ списком и краснеть на здоровой схеме.

-- 1. Виды позиций BOM: +2. `spare_kit_bag` — пакетик с запасной фурнитурой (домашняя секция
--    packaging); `tote_bag` — шоппер (packaging), закрывающий асимметрию с AUX_SUBTYPE_TOTE_BAG.
--    Было 54 токена, стало 56; самый длинный по-прежнему embroidery_stabilizer (21 символ) при
--    колонке VARCHAR(24), новые — 13 и 8. Порядок членов в шаблоне ни на что не влияет (тест
--    сверяет МНОЖЕСТВА), поэтому оба дописаны в хвост — так виден сам факт добавления.
SET @chk_bom_kind := (SELECT COUNT(*) FROM information_schema.TABLE_CONSTRAINTS tc
    JOIN information_schema.CHECK_CONSTRAINTS cc
      ON cc.CONSTRAINT_SCHEMA = tc.CONSTRAINT_SCHEMA AND cc.CONSTRAINT_NAME = tc.CONSTRAINT_NAME
    WHERE tc.TABLE_SCHEMA = DATABASE() AND tc.TABLE_NAME = 'tech_card_bom_item'
      AND tc.CONSTRAINT_NAME = 'chk_bom_item_kind' AND cc.CHECK_CLAUSE NOT LIKE '%spare_kit_bag%');
SET @ddl := IF(@chk_bom_kind = 1,
    'ALTER TABLE tech_card_bom_item
        DROP CHECK chk_bom_item_kind,
        ADD CONSTRAINT chk_bom_item_kind CHECK (kind IS NULL OR (kind REGEXP ''^(zipper|zipper_slider|button|snap|rivet|eyelet|hook_and_bar|snap_hook|buckle|strap_adjuster|ring|toggle|cord_stopper|cord_end|magnet|chain|elastic|drawcord|binding|tape|piping|webbing|hook_loop|boning|lace|ribbing|print|embroidery|applique|patch|heat_transfer|rhinestone|sequin|stud|foil|laser|sewing_thread|topstitch_thread|overlock_thread|buttonhole_thread|embroidery_thread|elastic_thread|polybag|carton|hanger|hangtag_string|sticker|tissue|dust_bag|garment_case|insert_card|other|seam_sealing_tape|embroidery_stabilizer|spare_kit_bag|tote_bag)$'' AND STRCMP(CAST(kind AS BINARY), CAST(LOWER(kind) AS BINARY)) = 0))',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- 2. Подтипы вспомогательной карточки: +1, `spare_kit_bag`. Колонка aux_subtype — VARCHAR(16),
--    токен 13 символов, MODIFY не нужен. Второй CHECK этой колонки (chk_tech_card_aux_subtype_purpose,
--    0173: подтип легален только при purpose='auxiliary') НЕ ТРОГАЕТСЯ — он о другом.
SET @chk_aux := (SELECT COUNT(*) FROM information_schema.TABLE_CONSTRAINTS tc
    JOIN information_schema.CHECK_CONSTRAINTS cc
      ON cc.CONSTRAINT_SCHEMA = tc.CONSTRAINT_SCHEMA AND cc.CONSTRAINT_NAME = tc.CONSTRAINT_NAME
    WHERE tc.TABLE_SCHEMA = DATABASE() AND tc.TABLE_NAME = 'tech_card'
      AND tc.CONSTRAINT_NAME = 'chk_tech_card_aux_subtype' AND cc.CHECK_CLAUSE NOT LIKE '%spare_kit_bag%');
SET @ddl := IF(@chk_aux = 1,
    'ALTER TABLE tech_card
        DROP CHECK chk_tech_card_aux_subtype,
        ADD CONSTRAINT chk_tech_card_aux_subtype CHECK (aux_subtype IS NULL OR aux_subtype REGEXP ''^(brand_label|care_label|size_label|hangtag|sticker|dust_bag|garment_case|tote_bag|box|insert|hanger|other|spare_kit_bag)$'')',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- +migrate Down
--
-- СЛОВАРИ НЕ СУЖАЮТСЯ ОБРАТНО, и это не лень, а тот же довод, по которому Down не сужает словари
-- в 0324 и 0255: сужение — это ADD CONSTRAINT с коротким списком, а он проверяется ретроактивно по
-- ВСЕЙ таблице и падает на первой же строке со `spare_kit_bag`, `tote_bag` или `spare_kit`. Схема
-- застряла бы полуоткатанной (прецедент 0306+0311), а старт прода — остановленным.
--
-- Честный текст для того, кто держит палец над откатом: откатывать здесь нечего и не нужно. Файл
-- ничего не удаляет и ничего не переписывает, он только РАЗРЕШАЕТ три новых значения. Старый
-- бинарь, встретив строку с kind = 'spare_kit_bag', отвергнет её как неизвестный вид — но это
-- вопрос порядка выкатки (бек раньше клиента), а не отката схемы.
SELECT 1;
