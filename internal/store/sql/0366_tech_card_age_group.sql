-- AGE GROUP — the style's target age group (adult / teen / kids / toddler / baby): a catalogue fact
-- beside target_gender, owned by UpdateStyle (StylePatch.age_group, mask path "age_group"), read
-- back on TechCard.age_group and on the colourway's ColorwayMerchandising.age_group.
--
-- NULL, NO DEFAULT, NO BACKFILL. NULL means "not set", and that is the true state of every style that
-- exists today: nobody has said which age group it is made for. A DEFAULT 'adult' — or a backfill —
-- would put a claim on every row that no person made, and a read could no longer tell "someone
-- picked adult" from "nobody looked". The wire maps NULL to AGE_GROUP_ENUM_UNKNOWN, and a write that
-- does not name age_group (an old client's full replace included) keeps whatever is stored.
--
-- NO CHECK. The vocabulary (entity.ValidAgeGroups) is enforced in Go on the write: UpdateStyle refuses
-- an unknown token with a field-tagged InvalidArgument on `age_group`, which a CHECK could only answer
-- with an unaddressed 3819. And ADD CONSTRAINT ... CHECK runs as ALGORITHM=COPY in MySQL 8 — it would
-- rewrite tech_card whole and re-validate its entire history inside the hard-coded five-minute
-- migration window of the deploy (store.go). The bare nullable ADD COLUMN is INSTANT.
--
-- ROLLBACK-SAFE. The previous binary never names the column: every write it makes to tech_card lists
-- its columns explicitly (techCardHeaderColumns, styleFieldsSet, the archive import), so under it the
-- column simply keeps its value, and its `SELECT * FROM tech_card` reads run on the sqlx Unsafe handle
-- (store.New), which ignores a column with no struct field. Rolling the binary back leaves the column
-- in place and every age group already recorded intact.
--
-- Idempotent: guarded by information_schema; PREPARE / EXECUTE / DEALLOCATE each on its own line
-- (prod runs without multiStatements).

-- +migrate Up

SET @tc_age_group := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card'
      AND COLUMN_NAME = 'age_group');
SET @ddl := IF(@tc_age_group = 0,
    'ALTER TABLE tech_card
        ADD COLUMN age_group VARCHAR(16) NULL COMMENT ''style target age group: adult|teen|kids|toddler|baby (vocabulary validated in Go, no CHECK); NULL = not set''',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- +migrate Down

SET @tc_age_group := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card'
      AND COLUMN_NAME = 'age_group');
SET @ddl := IF(@tc_age_group = 1, 'ALTER TABLE tech_card DROP COLUMN age_group', 'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;
