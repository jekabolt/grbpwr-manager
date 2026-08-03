-- +migrate Up

-- production-costing Phase 3 — the CONTRACT half of Phase 2's expand/contract (adversarial review
-- #7): the marker import surface (0119) shipped write-only — no editor ever existed, the admin
-- client always sent an empty list, and the full-replace on every run save deleted whatever a
-- direct API caller might have written. The table is empty everywhere by construction. Phase 2
-- removed the proto message + enum (reserved) and every Go read/write path; the DROP had to wait
-- one deploy because the then-serving binary still full-replaced the table on run save. Run-level
-- marker facts the app DOES use (production_run.marker_efficiency_pct / marker_notes) are untouched.
DROP TABLE IF EXISTS production_run_marker;

-- +migrate Down

SELECT 1;
