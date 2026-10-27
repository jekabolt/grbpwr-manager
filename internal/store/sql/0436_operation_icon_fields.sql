-- +migrate Up

-- ICON FIELDS OF A STEP (SPEC-B1, seam-icons wave). Three columns on tech_card_operation and one on
-- material_hardware_attr; nothing else.
--
--   tech_card_operation.seam_kind     WHICH seam inside seam_class (closed Go list entity.SeamKindTokens)
--   tech_card_operation.work_variant  execution inside one work (closed Go map entity.WorkVariants)
--   tech_card_operation.button_holes  2 | 4 holes of the button this step sews on (button_attach only)
--   material_hardware_attr.button_holes  2 | 4 holes of the button ARTICLE (BOM line), next to diameter_mm
--
-- ADDITIVE ONLY. Every column is born NULL on every existing row: no DEFAULT (NULL = «not stated», and
-- a default would erase the difference between «the technologist said» and «someone said for him»), no
-- backfill (seam_class → seam_kind is not 1:1; a guess would be laundering and would move the digest of
-- signed cards), no CHECK / FK / index. The vocabularies are closed in Go and grow with a client deploy
-- (a new kind always needs a new drawing), so a REGEXP CHECK would only buy a table COPY per new token.
--
-- ZERO WAVE OF STALE SIGNATURES: the digest pairs "seam_kind" (stitching tail), "work_variant" (work
-- tail) and "button_holes" (fastening tail) are emitted only for a valid value, so a NULL row hashes
-- byte-for-byte as before. The frozen golden hexes in internal/dto prove it.
--
-- CHARSET AND COLLATION ARE SPELLED OUT on the two token columns: tech_card_operation lives in utf8mb3
-- on prod, and an inherited collation would make the byte comparison the contract promises depend on
-- the table default. utf8mb4_bin is what operation_work.token and the step `work` column use (0330).
--
-- ALGORITHM=INSTANT IS EXPLICIT: metadata-only, no table copy, no history read. If MySQL could not do
-- it instantly the ALTER fails loudly instead of silently copying the table under the five-minute
-- migration ceiling (internal/store/store.go). Four separate ALTERs = four row versions; the last
-- rebuild of tech_card_operation was 0422 (ADD CHECK, COPY), so the 64-version ceiling is far away.
--
-- IDEMPOTENCY: MySQL auto-commits DDL, so a crash halfway leaves the schema half-applied with no
-- gorp_migrations row and the next start reruns the file FROM THE TOP. Each column has its own
-- information_schema gate; PREPARE / EXECUTE / DEALLOCATE one statement per line.
--
-- ROLLBACK: a binary rollback after real values exist would full-replace operations without these
-- columns and silently NULL them (and a card signed with a kind would hash differently). Once data
-- exists this wave is roll-forward only; the columns stay forever.

-- 1. tech_card_operation.seam_kind. Width 32 = the widest token any closed list of the card uses.
SET @op_seam_kind_col := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_operation'
      AND COLUMN_NAME = 'seam_kind');
SET @ddl := IF(@op_seam_kind_col = 0,
    'ALTER TABLE tech_card_operation
        ADD COLUMN seam_kind VARCHAR(32) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NULL COMMENT ''seam construction inside seam_class: token of entity.SeamKindTokens; NULL = not stated, never derived by the server'',
        ALGORITHM=INSTANT',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- 2. tech_card_operation.work_variant. Legal only together with `work`; the pair is checked in dto.
SET @op_work_variant_col := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_operation'
      AND COLUMN_NAME = 'work_variant');
SET @ddl := IF(@op_work_variant_col = 0,
    'ALTER TABLE tech_card_operation
        ADD COLUMN work_variant VARCHAR(24) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NULL COMMENT ''execution inside the step work: token of entity.WorkVariants[work]; NULL = not stated'',
        ALGORITHM=INSTANT',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- 3. tech_card_operation.button_holes. 2 | 4; the server never writes 0 (0 on the wire = NULL).
SET @op_button_holes_col := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_operation'
      AND COLUMN_NAME = 'button_holes');
SET @ddl := IF(@op_button_holes_col = 0,
    'ALTER TABLE tech_card_operation
        ADD COLUMN button_holes TINYINT UNSIGNED NULL COMMENT ''holes of the button this button_attach step sews on: 2 or 4; NULL = not stated'',
        ALGORITHM=INSTANT',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- 4. material_hardware_attr.button_holes — a typed column next to diameter_mm, like every other
--    hardware attribute (0157). 2 | 4; NULL = not stated.
SET @hw_button_holes_col := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'material_hardware_attr'
      AND COLUMN_NAME = 'button_holes');
SET @ddl := IF(@hw_button_holes_col = 0,
    'ALTER TABLE material_hardware_attr
        ADD COLUMN button_holes TINYINT UNSIGNED NULL COMMENT ''holes of a button article: 2 or 4; NULL = not stated'',
        ALGORITHM=INSTANT',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- +migrate Down
--
-- Down drops exactly what Up added, in reverse order. The values are lost with the columns; Down is a
-- development tool (up → down → up on a throwaway database), not a time machine for prod.

-- D1. material_hardware_attr.button_holes.
SET @hw_button_holes_back := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'material_hardware_attr'
      AND COLUMN_NAME = 'button_holes');
SET @ddl := IF(@hw_button_holes_back = 1,
    'ALTER TABLE material_hardware_attr DROP COLUMN button_holes',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- D2. tech_card_operation.button_holes.
SET @op_button_holes_back := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_operation'
      AND COLUMN_NAME = 'button_holes');
SET @ddl := IF(@op_button_holes_back = 1,
    'ALTER TABLE tech_card_operation DROP COLUMN button_holes',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- D3. tech_card_operation.work_variant.
SET @op_work_variant_back := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_operation'
      AND COLUMN_NAME = 'work_variant');
SET @ddl := IF(@op_work_variant_back = 1,
    'ALTER TABLE tech_card_operation DROP COLUMN work_variant',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- D4. tech_card_operation.seam_kind.
SET @op_seam_kind_back := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_operation'
      AND COLUMN_NAME = 'seam_kind');
SET @ddl := IF(@op_seam_kind_back = 1,
    'ALTER TABLE tech_card_operation DROP COLUMN seam_kind',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;
