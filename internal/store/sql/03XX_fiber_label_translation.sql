-- +migrate Up

-- Составники (care labels): имена волокон на языках ленты + флаг «животное не-текстильное».
--
-- ПОЧЕМУ ЯЗЫКИ — ЗАКРЫТЫЙ СПИСОК, А НЕ FK НА language. Лента печатает ровно десять строк
-- en fr de it es pt nl pl cn jp в этом порядке (макет составника). Это другой список, чем витринный
-- `language`: там нет ES/PT/NL/PL, а японский там `ja`, здесь `jp`. Склеить их — значит либо
-- завести в витрине языки, которых она не обслуживает, либо потерять строки ленты. Поэтому код
-- языка — CHAR(2) с CHECK по списку; тот же список в Go (entity.LabelLangs) и в клиенте.
--
-- ДВЕ ЛОВУШКИ CHECK (как в 0265): REGEXP под utf8mb3_general_ci прода и под utf8mb4_0900_ai_ci
-- контейнера регистронезависим, так что 'EN' прошёл бы и потом не нашёлся бы ни одной выборкой по
-- 'en'. Поэтому к REGEXP добавлено побайтное сравнение с LOWER через STRCMP/CAST AS BINARY
-- (REGEXP BINARY под utf8mb4 отвечает 3995). Таблица новая — CHECK не ретроактивен.
--
-- ПОЧЕМУ ФЛАГ НА fiber БЕЗ CHECK. animal_non_textile — новая колонка с DEFAULT 0 на старой таблице:
-- ни одна вчерашняя строка ей не противоречит, ограничений на неё не вешается.
--
-- СИД. Тело между маркерами печатает scripts/care-labels/seed-from-json.mjs из
-- tmp/plans/care-labels/fiber-translations.json (истина переводов; руками не править —
-- перегенерировать, `--check` сверяет файл с JSON). Каждая строка — INSERT IGNORE … SELECT … FROM
-- fiber WHERE code = …: код, которого нет в этой базе (на проде может не быть ELA), даёт ноль строк —
-- ни ошибки FK, ни сироты. IGNORE — перезапуск файла после падения посередине и уже правленные в
-- админке переводы не ломают миграцию и не затираются. Имена в сиде уже капсом (так в JSON);
-- клиент всё равно печатает toUpperCase.
--
-- Идемпотентность (CLAUDE.md): таблица — IF NOT EXISTS, колонка — под guard по
-- information_schema, PREPARE/EXECUTE/DEALLOCATE по одному оператору на строку (прод без
-- multiStatements), сид — INSERT IGNORE, флаг — идемпотентный UPDATE.

SET @col_exists := (
    SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'fiber'
      AND COLUMN_NAME = 'animal_non_textile'
);
SET @ddl := IF(@col_exists = 0,
    'ALTER TABLE fiber ADD COLUMN animal_non_textile TINYINT(1) NOT NULL DEFAULT 0 COMMENT ''non-textile part of animal origin (leather, fur): the label prints the Art. 12 phrase'' AFTER name',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

CREATE TABLE IF NOT EXISTS fiber_label_translation (
    fiber_code VARCHAR(8) NOT NULL,
    label_lang CHAR(2) NOT NULL COMMENT 'closed care-label language list, NOT language.code',
    name VARCHAR(64) NOT NULL COMMENT 'fibre name as printed on the label; the client prints it upper-case',
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP NOT NULL,
    updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP NOT NULL,
    PRIMARY KEY (fiber_code, label_lang),
    CONSTRAINT chk_fiber_label_lang CHECK (label_lang REGEXP '^(en|fr|de|it|es|pt|nl|pl|cn|jp)$' AND STRCMP(CAST(label_lang AS BINARY), CAST(LOWER(label_lang) AS BINARY)) = 0),
    CONSTRAINT chk_fiber_label_name CHECK (CHAR_LENGTH(TRIM(name)) > 0),
    CONSTRAINT fk_fiber_label_translation_fiber FOREIGN KEY (fiber_code) REFERENCES fiber(code) ON DELETE CASCADE
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4 COMMENT 'Fibre names in the care-label languages (closed list, not language)';

-- BEGIN generated seed: scripts/care-labels/seed-from-json.mjs (do not edit by hand)
-- 11 fibre codes x 10 label languages = 110 candidate rows,
-- a code the database does not know is skipped by the SELECT, never an FK error.
INSERT IGNORE INTO fiber_label_translation (fiber_code, label_lang, name) SELECT f.code, 'en', 'COTTON' FROM fiber f WHERE f.code = 'COT';
INSERT IGNORE INTO fiber_label_translation (fiber_code, label_lang, name) SELECT f.code, 'fr', 'COTON' FROM fiber f WHERE f.code = 'COT';
INSERT IGNORE INTO fiber_label_translation (fiber_code, label_lang, name) SELECT f.code, 'de', 'BAUMWOLLE' FROM fiber f WHERE f.code = 'COT';
INSERT IGNORE INTO fiber_label_translation (fiber_code, label_lang, name) SELECT f.code, 'it', 'COTONE' FROM fiber f WHERE f.code = 'COT';
INSERT IGNORE INTO fiber_label_translation (fiber_code, label_lang, name) SELECT f.code, 'es', 'ALGODÓN' FROM fiber f WHERE f.code = 'COT';
INSERT IGNORE INTO fiber_label_translation (fiber_code, label_lang, name) SELECT f.code, 'pt', 'ALGODÃO' FROM fiber f WHERE f.code = 'COT';
INSERT IGNORE INTO fiber_label_translation (fiber_code, label_lang, name) SELECT f.code, 'nl', 'KATOEN' FROM fiber f WHERE f.code = 'COT';
INSERT IGNORE INTO fiber_label_translation (fiber_code, label_lang, name) SELECT f.code, 'pl', 'BAWEŁNA' FROM fiber f WHERE f.code = 'COT';
INSERT IGNORE INTO fiber_label_translation (fiber_code, label_lang, name) SELECT f.code, 'cn', '棉' FROM fiber f WHERE f.code = 'COT';
INSERT IGNORE INTO fiber_label_translation (fiber_code, label_lang, name) SELECT f.code, 'jp', '綿' FROM fiber f WHERE f.code = 'COT';
INSERT IGNORE INTO fiber_label_translation (fiber_code, label_lang, name) SELECT f.code, 'en', 'WOOL' FROM fiber f WHERE f.code = 'WOL';
INSERT IGNORE INTO fiber_label_translation (fiber_code, label_lang, name) SELECT f.code, 'fr', 'LAINE' FROM fiber f WHERE f.code = 'WOL';
INSERT IGNORE INTO fiber_label_translation (fiber_code, label_lang, name) SELECT f.code, 'de', 'WOLLE' FROM fiber f WHERE f.code = 'WOL';
INSERT IGNORE INTO fiber_label_translation (fiber_code, label_lang, name) SELECT f.code, 'it', 'LANA' FROM fiber f WHERE f.code = 'WOL';
INSERT IGNORE INTO fiber_label_translation (fiber_code, label_lang, name) SELECT f.code, 'es', 'LANA' FROM fiber f WHERE f.code = 'WOL';
INSERT IGNORE INTO fiber_label_translation (fiber_code, label_lang, name) SELECT f.code, 'pt', 'LÃ' FROM fiber f WHERE f.code = 'WOL';
INSERT IGNORE INTO fiber_label_translation (fiber_code, label_lang, name) SELECT f.code, 'nl', 'WOL' FROM fiber f WHERE f.code = 'WOL';
INSERT IGNORE INTO fiber_label_translation (fiber_code, label_lang, name) SELECT f.code, 'pl', 'WEŁNA' FROM fiber f WHERE f.code = 'WOL';
INSERT IGNORE INTO fiber_label_translation (fiber_code, label_lang, name) SELECT f.code, 'cn', '羊毛' FROM fiber f WHERE f.code = 'WOL';
INSERT IGNORE INTO fiber_label_translation (fiber_code, label_lang, name) SELECT f.code, 'jp', '毛' FROM fiber f WHERE f.code = 'WOL';
INSERT IGNORE INTO fiber_label_translation (fiber_code, label_lang, name) SELECT f.code, 'en', 'POLYESTER' FROM fiber f WHERE f.code = 'POL';
INSERT IGNORE INTO fiber_label_translation (fiber_code, label_lang, name) SELECT f.code, 'fr', 'POLYESTER' FROM fiber f WHERE f.code = 'POL';
INSERT IGNORE INTO fiber_label_translation (fiber_code, label_lang, name) SELECT f.code, 'de', 'POLYESTER' FROM fiber f WHERE f.code = 'POL';
INSERT IGNORE INTO fiber_label_translation (fiber_code, label_lang, name) SELECT f.code, 'it', 'POLIESTERE' FROM fiber f WHERE f.code = 'POL';
INSERT IGNORE INTO fiber_label_translation (fiber_code, label_lang, name) SELECT f.code, 'es', 'POLIÉSTER' FROM fiber f WHERE f.code = 'POL';
INSERT IGNORE INTO fiber_label_translation (fiber_code, label_lang, name) SELECT f.code, 'pt', 'POLIÉSTER' FROM fiber f WHERE f.code = 'POL';
INSERT IGNORE INTO fiber_label_translation (fiber_code, label_lang, name) SELECT f.code, 'nl', 'POLYESTER' FROM fiber f WHERE f.code = 'POL';
INSERT IGNORE INTO fiber_label_translation (fiber_code, label_lang, name) SELECT f.code, 'pl', 'POLIESTER' FROM fiber f WHERE f.code = 'POL';
INSERT IGNORE INTO fiber_label_translation (fiber_code, label_lang, name) SELECT f.code, 'cn', '聚酯纤维' FROM fiber f WHERE f.code = 'POL';
INSERT IGNORE INTO fiber_label_translation (fiber_code, label_lang, name) SELECT f.code, 'jp', 'ポリエステル' FROM fiber f WHERE f.code = 'POL';
INSERT IGNORE INTO fiber_label_translation (fiber_code, label_lang, name) SELECT f.code, 'en', 'ELASTANE' FROM fiber f WHERE f.code = 'ELS';
INSERT IGNORE INTO fiber_label_translation (fiber_code, label_lang, name) SELECT f.code, 'fr', 'ÉLASTHANNE' FROM fiber f WHERE f.code = 'ELS';
INSERT IGNORE INTO fiber_label_translation (fiber_code, label_lang, name) SELECT f.code, 'de', 'ELASTHAN' FROM fiber f WHERE f.code = 'ELS';
INSERT IGNORE INTO fiber_label_translation (fiber_code, label_lang, name) SELECT f.code, 'it', 'ELASTAN' FROM fiber f WHERE f.code = 'ELS';
INSERT IGNORE INTO fiber_label_translation (fiber_code, label_lang, name) SELECT f.code, 'es', 'ELASTANO' FROM fiber f WHERE f.code = 'ELS';
INSERT IGNORE INTO fiber_label_translation (fiber_code, label_lang, name) SELECT f.code, 'pt', 'ELASTANO' FROM fiber f WHERE f.code = 'ELS';
INSERT IGNORE INTO fiber_label_translation (fiber_code, label_lang, name) SELECT f.code, 'nl', 'ELASTAAN' FROM fiber f WHERE f.code = 'ELS';
INSERT IGNORE INTO fiber_label_translation (fiber_code, label_lang, name) SELECT f.code, 'pl', 'ELASTAN' FROM fiber f WHERE f.code = 'ELS';
INSERT IGNORE INTO fiber_label_translation (fiber_code, label_lang, name) SELECT f.code, 'cn', '氨纶' FROM fiber f WHERE f.code = 'ELS';
INSERT IGNORE INTO fiber_label_translation (fiber_code, label_lang, name) SELECT f.code, 'jp', 'ポリウレタン' FROM fiber f WHERE f.code = 'ELS';
INSERT IGNORE INTO fiber_label_translation (fiber_code, label_lang, name) SELECT f.code, 'en', 'ELASTANE' FROM fiber f WHERE f.code = 'ELA';
INSERT IGNORE INTO fiber_label_translation (fiber_code, label_lang, name) SELECT f.code, 'fr', 'ÉLASTHANNE' FROM fiber f WHERE f.code = 'ELA';
INSERT IGNORE INTO fiber_label_translation (fiber_code, label_lang, name) SELECT f.code, 'de', 'ELASTHAN' FROM fiber f WHERE f.code = 'ELA';
INSERT IGNORE INTO fiber_label_translation (fiber_code, label_lang, name) SELECT f.code, 'it', 'ELASTAN' FROM fiber f WHERE f.code = 'ELA';
INSERT IGNORE INTO fiber_label_translation (fiber_code, label_lang, name) SELECT f.code, 'es', 'ELASTANO' FROM fiber f WHERE f.code = 'ELA';
INSERT IGNORE INTO fiber_label_translation (fiber_code, label_lang, name) SELECT f.code, 'pt', 'ELASTANO' FROM fiber f WHERE f.code = 'ELA';
INSERT IGNORE INTO fiber_label_translation (fiber_code, label_lang, name) SELECT f.code, 'nl', 'ELASTAAN' FROM fiber f WHERE f.code = 'ELA';
INSERT IGNORE INTO fiber_label_translation (fiber_code, label_lang, name) SELECT f.code, 'pl', 'ELASTAN' FROM fiber f WHERE f.code = 'ELA';
INSERT IGNORE INTO fiber_label_translation (fiber_code, label_lang, name) SELECT f.code, 'cn', '氨纶' FROM fiber f WHERE f.code = 'ELA';
INSERT IGNORE INTO fiber_label_translation (fiber_code, label_lang, name) SELECT f.code, 'jp', 'ポリウレタン' FROM fiber f WHERE f.code = 'ELA';
INSERT IGNORE INTO fiber_label_translation (fiber_code, label_lang, name) SELECT f.code, 'en', 'VISCOSE' FROM fiber f WHERE f.code = 'VIS';
INSERT IGNORE INTO fiber_label_translation (fiber_code, label_lang, name) SELECT f.code, 'fr', 'VISCOSE' FROM fiber f WHERE f.code = 'VIS';
INSERT IGNORE INTO fiber_label_translation (fiber_code, label_lang, name) SELECT f.code, 'de', 'VISKOSE' FROM fiber f WHERE f.code = 'VIS';
INSERT IGNORE INTO fiber_label_translation (fiber_code, label_lang, name) SELECT f.code, 'it', 'VISCOSA' FROM fiber f WHERE f.code = 'VIS';
INSERT IGNORE INTO fiber_label_translation (fiber_code, label_lang, name) SELECT f.code, 'es', 'VISCOSA' FROM fiber f WHERE f.code = 'VIS';
INSERT IGNORE INTO fiber_label_translation (fiber_code, label_lang, name) SELECT f.code, 'pt', 'VISCOSE' FROM fiber f WHERE f.code = 'VIS';
INSERT IGNORE INTO fiber_label_translation (fiber_code, label_lang, name) SELECT f.code, 'nl', 'VISCOSE' FROM fiber f WHERE f.code = 'VIS';
INSERT IGNORE INTO fiber_label_translation (fiber_code, label_lang, name) SELECT f.code, 'pl', 'WISKOZA' FROM fiber f WHERE f.code = 'VIS';
INSERT IGNORE INTO fiber_label_translation (fiber_code, label_lang, name) SELECT f.code, 'cn', '粘纤' FROM fiber f WHERE f.code = 'VIS';
INSERT IGNORE INTO fiber_label_translation (fiber_code, label_lang, name) SELECT f.code, 'jp', 'レーヨン' FROM fiber f WHERE f.code = 'VIS';
INSERT IGNORE INTO fiber_label_translation (fiber_code, label_lang, name) SELECT f.code, 'en', 'SILK' FROM fiber f WHERE f.code = 'SLK';
INSERT IGNORE INTO fiber_label_translation (fiber_code, label_lang, name) SELECT f.code, 'fr', 'SOIE' FROM fiber f WHERE f.code = 'SLK';
INSERT IGNORE INTO fiber_label_translation (fiber_code, label_lang, name) SELECT f.code, 'de', 'SEIDE' FROM fiber f WHERE f.code = 'SLK';
INSERT IGNORE INTO fiber_label_translation (fiber_code, label_lang, name) SELECT f.code, 'it', 'SETA' FROM fiber f WHERE f.code = 'SLK';
INSERT IGNORE INTO fiber_label_translation (fiber_code, label_lang, name) SELECT f.code, 'es', 'SEDA' FROM fiber f WHERE f.code = 'SLK';
INSERT IGNORE INTO fiber_label_translation (fiber_code, label_lang, name) SELECT f.code, 'pt', 'SEDA' FROM fiber f WHERE f.code = 'SLK';
INSERT IGNORE INTO fiber_label_translation (fiber_code, label_lang, name) SELECT f.code, 'nl', 'ZIJDE' FROM fiber f WHERE f.code = 'SLK';
INSERT IGNORE INTO fiber_label_translation (fiber_code, label_lang, name) SELECT f.code, 'pl', 'JEDWAB' FROM fiber f WHERE f.code = 'SLK';
INSERT IGNORE INTO fiber_label_translation (fiber_code, label_lang, name) SELECT f.code, 'cn', '桑蚕丝' FROM fiber f WHERE f.code = 'SLK';
INSERT IGNORE INTO fiber_label_translation (fiber_code, label_lang, name) SELECT f.code, 'jp', '絹' FROM fiber f WHERE f.code = 'SLK';
INSERT IGNORE INTO fiber_label_translation (fiber_code, label_lang, name) SELECT f.code, 'en', 'LINEN' FROM fiber f WHERE f.code = 'LIN';
INSERT IGNORE INTO fiber_label_translation (fiber_code, label_lang, name) SELECT f.code, 'fr', 'LIN' FROM fiber f WHERE f.code = 'LIN';
INSERT IGNORE INTO fiber_label_translation (fiber_code, label_lang, name) SELECT f.code, 'de', 'LEINEN' FROM fiber f WHERE f.code = 'LIN';
INSERT IGNORE INTO fiber_label_translation (fiber_code, label_lang, name) SELECT f.code, 'it', 'LINO' FROM fiber f WHERE f.code = 'LIN';
INSERT IGNORE INTO fiber_label_translation (fiber_code, label_lang, name) SELECT f.code, 'es', 'LINO' FROM fiber f WHERE f.code = 'LIN';
INSERT IGNORE INTO fiber_label_translation (fiber_code, label_lang, name) SELECT f.code, 'pt', 'LINHO' FROM fiber f WHERE f.code = 'LIN';
INSERT IGNORE INTO fiber_label_translation (fiber_code, label_lang, name) SELECT f.code, 'nl', 'LINNEN' FROM fiber f WHERE f.code = 'LIN';
INSERT IGNORE INTO fiber_label_translation (fiber_code, label_lang, name) SELECT f.code, 'pl', 'LEN' FROM fiber f WHERE f.code = 'LIN';
INSERT IGNORE INTO fiber_label_translation (fiber_code, label_lang, name) SELECT f.code, 'cn', '亚麻' FROM fiber f WHERE f.code = 'LIN';
INSERT IGNORE INTO fiber_label_translation (fiber_code, label_lang, name) SELECT f.code, 'jp', '麻' FROM fiber f WHERE f.code = 'LIN';
INSERT IGNORE INTO fiber_label_translation (fiber_code, label_lang, name) SELECT f.code, 'en', 'POLYAMIDE' FROM fiber f WHERE f.code = 'NYL';
INSERT IGNORE INTO fiber_label_translation (fiber_code, label_lang, name) SELECT f.code, 'fr', 'POLYAMIDE' FROM fiber f WHERE f.code = 'NYL';
INSERT IGNORE INTO fiber_label_translation (fiber_code, label_lang, name) SELECT f.code, 'de', 'POLYAMID' FROM fiber f WHERE f.code = 'NYL';
INSERT IGNORE INTO fiber_label_translation (fiber_code, label_lang, name) SELECT f.code, 'it', 'POLIAMMIDE' FROM fiber f WHERE f.code = 'NYL';
INSERT IGNORE INTO fiber_label_translation (fiber_code, label_lang, name) SELECT f.code, 'es', 'POLIAMIDA' FROM fiber f WHERE f.code = 'NYL';
INSERT IGNORE INTO fiber_label_translation (fiber_code, label_lang, name) SELECT f.code, 'pt', 'POLIAMIDA' FROM fiber f WHERE f.code = 'NYL';
INSERT IGNORE INTO fiber_label_translation (fiber_code, label_lang, name) SELECT f.code, 'nl', 'POLYAMIDE' FROM fiber f WHERE f.code = 'NYL';
INSERT IGNORE INTO fiber_label_translation (fiber_code, label_lang, name) SELECT f.code, 'pl', 'POLIAMID' FROM fiber f WHERE f.code = 'NYL';
INSERT IGNORE INTO fiber_label_translation (fiber_code, label_lang, name) SELECT f.code, 'cn', '锦纶' FROM fiber f WHERE f.code = 'NYL';
INSERT IGNORE INTO fiber_label_translation (fiber_code, label_lang, name) SELECT f.code, 'jp', 'ポリアミド' FROM fiber f WHERE f.code = 'NYL';
INSERT IGNORE INTO fiber_label_translation (fiber_code, label_lang, name) SELECT f.code, 'en', 'CASHMERE' FROM fiber f WHERE f.code = 'CSH';
INSERT IGNORE INTO fiber_label_translation (fiber_code, label_lang, name) SELECT f.code, 'fr', 'CACHEMIRE' FROM fiber f WHERE f.code = 'CSH';
INSERT IGNORE INTO fiber_label_translation (fiber_code, label_lang, name) SELECT f.code, 'de', 'KASCHMIR' FROM fiber f WHERE f.code = 'CSH';
INSERT IGNORE INTO fiber_label_translation (fiber_code, label_lang, name) SELECT f.code, 'it', 'CASHMERE' FROM fiber f WHERE f.code = 'CSH';
INSERT IGNORE INTO fiber_label_translation (fiber_code, label_lang, name) SELECT f.code, 'es', 'CACHEMIR' FROM fiber f WHERE f.code = 'CSH';
INSERT IGNORE INTO fiber_label_translation (fiber_code, label_lang, name) SELECT f.code, 'pt', 'CAXEMIRA' FROM fiber f WHERE f.code = 'CSH';
INSERT IGNORE INTO fiber_label_translation (fiber_code, label_lang, name) SELECT f.code, 'nl', 'KASJMIER' FROM fiber f WHERE f.code = 'CSH';
INSERT IGNORE INTO fiber_label_translation (fiber_code, label_lang, name) SELECT f.code, 'pl', 'KASZMIR' FROM fiber f WHERE f.code = 'CSH';
INSERT IGNORE INTO fiber_label_translation (fiber_code, label_lang, name) SELECT f.code, 'cn', '山羊绒' FROM fiber f WHERE f.code = 'CSH';
INSERT IGNORE INTO fiber_label_translation (fiber_code, label_lang, name) SELECT f.code, 'jp', 'カシミヤ' FROM fiber f WHERE f.code = 'CSH';
INSERT IGNORE INTO fiber_label_translation (fiber_code, label_lang, name) SELECT f.code, 'en', 'LEATHER' FROM fiber f WHERE f.code = 'LEA';
INSERT IGNORE INTO fiber_label_translation (fiber_code, label_lang, name) SELECT f.code, 'fr', 'CUIR' FROM fiber f WHERE f.code = 'LEA';
INSERT IGNORE INTO fiber_label_translation (fiber_code, label_lang, name) SELECT f.code, 'de', 'LEDER' FROM fiber f WHERE f.code = 'LEA';
INSERT IGNORE INTO fiber_label_translation (fiber_code, label_lang, name) SELECT f.code, 'it', 'PELLE' FROM fiber f WHERE f.code = 'LEA';
INSERT IGNORE INTO fiber_label_translation (fiber_code, label_lang, name) SELECT f.code, 'es', 'PIEL' FROM fiber f WHERE f.code = 'LEA';
INSERT IGNORE INTO fiber_label_translation (fiber_code, label_lang, name) SELECT f.code, 'pt', 'COURO' FROM fiber f WHERE f.code = 'LEA';
INSERT IGNORE INTO fiber_label_translation (fiber_code, label_lang, name) SELECT f.code, 'nl', 'LEER' FROM fiber f WHERE f.code = 'LEA';
INSERT IGNORE INTO fiber_label_translation (fiber_code, label_lang, name) SELECT f.code, 'pl', 'SKÓRA' FROM fiber f WHERE f.code = 'LEA';
INSERT IGNORE INTO fiber_label_translation (fiber_code, label_lang, name) SELECT f.code, 'cn', '皮革' FROM fiber f WHERE f.code = 'LEA';
INSERT IGNORE INTO fiber_label_translation (fiber_code, label_lang, name) SELECT f.code, 'jp', '革' FROM fiber f WHERE f.code = 'LEA';
UPDATE fiber SET animal_non_textile = 1 WHERE code IN ('LEA');
-- END generated seed

-- +migrate Down
DROP TABLE IF EXISTS fiber_label_translation;

SET @col_exists := (
    SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'fiber'
      AND COLUMN_NAME = 'animal_non_textile'
);
SET @ddl := IF(@col_exists = 1,
    'ALTER TABLE fiber DROP COLUMN animal_non_textile',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;
