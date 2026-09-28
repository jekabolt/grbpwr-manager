-- AI providers wave (28.09), commit F, B-30 — THE BASE OPENROUTER'S DAY IS MEASURED FROM (D-17).
--
-- OpenRouter has no per-day cost history. Its /api/v1/key answers one CUMULATIVE counter, data.usage:
-- what the key has spent since it was issued. A day is therefore the DIFFERENCE of two readings, and
-- D-17 fixes which two: the readings taken right after two consecutive LOCAL midnights
-- (design_settings.budget_timezone, Europe/Warsaw — the ledger's own days). The reconciliation worker
-- takes a reading 30 s after every local midnight, writes reading − base under the day just closed
-- into ai_provider_cost_daily with bucket_tz = the zone, and keeps the reading here as the next day's
-- base; between midnights it re-writes today's running partial (reading − base) every hour.
--
-- WHY A TABLE. The base must outlive the process: every deploy is a restart, and a base held in memory
-- would be lost with the day it was opened for. One row per provider, replaced in place (the primary
-- key is the provider): the worker only ever needs the LAST base, and the days it closed are in
-- ai_provider_cost_daily already.
--
-- WHEN THE BASE IS TAKEN AGAIN, NOTHING IS WRITTEN FOR THAT READING: the first reading ever (nothing to
-- diff against), a counter that went DOWN (a rotated key restarts from zero), a base of another zone
-- than today's, and a midnight reading that never happened (a restart or an outage across midnight:
-- the day before keeps its last hourly partial, and today counts from the new base). A guessed split
-- would present a guess as their number.
--
-- day is the local day this base OPENS: the day whose midnight it was read at, or the day it was taken
-- again on. bucket_tz names the zone of that day; taken_at is UTC. usage_usd DECIMAL(14,6): the
-- counter only grows over a key's life, so it gets two more integer digits than a day's amount_usd
-- (DECIMAL(12,6), 0374); six places, because the difference of two readings is a day's number, and a
-- base rounded coarser than the day it is subtracted from would move that day.
--
-- VOCABULARIES ARE VARCHAR, CLOSED IN GO (entity.AIProvider*; the zone an IANA name the store loads
-- before it writes). No ENUM, no CHECK, no foreign key (the house rule for the AI tables).
--
-- THE OLD BINARY ON THE NEW SCHEMA IS SAFE: it never reads or writes this table.
--
-- IDEMPOTENT: the table is created only IF NOT EXISTS, and nothing is seeded — the first reading writes
-- the first row.

-- +migrate Up

CREATE TABLE IF NOT EXISTS ai_provider_usage_snapshot (
    provider_key VARCHAR(32) NOT NULL PRIMARY KEY,
    usage_usd DECIMAL(14,6) NOT NULL COMMENT 'the cumulative counter as read, USD',
    day DATE NOT NULL COMMENT 'the local day this reading opened (taken at its midnight, or re-taken that day)',
    bucket_tz VARCHAR(32) NOT NULL COMMENT 'the zone day is a day of (design_settings.budget_timezone at the reading)',
    taken_at DATETIME(6) NOT NULL COMMENT 'UTC, when the reading was taken'
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT 'The base a cumulative provider counter is diffed against at each local midnight (D-17)';

-- +migrate Down
--
-- Rolls back only together with the code: the worker reads and writes this table on every OpenRouter
-- reading. Prod and beta only migrate up; Down is the developer's path.

DROP TABLE IF EXISTS ai_provider_usage_snapshot;
