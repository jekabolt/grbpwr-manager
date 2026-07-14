-- +migrate Up

-- gap-07 v2 (E): structured nesting / marker records instead of only the free-text
-- production_run.marker_efficiency_pct + marker_notes scalars. A `production_run_marker` is one
-- marker (раскладка / lay) imported from the CAD/nesting software (Gerber / Optitex / Lectra /
-- Audaces) or entered by hand: which fabric width and lay length it was nested on, how many units
-- it yields, its fabric-utilisation %, and a reference URL to the exported marker file. A run has
-- many markers (one per fabric/size layout); they are children of the run, full-replaced on update
-- and cascade-deleted with it, exactly like production_run_line / production_run_cost.
--
-- These are PLANNING / traceability data, NOT costing: nothing here feeds the run's actual cost or
-- cost_price. The per-marker efficiency_pct is a more granular companion to the run-header scalar,
-- not a replacement — the header stays for a quick single-figure summary.
--
-- Idempotent: CREATE TABLE IF NOT EXISTS with inline named FKs/indexes/CHECKs.

CREATE TABLE IF NOT EXISTS production_run_marker (
    id INT AUTO_INCREMENT PRIMARY KEY,
    run_id INT NOT NULL,
    source VARCHAR(24) NOT NULL DEFAULT 'manual',   -- gerber | optitex | lectra | audaces | manual | other
    marker_name VARCHAR(191) NULL,                  -- marker / lay identifier from the software
    size_id INT NULL,                               -- single-size marker (NULL = mixed-size lay)
    material_id INT NULL,                            -- fabric the marker was nested on (optional)
    marker_width DECIMAL(10,2) NULL,                -- fabric width the marker was made for (cm)
    lay_length DECIMAL(10,2) NULL,                  -- marker / lay length (cm)
    units_per_marker INT NULL,                      -- garments yielded by one marker
    efficiency_pct DECIMAL(5,2) NULL,               -- fabric utilisation % (per-marker)
    marker_file_url VARCHAR(512) NULL,              -- bare URL of the exported marker file (UploadPattern convention)
    notes VARCHAR(1024) NULL,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT fk_prm_run FOREIGN KEY (run_id) REFERENCES production_run(id) ON DELETE CASCADE,
    CONSTRAINT fk_prm_size FOREIGN KEY (size_id) REFERENCES size(id) ON DELETE SET NULL,
    CONSTRAINT fk_prm_material FOREIGN KEY (material_id) REFERENCES material(id) ON DELETE SET NULL,
    CONSTRAINT chk_prm_width CHECK (marker_width IS NULL OR marker_width >= 0),
    CONSTRAINT chk_prm_length CHECK (lay_length IS NULL OR lay_length >= 0),
    CONSTRAINT chk_prm_units CHECK (units_per_marker IS NULL OR units_per_marker >= 0),
    CONSTRAINT chk_prm_eff CHECK (efficiency_pct IS NULL OR (efficiency_pct >= 0 AND efficiency_pct <= 100)),
    CONSTRAINT chk_prm_source CHECK (source REGEXP '^(gerber|optitex|lectra|audaces|manual|other)$'),
    INDEX idx_prm_run (run_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- +migrate Down

DROP TABLE IF EXISTS production_run_marker;
SELECT 1;
