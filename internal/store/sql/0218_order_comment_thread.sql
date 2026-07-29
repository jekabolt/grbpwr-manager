-- +migrate Up
-- Append-only admin discussion on an order. customer_order.order_comment remains
-- the legacy latest-comment projection for existing readers.
CREATE TABLE IF NOT EXISTS order_comment (
  id BIGINT PRIMARY KEY AUTO_INCREMENT,
  order_id INT NOT NULL,
  author VARCHAR(255) NOT NULL DEFAULT '' COMMENT 'admin account username, from the JWT',
  body TEXT NOT NULL,
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  INDEX idx_order_comment_order (order_id, created_at),
  CONSTRAINT fk_order_comment_order FOREIGN KEY (order_id) REFERENCES customer_order(id) ON DELETE CASCADE
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4 COMMENT 'Append-only admin comments on customer orders';

-- +migrate Down
DROP TABLE IF EXISTS order_comment;
