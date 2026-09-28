-- AI providers wave (28.09), commit E, B-28/B-29 — THE ZONE A PROVIDER'S OWN DAY IS COUNTED IN (D-17).
--
-- ai_provider_cost_daily.day (0374) is the day AS THE PROVIDER BUCKETS IT, and that is not our day. The
-- ledger counts days in design_settings.budget_timezone (Europe/Warsaw); the cost APIs with day buckets
-- bucket by UTC day: OpenAI /v1/organization/costs and Anthropic cost_report with bucket_width=1d, fal
-- /v1/models/usage per day. OpenRouter has no per-day history at all, only a cumulative counter
-- (data.usage on /api/v1/key). Since B-30 (0380) the worker makes its days itself, exactly in OUR
-- days: the difference of two readings taken 30 s after consecutive LOCAL midnights, written with
-- bucket_tz = design_settings.budget_timezone. (Commit E first wrote usage_daily, the running total of
-- the CURRENT UTC day, as a UTC row; the rows it wrote keep bucket_tz 'UTC' and say so.)
--
-- NOT RE-BUCKETED. Moving their number onto our days would need hourly buckets, which not every API has,
-- and a synthetic split would present a guess as their number. The row keeps the provider's day and says
-- which zone it is a day of; the report returns that zone per provider (their_bucket_tz) and the panel
-- labels each provider's number «utc days» or «local days» by it, with the hint that a UTC day can
-- differ from a local day by up to two hours at each end. The column exists to catch DRIFT, which shows
-- over a week or a month.
--
-- VARCHAR(32), NOT NULL, DEFAULT 'UTC': an IANA zone name, closed in Go (the worker writes 'UTC', and the
-- budget zone for OpenRouter's days);
-- the default is also the truth for every row written before this file — each was a UTC bucket. No
-- ENUM, no CHECK (the house rule for vocabularies and for existing tables).
--
-- COST. ADD COLUMN at the END of the table is INSTANT (MySQL 8.0.12+): no row is rewritten.
--
-- THE OLD BINARY ON THE NEW SCHEMA IS SAFE: its upsert does not name bucket_tz, so its rows take the
-- default, which is what they are; its report reads no such column.
--
-- IDEMPOTENT: the ALTER runs only when information_schema says the column is missing, so a re-run after
-- a half-applied boot is a no-op.

-- +migrate Up

SET @cd_bucket_tz := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'ai_provider_cost_daily'
      AND COLUMN_NAME = 'bucket_tz');
SET @ddl := IF(@cd_bucket_tz = 0,
    'ALTER TABLE ai_provider_cost_daily
        ADD COLUMN bucket_tz VARCHAR(32) NOT NULL DEFAULT ''UTC''
            COMMENT ''the zone day is a day of, as the provider buckets it (D-17); UTC for every cost API today''',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- +migrate Down
--
-- Rolls back only together with the code: UpsertCostDaily names the column, and without it every
-- reconciliation write would fail. Prod and beta only migrate up; Down is the developer's path.

SET @cd_bucket_tz_down := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'ai_provider_cost_daily'
      AND COLUMN_NAME = 'bucket_tz');
SET @ddl := IF(@cd_bucket_tz_down = 1,
    'ALTER TABLE ai_provider_cost_daily DROP COLUMN bucket_tz',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;
