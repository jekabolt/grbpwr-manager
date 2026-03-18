-- +migrate Up

-- Order-level review (delivery & packaging)
CREATE TABLE order_review (
    id INT PRIMARY KEY AUTO_INCREMENT,
    order_id INT NOT NULL UNIQUE,
    delivery_rating ENUM(
        'much_faster_than_expected',
        'faster_than_expected',
        'as_expected',
        'slower_than_expected',
        'much_slower_than_expected'
    ) NOT NULL,
    packaging_rating ENUM(
        'damaged',
        'acceptable',
        'good',
        'excellent'
    ) NOT NULL,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP NOT NULL,
    FOREIGN KEY (order_id) REFERENCES customer_order(id) ON DELETE CASCADE
);

-- Item-level review (product rating, fit, recommendation, text)
CREATE TABLE order_item_review (
    id INT PRIMARY KEY AUTO_INCREMENT,
    order_item_id INT NOT NULL UNIQUE,
    rating ENUM('poor', 'fair', 'good', 'very_good', 'excellent') NOT NULL,
    fit_rating ENUM(
        'runs_small',
        'slightly_small',
        'true_to_size',
        'slightly_large',
        'runs_large'
    ) NOT NULL,
    recommend BOOLEAN NOT NULL DEFAULT TRUE,
    text TEXT NOT NULL,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP NOT NULL,
    FOREIGN KEY (order_item_id) REFERENCES order_item(id) ON DELETE CASCADE
);

-- +migrate Down
DROP TABLE IF EXISTS order_item_review;
DROP TABLE IF EXISTS order_review;