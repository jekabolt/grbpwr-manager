-- FLAT › WORDS — THE PERSON'S OWN WORDS FOR A FLAT (2026-10-07, tmp/plans/flat-consistency M14).
--
-- Owner: «показывай в WORDS только то, что уходит». A flat sends «garment: <class>» from
-- garment_description and NOTHING ELSE of it: that description is seeded by a model brief and edited
-- by people, one string with no author, so its prose never travels to a flat
-- (designgen.FlatWordsCarryDescription). This column holds the lines a PERSON types in FLAT › WORDS
-- under the class line — no model writes it (no ai ✦ on that box, no seeding), so it is human by
-- construction — and a flat sends them as typed (designgen.FlatGarmentNote). Read by flat runs only.
--
-- NULL, NO DEFAULT, NO BACKFILL: every card today has no flat words, and a card with none sends exactly
-- what it sent before. garment_description is not touched — it stays where it lives for the other
-- kinds (render, 3D, recolour).
--
-- Same verbatim protocol as garment_description (0348): a save that does not name the field keeps the
-- column (IF(:flat_words_omitted, …) in the store), "" clears it.
--
-- NO CHECK (ADD CONSTRAINT … CHECK runs as ALGORITHM=COPY and re-validates the whole history inside
-- the deploy's five-minute migration window). A nullable TEXT at the end of tech_card is INSTANT.
--
-- ROLLBACK-SAFE: the previous binary never names the column (explicit column lists on every write;
-- `SELECT *` reads run on the sqlx Unsafe handle, which ignores a column with no struct field).
--
-- Idempotent: guarded by information_schema; PREPARE / EXECUTE / DEALLOCATE each on its own line
-- (prod runs without multiStatements).

-- +migrate Up

SET @tc_flat_words := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card'
      AND COLUMN_NAME = 'flat_words');
SET @ddl := IF(@tc_flat_words = 0,
    'ALTER TABLE tech_card
        ADD COLUMN flat_words TEXT NULL COMMENT ''the person own words for a flat, typed in FLAT WORDS under the class line; never model-written; sent by flat runs only''',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- +migrate Down

SET @tc_flat_words := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card'
      AND COLUMN_NAME = 'flat_words');
SET @ddl := IF(@tc_flat_words = 1, 'ALTER TABLE tech_card DROP COLUMN flat_words', 'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;
