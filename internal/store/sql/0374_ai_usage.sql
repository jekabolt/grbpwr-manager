-- AI providers wave (27.09), commit A "shadow" — THE LEDGER: one row per PHYSICAL provider call, and
-- beside it the provider's own daily number to reconcile against.
--
-- A ROW BEFORE EVERY CALL (02-PLAN A1). The row is inserted with status 'dispatching' BEFORE the request
-- leaves and finalised after it returns, both as short writes outside the network call (the
-- StartAttempt / FinishAttempt shape). A process that dies in between leaves a 'dispatching' row, and
-- the sweeper turns rows older than 15 minutes into 'unknown': a call that may have cost money is never
-- simply absent. An async submit (fal, meshy) finalises as 'accepted' with no price; the collect that
-- delivers prices THAT row, found by (run_id, attempt_no, call_no).
--
-- WHO AND WHEN. actor is the JWT username (the same string as design_run.author; 'system' for background
-- work), actor_admin_id the admins.id when the interceptor could resolve it. occurred_at is UTC;
-- day_local is the calendar day of occurred_at in design_settings.budget_timezone, COMPUTED IN GO at
-- write time (the 0344 rule: the day is an answer of the organisation, not of whichever MySQL session
-- wrote the row). Old rows never move when the zone changes; the report reads day_local BETWEEN.
--
-- MONEY. cost_usd NULL means UNKNOWN and is never read as zero: the report counts such rows as
-- "unpriced" instead of summing them away. cost_source names where the number came from (provider,
-- units, table, estimate, free, none) and price_version names the code price table that produced a
-- 'table' price. provider_key is the BILLING transport (recraft through openrouter is 'openrouter').
--
-- VOCABULARIES ARE VARCHAR, CLOSED IN GO (entity.AICall*, entity.AICost*), no ENUM and no CHECK.
--
-- NO FOREIGN KEY TO design_run. A money row outlives the card and the run it was spent on (the 0344
-- argument): deleting a card must not erase what its pictures cost. run_id / attempt_no / call_no are
-- NULL on chat rows, and MySQL lets NULLs repeat under a UNIQUE key, so uq_ai_usage_call binds only
-- the design calls, which is exactly what makes the collect's update addressable and idempotent.
--
-- INDEXES follow the readers: the report's period by provider, by actor (name and id) and by purpose,
-- the drill-down keyset (occurred_at, id), and (status, occurred_at) for the sweeper's scan.
--
-- ai_provider_cost_daily is "their number" (D-06): what the provider's own cost API says we spent that
-- day, fetched by the reconciliation worker where an admin key exists. It never replaces the ledger;
-- the report shows it beside ours.
--
-- IDEMPOTENT: IF NOT EXISTS everywhere, no seeds, no ALTER on an existing table.

-- +migrate Up

CREATE TABLE IF NOT EXISTS ai_usage_event (
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
    occurred_at DATETIME(6) NOT NULL COMMENT 'UTC, when the call was dispatched',
    day_local DATE NOT NULL COMMENT 'day of occurred_at in design_settings.budget_timezone, computed in Go at write',
    provider_key VARCHAR(32) NOT NULL COMMENT 'the billing transport',
    model VARCHAR(128) NOT NULL DEFAULT '' COMMENT 'the answering model when the provider reports it, else the requested one',
    purpose VARCHAR(48) NOT NULL COMMENT 'entity.AIPurposes',
    actor VARCHAR(255) NOT NULL DEFAULT '' COMMENT 'JWT username; system for background work',
    actor_admin_id INT UNSIGNED NULL COMMENT 'admins.id when resolved; no FK, the row outlives the account',
    run_id INT UNSIGNED NULL COMMENT 'design_run.id for design calls; no FK',
    attempt_no TINYINT UNSIGNED NULL COMMENT 'design_run_attempt.attempt_no',
    call_no TINYINT UNSIGNED NOT NULL DEFAULT 1 COMMENT 'transport invocation inside the attempt, from 1',
    request_id VARCHAR(128) NULL COMMENT 'provider request or generation id',
    status VARCHAR(24) NOT NULL COMMENT 'dispatching|ok|free|failed|charged_failed|accepted|unknown; closed in Go',
    error_code VARCHAR(64) NULL,
    http_status SMALLINT NULL,
    engaged TINYINT(1) NULL COMMENT '1 = the request was written, money may have moved',
    fallback_from VARCHAR(32) NULL COMMENT 'the provider this call fell back from',
    prompt_tokens INT NULL,
    completion_tokens INT NULL,
    cached_tokens INT NULL,
    reasoning_tokens INT NULL,
    units DECIMAL(12,4) NULL COMMENT 'billable units or credits when the provider bills in units',
    unit VARCHAR(16) NULL,
    cost_usd DECIMAL(12,6) NULL COMMENT 'NULL = unknown, never zero',
    cost_source VARCHAR(16) NOT NULL DEFAULT 'none' COMMENT 'provider|units|table|estimate|free|none; closed in Go',
    price_version VARCHAR(16) NULL COMMENT 'aiprov/pricing Version behind a table price',
    latency_ms INT NULL,
    finished_at DATETIME(6) NULL COMMENT 'UTC, when the row left dispatching',
    KEY idx_ai_usage_day_provider (day_local, provider_key),
    KEY idx_ai_usage_actor_day (actor, day_local),
    KEY idx_ai_usage_actor_id_day (actor_admin_id, day_local),
    KEY idx_ai_usage_purpose_day (purpose, day_local),
    KEY idx_ai_usage_occurred (occurred_at, id),
    KEY idx_ai_usage_status (status, occurred_at),
    UNIQUE KEY uq_ai_usage_call (run_id, attempt_no, call_no)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT 'AI ledger: one row per physical provider call, written before the call';

CREATE TABLE IF NOT EXISTS ai_provider_cost_daily (
    provider_key VARCHAR(32) NOT NULL,
    day DATE NOT NULL COMMENT 'the day as the provider cost API buckets it',
    amount_usd DECIMAL(12,6) NOT NULL,
    currency CHAR(3) NOT NULL DEFAULT 'USD',
    fetched_at DATETIME(6) NOT NULL COMMENT 'UTC, when the reconciliation worker read it',
    PRIMARY KEY (provider_key, day)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT 'Their number: daily spend as the provider own cost API reports it';

-- +migrate Down

DROP TABLE IF EXISTS ai_provider_cost_daily;
DROP TABLE IF EXISTS ai_usage_event;
