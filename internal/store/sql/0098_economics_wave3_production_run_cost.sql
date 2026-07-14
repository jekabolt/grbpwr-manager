-- +migrate Up
-- Economics audit — Wave 3 / task 09 (phase 2): actual production-run costs → plan/fact.
-- Phase 1 froze the planned unit cost on the run; this records the ACTUAL cost articles incurred
-- for the batch (materials at real purchase price, real CMT invoice, hardware, packaging, freight,
-- duty, other) so cost is measured, not eternally assumed. amount is in the purchase currency;
-- amount_base folds it to the base currency (via costing_fx_rate, or entered manually) so the run
-- totals and plan/fact are a plain SUM with no read-time FX. actual_unit_cost, defect_pct_actual
-- and the plan/fact deltas are computed on read from these rows + the phase-1 size grid.

CREATE TABLE IF NOT EXISTS production_run_cost (
  id INT PRIMARY KEY AUTO_INCREMENT,
  run_id INT NOT NULL,
  kind VARCHAR(16) NOT NULL
    CHECK (kind REGEXP '^(materials|cmt|hardware|packaging|logistics|duty|other)$'),
  description VARCHAR(255) NULL,
  amount DECIMAL(12, 2) NOT NULL CHECK (amount >= 0),
  currency CHAR(3) NOT NULL COMMENT 'ISO 4217, uppercase',
  amount_base DECIMAL(12, 2) NULL COMMENT 'amount in base currency (folded via costing_fx_rate or manual)',
  incurred_at DATE NULL,
  created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  FOREIGN KEY (run_id) REFERENCES production_run(id) ON DELETE CASCADE,
  INDEX idx_prc_run (run_id)
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4 COMMENT 'Actual cost articles incurred for a production run';

-- +migrate Down
DROP TABLE IF EXISTS production_run_cost;
