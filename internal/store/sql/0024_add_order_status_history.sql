-- +migrate Up
-- Migration: Add order status history tracking
-- Purpose: Track all order status transitions with timestamps and actors for audit trail
-- Affected tables: order_status_history (new)

-- Create order status history table
CREATE TABLE order_status_history (
    id INT PRIMARY KEY AUTO_INCREMENT,
    order_id INT NOT NULL,
    order_status_id INT NOT NULL,
    changed_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP NOT NULL,
    changed_by VARCHAR(255) NULL COMMENT 'admin username or "system" or "user"',
    notes TEXT NULL COMMENT 'optional notes about the status change',
    FOREIGN KEY (order_id) REFERENCES customer_order(id) ON DELETE CASCADE,
    FOREIGN KEY (order_status_id) REFERENCES order_status(id),
    INDEX idx_order_id (order_id),
    INDEX idx_changed_at (changed_at)
) COMMENT 'Audit trail of all order status changes';

-- Backfill: insert initial status for existing orders
-- Use the 'placed' timestamp as the initial status change time
INSERT INTO order_status_history (order_id, order_status_id, changed_at, changed_by)
SELECT id, order_status_id, placed, 'system'
FROM customer_order;

-- +migrate Down
DROP TABLE IF EXISTS order_status_history;
