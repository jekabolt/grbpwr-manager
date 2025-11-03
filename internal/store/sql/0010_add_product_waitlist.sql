-- +migrate Up
-- Create product_waitlist table for back-in-stock notifications
CREATE TABLE product_waitlist (
    id INT AUTO_INCREMENT PRIMARY KEY,
    product_id INT NOT NULL,
    size_id INT NOT NULL,
    email VARCHAR(255) NOT NULL,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    INDEX idx_product_size (product_id, size_id),
    INDEX idx_email (email),
    UNIQUE KEY unique_product_size_email (product_id, size_id, email),
    FOREIGN KEY (product_id) REFERENCES product(id) ON DELETE CASCADE,
    FOREIGN KEY (size_id) REFERENCES size(id) ON DELETE CASCADE
);

-- +migrate Down
-- Remove product_waitlist table
DROP TABLE IF EXISTS product_waitlist;

