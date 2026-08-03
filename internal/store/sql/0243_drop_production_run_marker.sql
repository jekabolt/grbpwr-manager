-- +migrate Up

-- production-costing Phase 2 (review cut, plan 14 §5): the marker import surface (0119) shipped
-- write-only — no editor ever existed, the admin client always sent an empty list, and the
-- full-replace on every run save deleted whatever a direct API caller might have written. The table
-- is empty everywhere by construction. Proto message + enum removed (reserved) and all Go
-- read/write paths deleted in this same change-set; run-level marker facts the app DOES use
-- (production_run.marker_efficiency_pct / marker_notes) are untouched.
DROP TABLE IF EXISTS production_run_marker;

-- +migrate Down

SELECT 1;
