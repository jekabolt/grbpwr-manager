-- +migrate Up

-- production-costing Phase 2 dead-schema drop (plan 01 §1.5 / 13): production_run_size was the
-- flat per-size grid 0097 shipped; 0110 replaced it with production_run_line (colour-model × size)
-- and every Go reference went with it — zero readers or writers since. Verified by grep before this
-- drop; the receipt tables (0231) FK production_run_line, never this one.
DROP TABLE IF EXISTS production_run_size;

-- +migrate Down

-- The table was orphaned data with no readers; recreating an empty shell would only invite drift.
SELECT 1;
