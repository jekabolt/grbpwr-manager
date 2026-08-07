-- +migrate Up

-- НАЗНАЧЕНИЕ (purpose) of a roll-goods BOM line, plus the "семпловая" flag.
--
-- WHY A NEW COLUMN AND NOT A NEW SECTION. `section` is load-bearing: it drives the wastage
-- gross-up, the product composition derive, and which lines a раскладка may bind to (see
-- rollGoodsSectionList). A pocket-bag fabric, a contrast fabric and a mesh second layer are all
-- genuinely section='fabric' — they are cloth sold by length and cut on the same marker — and they
-- differ only in what the garment uses them FOR. Splitting them into sections would silently change
-- every one of those three behaviours; putting the distinction on its own axis changes none.
--
-- WHY THE LIST IS CLOSED, AND WHY `other` STILL CARRIES A NOTE. The whole point of the field is to
-- GROUP lines ("the subset of fabrics that are lining"). A free-text purpose is not a group — two
-- operators would write "карманка" and "мешковина кармана" and the grouping would quietly stop
-- working. So the vocabulary is fixed at eight values and the escape hatch is a SEPARATE note
-- column that is only legal on `other` — chk_bom_item_purpose_note is what keeps the note from
-- becoming a shadow purpose on `main`, which is exactly how a closed list dissolves in practice.
--
-- WHY is_sample IS A FLAG AND NOT A NINTH PURPOSE. A sample is sewn from a sample MAIN fabric AND a
-- sample LINING. As a purpose value both lines would collapse to "sample" and lose the role that
-- makes them useful; as a flag each keeps its purpose and merely says which yardage it is.
--
-- WHY NOTHING IS BACKFILLED. purpose is NULL on every existing line, deliberately: section='fabric'
-- is precisely where pocket-bag, contrast and mesh hide today, so any rule that guessed would label
-- them "основной материал" confidently and wrongly. NULL reads as "not sorted yet" and the operator
-- sorts by hand; a wrong value reads as a fact and would be believed.
--
-- Idempotent per CLAUDE.md: MySQL DDL auto-commits, so a mid-file failure leaves the schema
-- half-applied with no gorp_migrations row and the next boot re-runs this file from the top. The
-- three columns and both constraints ride ONE ALTER (MySQL 8 atomic DDL: all or nothing), guarded on
-- the presence of `purpose`, so a re-run is a no-op rather than a "duplicate column" halt.
--
-- ДВЕ ЛОВУШКИ SQL, обе выглядят правильно и обе молчат:
--
-- 1. `purpose = 'other'` при purpose IS NULL даёт NULL, а CHECK со значением NULL MySQL считает
--    ВЫПОЛНЕННЫМ. То есть очевидная запись `purpose_note IS NULL OR purpose = 'other'` ловит дырку
--    вида purpose='main' и пропускает дырку вида purpose IS NULL — а NULL это состояние КАЖДОЙ
--    строки до этой миграции и каждой ещё не разложенной. Нужен NULL-безопасный `<=>`, иначе
--    примечание становится теневым назначением ровно там, где назначения ещё нет.
-- 2. REGEXP под utf8mb3_general_ci регистронезависим, так что 'MAIN' прошёл бы и не попал бы потом
--    ни в одну группу. `REGEXP BINARY` тут НЕ подходит: под utf8mb4_0900_ai_ci (так подключаются
--    контейнерные тесты) MySQL отвечает 3995 «Character set … cannot be used in conjunction with
--    binary». Портируемо между utf8mb3 прода и utf8mb4 тестов — сравнить байты через STRCMP с
--    LOWER: значение должно совпадать со своей нижнерегистровой формой ПОБАЙТОВО.

SET @col_exists := (
    SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'tech_card_bom_item'
      AND COLUMN_NAME = 'purpose'
);
SET @ddl := IF(@col_exists = 0,
    'ALTER TABLE tech_card_bom_item ADD COLUMN purpose VARCHAR(24) NULL COMMENT ''назначение of a roll-goods line; NULL = not sorted yet (never guessed)'' AFTER section, ADD COLUMN purpose_note VARCHAR(255) NULL COMMENT ''free-text note, legal only when purpose = other'' AFTER purpose, ADD COLUMN is_sample TINYINT(1) NOT NULL DEFAULT 0 COMMENT ''семпловая: this yardage is what the sample is sewn from'' AFTER purpose_note, ADD CONSTRAINT chk_bom_item_purpose CHECK (purpose IS NULL OR (purpose REGEXP ''^(main|lining|pocketing|interfacing|insulation|contrast|mesh|other)$'' AND STRCMP(CAST(purpose AS BINARY), CAST(LOWER(purpose) AS BINARY)) = 0)), ADD CONSTRAINT chk_bom_item_purpose_note CHECK (purpose_note IS NULL OR purpose <=> ''other'')',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- +migrate Down
SET @col_exists := (
    SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'tech_card_bom_item'
      AND COLUMN_NAME = 'purpose'
);
SET @ddl := IF(@col_exists = 1,
    'ALTER TABLE tech_card_bom_item DROP CONSTRAINT chk_bom_item_purpose_note, DROP CONSTRAINT chk_bom_item_purpose, DROP COLUMN is_sample, DROP COLUMN purpose_note, DROP COLUMN purpose',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;
