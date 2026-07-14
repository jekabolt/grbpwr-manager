-- +migrate Up

-- material_stock is the maintained on-hand balance and moving-average unit cost of a catalog
-- material (new-flow NF-01). One row per material, created lazily on the first movement. The
-- balance is stored (not derived) so an issue can be guarded against going negative atomically,
-- exactly like product_size.quantity. Valuation is moving-average in the base currency (EUR);
-- lot/FIFO valuation is deliberately out of scope for v1.
CREATE TABLE IF NOT EXISTS material_stock (
    material_id        INT PRIMARY KEY,
    on_hand            DECIMAL(12, 3) NOT NULL DEFAULT 0,
    -- moving-average unit cost in the base currency; NULL until the first costed receipt
    avg_unit_cost_base DECIMAL(12, 4) NULL,
    updated_at         TIMESTAMP DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP NOT NULL,
    CONSTRAINT fk_material_stock_material FOREIGN KEY (material_id)
        REFERENCES material(id) ON DELETE CASCADE,
    CONSTRAINT chk_material_stock_on_hand CHECK (on_hand >= 0)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- material_stock_movement is the append-only ledger of every stock change, mirroring
-- product_stock_change_history: each row carries the balance before/after so the full history is
-- auditable. quantity is always non-negative; movement_type (and before/after) give the direction.
--   receipt            purchase-in
--   receipt_production auxiliary item produced by our own run lands in stock (NF-07)
--   issue_production   issued into a production run
--   issue_sample       issued to a sample
--   return_production  unused remainder returned from a run
--   return_sample      returned from a sample
--   adjustment         stock count (set/adjust) with a reason
--   writeoff           write-off (damage, loss, supplier defect)
-- sample_id has no FK yet — the sample table is introduced in migration 0108, which adds it.
CREATE TABLE IF NOT EXISTS material_stock_movement (
    id                INT AUTO_INCREMENT PRIMARY KEY,
    material_id       INT NOT NULL,
    movement_type     VARCHAR(24) NOT NULL,
    quantity          DECIMAL(12, 3) NOT NULL,
    on_hand_before    DECIMAL(12, 3) NOT NULL,
    on_hand_after     DECIMAL(12, 3) NOT NULL,
    -- unit cost of the movement: for a receipt from the supplier document (purchase currency);
    -- for an issue the moving average frozen at issue time (already base)
    unit_cost         DECIMAL(12, 4) NULL,
    currency          CHAR(3) NULL,
    unit_cost_base    DECIMAL(12, 4) NULL,
    -- typed references (at most one is set), mirroring the task deep-link pattern
    production_run_id INT NULL,
    sample_id         INT NULL,
    tech_card_id      INT NULL,
    lot               VARCHAR(64) NULL,
    supplier_doc      VARCHAR(255) NULL,
    reason            VARCHAR(32) NULL,
    comment           VARCHAR(255) NULL,
    admin_username    VARCHAR(255) NOT NULL DEFAULT '',
    occurred_at       DATE NULL,
    created_at        TIMESTAMP DEFAULT CURRENT_TIMESTAMP NOT NULL,
    CONSTRAINT fk_msm_material FOREIGN KEY (material_id) REFERENCES material(id),
    CONSTRAINT fk_msm_run FOREIGN KEY (production_run_id) REFERENCES production_run(id) ON DELETE SET NULL,
    CONSTRAINT fk_msm_tech_card FOREIGN KEY (tech_card_id) REFERENCES tech_card(id) ON DELETE SET NULL,
    CONSTRAINT chk_msm_type CHECK (movement_type REGEXP
        '^(receipt|receipt_production|issue_production|issue_sample|return_production|return_sample|adjustment|writeoff)$'),
    CONSTRAINT chk_msm_qty CHECK (quantity >= 0),
    INDEX idx_msm_material (material_id),
    INDEX idx_msm_run (production_run_id),
    INDEX idx_msm_sample (sample_id),
    INDEX idx_msm_type (movement_type),
    INDEX idx_msm_created (created_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- +migrate Down

DROP TABLE IF EXISTS material_stock_movement;
DROP TABLE IF EXISTS material_stock;
