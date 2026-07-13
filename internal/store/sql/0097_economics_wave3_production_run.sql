-- +migrate Up
-- Economics audit — Wave 3 / task 09 (phase 1): production runs (партии) — the plan side.
-- Production was never modelled: tech_card_size.order_qty is a bare number for scaling costing,
-- the production kanban board holds free-form cards, and defect_percent is an eternal plan with no
-- recorded fact. Without a run aggregate there is no plan/fact cost analysis and stock is topped up
-- with no batch cost. Phase 1 adds the run + its own size grid (distinct from the card's order_qty)
-- and snapshots the planned unit cost at plan time so the run's plan stops drifting when the card is
-- edited. A run may reference an immutable tech_card_release (task 11) as its frozen plan source.
-- Phase 2 (actual costs) and phase 3 (stock/cost integration) follow in later migrations.

CREATE TABLE production_run (
  id INT PRIMARY KEY AUTO_INCREMENT,
  tech_card_id INT NOT NULL,
  release_id INT NULL COMMENT 'optional tech_card_release this run was planned from (task 11)',
  status VARCHAR(16) NOT NULL DEFAULT 'planned'
    CHECK (status REGEXP '^(planned|in_progress|received|closed|cancelled)$'),
  started_at DATETIME NULL,
  received_at DATETIME NULL,
  planned_unit_cost DECIMAL(10, 2) NULL COMMENT 'snapshot of the card/release unit cost at plan time',
  planned_currency CHAR(3) NULL COMMENT 'currency of planned_unit_cost',
  notes TEXT NULL,
  created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  FOREIGN KEY (tech_card_id) REFERENCES tech_card(id),
  FOREIGN KEY (release_id) REFERENCES tech_card_release(id) ON DELETE SET NULL,
  INDEX idx_prun_tech_card (tech_card_id),
  INDEX idx_prun_status (status)
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4 COMMENT 'Production run (партия): a batch produced from a tech card';

CREATE TABLE production_run_size (
  run_id INT NOT NULL,
  size_id INT NOT NULL,
  planned_qty INT NOT NULL DEFAULT 0 CHECK (planned_qty >= 0),
  received_qty INT NULL CHECK (received_qty IS NULL OR received_qty >= 0),
  defect_qty INT NULL CHECK (defect_qty IS NULL OR defect_qty >= 0),
  PRIMARY KEY (run_id, size_id),
  FOREIGN KEY (run_id) REFERENCES production_run(id) ON DELETE CASCADE,
  FOREIGN KEY (size_id) REFERENCES size(id)
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4 COMMENT 'Per-size planned/received/defect quantities of a production run';

-- +migrate Down
DROP TABLE IF EXISTS production_run_size;
DROP TABLE IF EXISTS production_run;
