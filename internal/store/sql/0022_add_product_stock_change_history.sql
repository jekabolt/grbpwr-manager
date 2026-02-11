-- +migrate Up
CREATE TABLE product_stock_change_history (
    id INT PRIMARY KEY AUTO_INCREMENT,
    product_id INT NOT NULL,
    size_id INT NOT NULL,
    quantity_delta DECIMAL(10, 2) NOT NULL,
    quantity_before DECIMAL(10, 2) NULL,
    quantity_after DECIMAL(10, 2) NOT NULL,
    source VARCHAR(50) NOT NULL,
    order_id INT NULL,
    order_uuid CHAR(36) NULL,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP NOT NULL,
    FOREIGN KEY (product_id) REFERENCES product(id) ON DELETE CASCADE,
    FOREIGN KEY (size_id) REFERENCES size(id) ON DELETE CASCADE,
    FOREIGN KEY (order_id) REFERENCES customer_order(id) ON DELETE SET NULL,
    INDEX idx_product_size (product_id, size_id),
    INDEX idx_created_at (created_at),
    INDEX idx_order_id (order_id)
) COMMENT 'Audit log of product size stock quantity changes';

-- +migrate Down
DROP TABLE IF EXISTS product_stock_change_history;
