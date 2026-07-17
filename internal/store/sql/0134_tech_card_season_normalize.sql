-- +migrate Up

-- SKU redesign task 05: normalize the style's season into validated parts so the SKU generator can
-- take a real SS/FW/PF/RC code + year for colourway-linked (styled) products. Today tech_card.season
-- is free-text ("ss26" / "SS26" / "-" / NULL). We ADD season_code CHAR(2) + season_year SMALLINT as
-- the normalized source of truth and backfill them by parsing the free-text.
--
-- The legacy free-text `season` column is intentionally KEPT: it is load-bearing in the
-- UNIQUE(style_number, season) key and in ~6 store read/write/filter sites. Dropping it (and
-- reworking that unique key) is a PR2 merch-dictionary cleanup; PR1 only needs the validated parts.
--
-- Idempotent: guarded ADD COLUMN / ADD CHECK via information_schema (multi-line PREPARE/EXECUTE/
-- DEALLOCATE — a single line trips 1064 on the managed DSN, see 0124); the backfill only fills
-- rows whose season_code is still NULL, so a re-run is a no-op.

-- 1) Columns.
SET @need_cols := (SELECT COUNT(*) = 0 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card' AND COLUMN_NAME = 'season_code');
SET @sql := IF(@need_cols,
    'ALTER TABLE tech_card
        ADD COLUMN season_code CHAR(2)  NULL,
        ADD COLUMN season_year SMALLINT NULL',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

-- 2) CHECK: season_code must be a valid enum when set (NULL allowed for unset/legacy). Named
-- explicitly so it is never dropped by a positional <table>_chk_<n> name.
SET @need_chk := (SELECT COUNT(*) = 0 FROM information_schema.TABLE_CONSTRAINTS
    WHERE CONSTRAINT_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card'
      AND CONSTRAINT_NAME = 'tech_card_season_code_enum');
SET @sql := IF(@need_chk,
    'ALTER TABLE tech_card ADD CONSTRAINT tech_card_season_code_enum
        CHECK (season_code IS NULL OR season_code IN (''SS'',''FW'',''PF'',''RC''))',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

-- 3) Backfill from free-text: "ss26"/"SS26" -> code SS, year 2026. Values without a valid 2-letter
-- code + 2-digit year ("-", NULL, junk) are left NULL for an operator to fix. Only still-NULL rows.
UPDATE tech_card
    SET season_code = UPPER(LEFT(season, 2)),
        season_year = 2000 + CAST(REGEXP_SUBSTR(season, '[0-9]{2}') AS UNSIGNED)
    WHERE season_code IS NULL
      AND season IS NOT NULL
      AND UPPER(LEFT(season, 2)) IN ('SS', 'FW', 'PF', 'RC')
      AND season REGEXP '[0-9]{2}';
