-- +migrate Up

-- gap-07 v2 (B): auto-consume packaging on ship. `packaging_bom` is the global recipe of packaging
-- materials (dust bag, box…) consumed per shipped order: `qty_per_order` once per shipment plus
-- `qty_per_item` × the order's unit count. On the shipped transition the server writes those off the
-- material warehouse (movement writeoff, reason=packaging) instead of the operator doing it by hand.
--
-- `order_packaging_consumed` is the idempotency guard (PK order_id): shipping is re-entrant
-- (SetTrackingNumber accepts an already-Shipped order), so the consume claims the order once — a
-- second ship is a no-op and never double-writes-off.
--
-- Idempotent DDL: both use CREATE TABLE IF NOT EXISTS guards.

CREATE TABLE IF NOT EXISTS packaging_bom (
    id INT AUTO_INCREMENT PRIMARY KEY,
    material_id INT NOT NULL,
    qty_per_order DECIMAL(12,3) NOT NULL DEFAULT 0,  -- consumed once per shipped order (a box)
    qty_per_item DECIMAL(12,3) NOT NULL DEFAULT 0,   -- consumed per unit in the order (a dust bag)
    active BOOLEAN NOT NULL DEFAULT TRUE,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    CONSTRAINT fk_packaging_bom_material FOREIGN KEY (material_id) REFERENCES material(id) ON DELETE CASCADE,
    CONSTRAINT uniq_packaging_bom_material UNIQUE (material_id),
    CONSTRAINT chk_packaging_bom_qty CHECK (qty_per_order >= 0 AND qty_per_item >= 0)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS order_packaging_consumed (
    order_id INT PRIMARY KEY,
    consumed_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    movement_count INT NOT NULL DEFAULT 0,           -- how many writeoff movements the consume produced
    skipped_count INT NOT NULL DEFAULT 0,            -- recipe materials skipped (short stock / deleted) — reconciliation trail (g25-02)
    CONSTRAINT fk_order_packaging_order FOREIGN KEY (order_id) REFERENCES customer_order(id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- +migrate Down

DROP TABLE IF EXISTS order_packaging_consumed;
DROP TABLE IF EXISTS packaging_bom;
SELECT 1;
