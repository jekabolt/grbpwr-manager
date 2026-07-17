-- +migrate Up

-- PR6 phase 3 (POM / size chart to the style), step 2 of 2: drop the per-colourway size_measurement
-- table now that the catalogue size chart lives on the style (tech_card_size_measurement, 0141) and the
-- product read/write have been repointed to it.
--
-- Contract is forbidden unless the persisted 0141 chart reconciliation report is empty — re-verified
-- here (mirroring 0140/0139), so a manual/catch-up run can never silently discard the legacy charts
-- while a value conflict is unresolved. If 0141 stopped on a conflict, this INSERT violates the named
-- CHECK (conflict_count = 0) and halts before the drop. The report/provenance tables are intentionally
-- retained for audit/rollback evidence. Idempotent: DROP TABLE IF EXISTS; the guard re-check is a
-- deterministic recount.

DELETE FROM migration_0141_chart_guard;
INSERT INTO migration_0141_chart_guard (singleton, conflict_count)
SELECT 1, COUNT(*) FROM migration_0141_chart_conflict;

DROP TABLE IF EXISTS size_measurement;
