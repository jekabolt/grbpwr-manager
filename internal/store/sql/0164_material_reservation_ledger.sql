-- +migrate Up

-- PLM rework Q3 / S22 (01-DOMAIN-MODEL §2.8): packaging reservation ledger. Until now packaging was
-- consumed only at ship time (0116) — between order placement and ship the packaging warehouse was
-- blind, so it could be oversold. This append-only ledger (modelled on material_stock_movement)
-- records a claim's lifecycle: reserve at order placement → consume at ship → release at cancel/
-- refund. It NEVER moves on_hand — the physical decrement stays the ship-time writeoff in
-- material_stock_movement (reason='packaging'); the ledger only tracks whether a claim is still open.
--
--   available(material) = on_hand − Σ qty of OPEN claims
--   a claim (claim_key) is OPEN when it has a 'reserve' row and no 'consume'/'release' row.
--
-- claim_key is deterministic ("{order_id}:{material_id}") so a retry is a no-op: UNIQUE(claim_key,
-- event) makes a repeated reserve/consume/release for the same claim collapse to one row (mirrors the
-- order_packaging_consumed PK-claim idempotency of 0116).
--
-- Idempotent: CREATE TABLE IF NOT EXISTS with named FK/CHECK inline.

CREATE TABLE IF NOT EXISTS material_reservation_ledger (
    id INT AUTO_INCREMENT PRIMARY KEY,
    material_id INT NOT NULL,
    order_id INT NOT NULL,
    qty DECIMAL(12,3) NOT NULL,                       -- reserved / consumed / released magnitude (> 0)
    event VARCHAR(8) NOT NULL,                        -- reserve | consume | release
    claim_key VARCHAR(64) NOT NULL,                   -- idempotency root, e.g. "{order_id}:{material_id}"
    created_by VARCHAR(255) NOT NULL DEFAULT '',      -- audit: server-stamped username
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT fk_material_reservation_material FOREIGN KEY (material_id) REFERENCES material(id) ON DELETE CASCADE,
    CONSTRAINT fk_material_reservation_order FOREIGN KEY (order_id) REFERENCES customer_order(id) ON DELETE CASCADE,
    CONSTRAINT uniq_material_reservation_claim_event UNIQUE (claim_key, event),
    CONSTRAINT chk_material_reservation_qty CHECK (qty > 0),
    CONSTRAINT chk_material_reservation_event CHECK (event REGEXP '^(reserve|consume|release)$'),
    INDEX idx_material_reservation_material (material_id),
    INDEX idx_material_reservation_order (order_id),
    INDEX idx_material_reservation_claim (claim_key)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- +migrate Down

DROP TABLE IF EXISTS material_reservation_ledger;
SELECT 1;
