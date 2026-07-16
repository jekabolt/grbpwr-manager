-- +migrate Up

-- Replace the free-text tech-card season source with one atomic nullable pair:
-- season_code (SS/FW/PF/RC) + season_year (2000..2099). The old season column remains only because
-- existing uniqueness/filtering reads use it; it is now a canonical derived label (SS26), protected
-- by a CHECK so neither the API nor direct SQL can persist an independent free-text value.
--
-- Production preflight (2026-07-16, read-only): 5 cards are SS26, 3 are NULL, and no product-linked
-- card has an invalid season. Unknown legacy values in other environments become explicitly unset;
-- no code/year is guessed from arbitrary text and styled SKU minting rejects an unset pair.

-- Complete a legacy pair only when the entire trimmed label is the old exact CODEYY form.
UPDATE tech_card
SET season_code = UPPER(LEFT(TRIM(season), 2)),
    season_year = 2000 + CAST(RIGHT(TRIM(season), 2) AS UNSIGNED)
WHERE (season_code IS NULL OR season_year IS NULL)
  AND TRIM(season) REGEXP '^(SS|FW|PF|RC)[0-9]{2}$';

-- A partial or invalid normalized pair is not a season. Clear it rather than inventing an SKU fact.
UPDATE tech_card
SET season_code = NULL,
    season_year = NULL
WHERE (season_code IS NULL AND season_year IS NOT NULL)
   OR (season_code IS NOT NULL AND season_year IS NULL)
   OR (season_code IS NOT NULL AND season_code NOT IN ('SS', 'FW', 'PF', 'RC'))
   OR (season_year IS NOT NULL AND (season_year < 2000 OR season_year > 2099));

-- Canonical DB-only label used by the existing UNIQUE(style_number, season) index/read models.
UPDATE tech_card
SET season = CASE
    WHEN season_code IS NULL THEN NULL
    ELSE CONCAT(season_code, LPAD(MOD(season_year, 100), 2, '0'))
END;

SET @season_label_wide := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card' AND COLUMN_NAME = 'season'
      AND (DATA_TYPE <> 'varchar' OR CHARACTER_MAXIMUM_LENGTH <> 4));
SET @sql := IF(@season_label_wide > 0,
    'ALTER TABLE tech_card MODIFY COLUMN season VARCHAR(4) NULL COMMENT ''derived canonical SKU season label''',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

SET @need_season_atomic_chk := (SELECT COUNT(*) = 0 FROM information_schema.TABLE_CONSTRAINTS
    WHERE CONSTRAINT_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card'
      AND CONSTRAINT_NAME = 'tech_card_season_atomic');
SET @sql := IF(@need_season_atomic_chk,
    'ALTER TABLE tech_card ADD CONSTRAINT tech_card_season_atomic CHECK (
        (season_code IS NULL AND season_year IS NULL AND season IS NULL)
        OR
        (season_code IS NOT NULL AND season_year IS NOT NULL AND season IS NOT NULL
         AND season_code IN (''SS'',''FW'',''PF'',''RC'')
         AND season_year BETWEEN 2000 AND 2099
         AND season = CONCAT(season_code, LPAD(MOD(season_year, 100), 2, ''0'')))
    )',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;
