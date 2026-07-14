-- +migrate Up

-- new-flow NF-06: production runs go multi-colourway. Until now a run carried a per-size grid
-- (production_run_size, PK run_id+size_id) and ReceiveProductionRun took ONE product_id — but a
-- marker (раскладка) is cut for several colour-models at once, so a batch yields several products.
-- production_run_line replaces the grid with a colourway/product × size line; receive then groups
-- lines by product_id and increments each product's stock. The old grid is BACKFILLED (product_id
-- unknown → NULL) and left in place — code switches to lines atomically with this deploy, and the
-- drop of production_run_size is a separate later migration (CLAUDE.md: prefer several small ones).
--
-- Also captures marker metadata on the run (efficiency % from the nesting software + free notes).
--
-- Idempotent: CREATE TABLE IF NOT EXISTS with inline named indexes/CHECKs, INSERT ... WHERE NOT
-- EXISTS backfill, and guarded ADD COLUMN via information_schema (MySQL 8 has no ADD COLUMN IF NOT
-- EXISTS) so a mid-file DDL failure re-runs cleanly from the top.

CREATE TABLE IF NOT EXISTS production_run_line (
    id INT AUTO_INCREMENT PRIMARY KEY,
    run_id INT NOT NULL,
    -- the product colour-model; NULL is allowed while planning (the colourway may not be published
    -- as a product yet), but receive requires product_id on every line with received_qty > 0.
    product_id INT NULL,
    size_id INT NOT NULL,
    planned_qty INT NOT NULL DEFAULT 0,
    received_qty INT NULL,
    defect_qty INT NULL,
    CONSTRAINT fk_prl_run FOREIGN KEY (run_id) REFERENCES production_run(id) ON DELETE CASCADE,
    CONSTRAINT fk_prl_product FOREIGN KEY (product_id) REFERENCES product(id),
    CONSTRAINT fk_prl_size FOREIGN KEY (size_id) REFERENCES size(id),
    CONSTRAINT chk_prl_planned CHECK (planned_qty >= 0),
    CONSTRAINT chk_prl_received CHECK (received_qty IS NULL OR received_qty >= 0),
    CONSTRAINT chk_prl_defect CHECK (defect_qty IS NULL OR defect_qty >= 0),
    UNIQUE KEY uniq_prl (run_id, product_id, size_id),
    INDEX idx_prl_run (run_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

-- backfill existing runs from the per-size grid (product_id unknown → NULL). Guarded so re-running
-- the migration after a partial apply does not duplicate: skip any run that already has lines.
INSERT INTO production_run_line (run_id, product_id, size_id, planned_qty, received_qty, defect_qty)
SELECT s.run_id, NULL, s.size_id, s.planned_qty, s.received_qty, s.defect_qty
FROM production_run_size s
WHERE NOT EXISTS (SELECT 1 FROM production_run_line l WHERE l.run_id = s.run_id);

-- marker (nesting) metadata on the run.
SET @need_cols := (SELECT COUNT(*) = 0 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'production_run' AND COLUMN_NAME = 'marker_efficiency_pct');
SET @sql := IF(@need_cols,
    'ALTER TABLE production_run
        ADD COLUMN marker_efficiency_pct DECIMAL(5,2) NULL,
        ADD COLUMN marker_notes TEXT NULL',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

-- +migrate Down

DROP TABLE IF EXISTS production_run_line;
-- (leaves the marker columns; a Down is not exercised in prod automigrate)
SELECT 1;
