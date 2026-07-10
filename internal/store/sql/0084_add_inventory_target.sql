-- +migrate Up
-- Description: Per-SKU inventory targets (reorder point, target days of cover, supplier
--   lead time) so the dashboard can compute a server-side needs_reorder decision instead
--   of returning a raw days-on-hand column for the operator to threshold by hand.
--   All three thresholds are optional (NULL = no threshold on that dimension).
-- Affected tables: inventory_target (new)
-- Type: additive (non-breaking)

CREATE TABLE inventory_target (
    id INT PRIMARY KEY AUTO_INCREMENT,
    product_id INT NOT NULL,
    size_id INT NOT NULL,
    reorder_point INT NULL CHECK (reorder_point IS NULL OR reorder_point >= 0),
    target_days_cover INT NULL CHECK (target_days_cover IS NULL OR target_days_cover >= 0),
    lead_time_days INT NULL CHECK (lead_time_days IS NULL OR lead_time_days >= 0),
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP NOT NULL,
    updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP NOT NULL,
    UNIQUE KEY uq_inventory_target (product_id, size_id),
    FOREIGN KEY (product_id) REFERENCES product(id),
    FOREIGN KEY (size_id) REFERENCES size(id)
);

-- +migrate Down

DROP TABLE IF EXISTS inventory_target;
