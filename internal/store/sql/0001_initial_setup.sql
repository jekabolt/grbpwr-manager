-- +migrate Up
CREATE TABLE products (
    id INT PRIMARY KEY AUTO_INCREMENT,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    preorder TEXT,
    name TEXT NOT NULL,
    description TEXT NOT NULL,
    hidden BOOLEAN DEFAULT FALSE
);

CREATE TABLE product_images (
    id INT PRIMARY KEY AUTO_INCREMENT,
    product_id INT NOT NULL,
    full_size TEXT NOT NULL,
    thumbnail TEXT NOT NULL,
    compressed TEXT NOT NULL,
    FOREIGN KEY(product_id) REFERENCES products(id)
);

CREATE TABLE product_categories (
    id INT PRIMARY KEY AUTO_INCREMENT,
    product_id INT NOT NULL,
    category TEXT NOT NULL,
    FOREIGN KEY(product_id) REFERENCES products(id)
);

CREATE TABLE product_sizes (
    id INT PRIMARY KEY AUTO_INCREMENT,
    product_id INT,
    XXS INT,
    XS INT,
    S INT,
    M INT,
    L INT,
    XL INT,
    XXL INT,
    OS INT,
    FOREIGN KEY(product_id) REFERENCES products(id)
);

CREATE TABLE product_prices (
    id INT PRIMARY KEY AUTO_INCREMENT,
    product_id INT NOT NULL,
    USD DECIMAL(10, 2) NOT NULL,
    EUR DECIMAL(10, 2) NOT NULL,
    USDC DECIMAL(10, 2) NOT NULL,
    ETH DECIMAL(10, 2) NOT NULL,
    sale INT DEFAULT 0,
    FOREIGN KEY(product_id) REFERENCES products(id)
);

CREATE TABLE hero (
    time_changed TIMESTAMP,
    content_link VARCHAR(255) NOT NULL,
    content_type VARCHAR(255) NOT NULL,
    explore_link VARCHAR(255) NOT NULL,
    explore_text VARCHAR(255) NOT NULL
);

CREATE TABLE payment_method (
    id INT PRIMARY KEY AUTO_INCREMENT,
    method VARCHAR(50) NOT NULL UNIQUE
);

INSERT INTO
    payment_method (method)
VALUES
    ('Card'),
    ('ETH'),
    ('USDC'),
    ('USDT');

CREATE TABLE payment_currency (
    id INT PRIMARY KEY AUTO_INCREMENT,
    currency VARCHAR(50) NOT NULL UNIQUE
);

INSERT INTO
    payment_currency (currency)
VALUES
    ('EUR'),
    ('USD'),
    ('USDC'),
    ('ETH');

CREATE TABLE order_status (
    id INT PRIMARY KEY AUTO_INCREMENT,
    status VARCHAR(50) NOT NULL UNIQUE
);

INSERT INTO
    order_status (status)
VALUES
    ('Placed'),
    ('Confirmed'),
    ('Shipped'),
    ('Delivered'),
    ('Cancelled'),
    ('Refunded');

CREATE TABLE payment (
    id INT PRIMARY KEY AUTO_INCREMENT,
    method_id INT NOT NULL,
    currency_id INT NOT NULL,
    currency_transaction_id VARCHAR(255) NOT NULL,
    transaction_amount DECIMAL(10, 2) NOT NULL,
    payer VARCHAR(255) NOT NULL,
    payee VARCHAR(255) NOT NULL,
    is_transaction_done BOOLEAN DEFAULT FALSE,
    FOREIGN KEY (method_id) REFERENCES payment_method(id),
    FOREIGN KEY (currency_id) REFERENCES payment_currency(id)
);

CREATE TABLE address (
    id INT PRIMARY KEY AUTO_INCREMENT,
    street VARCHAR(255) NOT NULL,
    house_number VARCHAR(50) NOT NULL,
    apartment_number VARCHAR(50) NOT NULL,
    city VARCHAR(255) NOT NULL,
    state VARCHAR(255) NOT NULL,
    country VARCHAR(255) NOT NULL,
    postal_code VARCHAR(20) NOT NULL
);

INSERT INTO
    address (
        street,
        house_number,
        apartment_number,
        city,
        state,
        country,
        postal_code
    )
VALUES
    (
        'test',
        '1A',
        '10',
        'New York',
        'New York',
        'United States',
        'test'
    );

CREATE TABLE buyer (
    id INT PRIMARY KEY AUTO_INCREMENT,
    first_name VARCHAR(255) NOT NULL,
    last_name VARCHAR(255) NOT NULL,
    email VARCHAR(255) NOT NULL,
    phone VARCHAR(20) NOT NULL,
    billing_address_id INT NOT NULL,
    shipping_address_id INT NOT NULL,
    receive_promo_emails BOOLEAN DEFAULT FALSE,
    FOREIGN KEY (billing_address_id) REFERENCES address(id),
    FOREIGN KEY (shipping_address_id) REFERENCES address(id)
);

CREATE TABLE shipment_carriers (
    id INT PRIMARY KEY AUTO_INCREMENT,
    carrier VARCHAR(255),
    USD DECIMAL(10, 2) NOT NULL,
    EUR DECIMAL(10, 2) NOT NULL,
    USDC DECIMAL(10, 2) NOT NULL,
    ETH DECIMAL(10, 2) NOT NULL,
    allowed BOOLEAN DEFAULT FALSE
);

INSERT INTO
    shipment_carriers (carrier, USD, EUR, USDC, ETH, allowed)
VALUES
    ('DHL', 10.99, 8.75, 12.50, 0.02, TRUE);

INSERT INTO
    shipment_carriers (carrier, USD, EUR, USDC, ETH, allowed)
VALUES
    ('FREE', 0, 0, 0, 0, TRUE);

CREATE TABLE shipment (
    id INT PRIMARY KEY AUTO_INCREMENT,
    carrier_id INT,
    tracking_code VARCHAR(255),
    shipping_date DATETIME,
    estimated_arrival_date DATETIME,
    FOREIGN KEY (carrier_id) REFERENCES shipment_carriers(id)
);

CREATE TABLE promo_codes (
    id INT PRIMARY KEY AUTO_INCREMENT,
    code VARCHAR(255) NOT NULL UNIQUE,
    free_shipping BOOLEAN DEFAULT FALSE,
    sale INT DEFAULT 0,
    expiration TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    allowed BOOLEAN DEFAULT FALSE
);

CREATE TABLE orders (
    id INT PRIMARY KEY AUTO_INCREMENT,
    buyer_id INT NOT NULL,
    placed DATETIME DEFAULT CURRENT_TIMESTAMP,
    payment_id INT NOT NULL,
    shipment_id INT NOT NULL,
    total_price DECIMAL(10, 2),
    status_id INT NOT NULL,
    promo_id INT,
    FOREIGN KEY (buyer_id) REFERENCES buyer(id),
    FOREIGN KEY (payment_id) REFERENCES payment(id),
    FOREIGN KEY (shipment_id) REFERENCES shipment(id),
    FOREIGN KEY (status_id) REFERENCES order_status(id),
    FOREIGN KEY (promo_id) REFERENCES promo_codes(id)
);

CREATE TABLE order_item (
    id INT PRIMARY KEY AUTO_INCREMENT,
    order_id INT,
    product_id INT,
    quantity INT,
    size VARCHAR(50),
    FOREIGN KEY (order_id) REFERENCES orders(id),
    FOREIGN KEY (product_id) REFERENCES products(id)
);

CREATE TABLE admins (
    id INT PRIMARY KEY AUTO_INCREMENT,
    username VARCHAR(255) NOT NULL UNIQUE,
    password_hash VARCHAR(255) NOT NULL
);