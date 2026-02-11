-- +migrate Up
ALTER TABLE customer_order
ADD COLUMN refunded_amount DECIMAL(10, 2) NOT NULL DEFAULT 0
COMMENT 'Amount refunded for this order (in order currency)';

CREATE TABLE refunded_order_item (
    id INT PRIMARY KEY AUTO_INCREMENT,
    order_id INT NOT NULL,
    order_item_id INT NOT NULL,
    quantity_refunded INT NOT NULL,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP NOT NULL,
    FOREIGN KEY (order_id) REFERENCES customer_order(id) ON DELETE CASCADE,
    FOREIGN KEY (order_item_id) REFERENCES order_item(id) ON DELETE CASCADE
);

CREATE INDEX idx_refunded_order_item_order_id ON refunded_order_item(order_id);

-- +migrate Down
DROP TABLE IF EXISTS refunded_order_item;

ALTER TABLE customer_order
DROP COLUMN refunded_amount;
