-- +migrate Up
CREATE TABLE category (
    id INT PRIMARY KEY AUTO_INCREMENT,
    name VARCHAR(50) NOT NULL UNIQUE
);

INSERT INTO
    category (name)
VALUES
    ('t-shirt'),
    ('jeans'),
    ('dress'),
    ('jacket'),
    ('sweater'),
    ('pant'),
    ('skirt'),
    ('short'),
    ('blazer'),
    ('coat'),
    ('socks'),
    ('underwear'),
    ('bra'),
    ('hat'),
    ('scarf'),
    ('gloves'),
    ('shoes'),
    ('belt'),
    ('other');

CREATE TABLE size (
    id INT PRIMARY KEY AUTO_INCREMENT,
    name VARCHAR(10) NOT NULL UNIQUE
);

INSERT INTO
    size (name)
VALUES
    ('xxs'),
    ('xs'),
    ('s'),
    ('m'),
    ('l'),
    ('xl'),
    ('xxl'),
    ('os');

CREATE TABLE measurement_name (
    id INT PRIMARY KEY AUTO_INCREMENT,
    name VARCHAR(50) NOT NULL UNIQUE
);

INSERT INTO
    measurement_name (name)
VALUES
    ('waist'),
    ('inseam'),
    ('length'),
    ('rise'),
    ('hips'),
    ('shoulders'),
    ('bust'),
    ('sleeve'),
    ('width'),
    ('height');

CREATE TABLE payment_method (
    id INT PRIMARY KEY AUTO_INCREMENT,
    name VARCHAR(50) NOT NULL UNIQUE,
    allowed BOOLEAN DEFAULT TRUE 
);

INSERT INTO
    payment_method (name)
VALUES
    ('card'),
    ('eth'),
    ('usdc'),
    ('usdt');

CREATE TABLE order_status (
    id INT PRIMARY KEY AUTO_INCREMENT,
    name VARCHAR(50) NOT NULL UNIQUE
);

INSERT INTO
    order_status (name)
VALUES
    ('placed'),
    ('awaiting_payment'),
    ('confirmed'),
    ('shipped'),
    ('delivered'),
    ('cancelled'),
    ('refunded');

CREATE TABLE product (
    id INT PRIMARY KEY AUTO_INCREMENT,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP NOT NULL,
    updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP NOT NULL,
    preorder VARCHAR(255),
    name VARCHAR(255) NOT NULL,
    brand VARCHAR(255) NOT NULL,
    sku VARCHAR(255) NOT NULL,
    color VARCHAR(255) NOT NULL,
    color_hex VARCHAR(255) NOT NULL CHECK (
        color_hex REGEXP '^#([A-Fa-f0-9]{6}|[A-Fa-f0-9]{3})$'
    ),
    country_of_origin VARCHAR(50) NOT NULL,
    thumbnail VARCHAR(255) NOT NULL,
    price DECIMAL(10, 2) NOT NULL CHECK (price >= 0),
    sale_percentage DECIMAL(5, 2) DEFAULT 0 CHECK (
        sale_percentage >= 0
        AND sale_percentage <= 100
    ),
    category_id INT NOT NULL,
    description TEXT NOT NULL,
    hidden BOOLEAN DEFAULT FALSE,
    target_gender VARCHAR(255) NOT NULL CHECK (
        target_gender REGEXP '^(male|female|unisex)$'
    ),
    FOREIGN KEY (category_id) REFERENCES category(id) ON DELETE CASCADE
);

CREATE TABLE product_size (
    id INT PRIMARY KEY AUTO_INCREMENT,
    product_id INT NOT NULL,
    size_id INT NOT NULL,
    quantity INT NOT NULL CHECK (quantity >= 0),
    UNIQUE(product_id, size_id),
    FOREIGN KEY(product_id) REFERENCES product(id) ON DELETE CASCADE,
    FOREIGN KEY(size_id) REFERENCES size(id) ON DELETE CASCADE
);

CREATE TABLE size_measurement (
    id INT PRIMARY KEY AUTO_INCREMENT,
    product_id INT NOT NULL,
    product_size_id INT NOT NULL,
    measurement_name_id INT NOT NULL,
    measurement_value DECIMAL(10, 2) NOT NULL,
    UNIQUE(product_id, product_size_id, measurement_name_id),
    FOREIGN KEY(measurement_name_id) REFERENCES measurement_name(id) ON DELETE CASCADE
);

CREATE TABLE product_media (
    id INT PRIMARY KEY AUTO_INCREMENT,
    product_id INT NOT NULL,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP NOT NULL,
    full_size VARCHAR(255) NOT NULL,
    thumbnail VARCHAR(255) NOT NULL,
    compressed VARCHAR(255) NOT NULL,
    FOREIGN KEY(product_id) REFERENCES product(id) ON DELETE CASCADE
);

CREATE TABLE product_tag (
    id INT PRIMARY KEY AUTO_INCREMENT,
    product_id INT NOT NULL,
    tag VARCHAR(255) NOT NULL,
    FOREIGN KEY(product_id) REFERENCES product(id) ON DELETE CASCADE
);


CREATE TABLE payment (
    id INT PRIMARY KEY AUTO_INCREMENT,
    payment_method_id INT NOT NULL,
    transaction_id VARCHAR(255) UNIQUE,
    transaction_amount DECIMAL(10, 2) NOT NULL,
    payer VARCHAR(255),
    payee VARCHAR(255),
    is_transaction_done BOOLEAN NOT NULL DEFAULT FALSE,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    modified_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    FOREIGN KEY(payment_method_id) REFERENCES payment_method(id)
);

CREATE TABLE address (
    id INT PRIMARY KEY AUTO_INCREMENT,
    street VARCHAR(255) NOT NULL,
    house_number VARCHAR(50) NOT NULL,
    apartment_number VARCHAR(50),
    city VARCHAR(255) NOT NULL,
    state VARCHAR(255),
    country VARCHAR(255) NOT NULL,
    postal_code VARCHAR(20) NOT NULL
);

CREATE TABLE buyer (
    id INT PRIMARY KEY AUTO_INCREMENT,
    first_name VARCHAR(255) NOT NULL,
    last_name VARCHAR(255) NOT NULL,
    email VARCHAR(100) NOT NULL CHECK (
        email REGEXP '^[a-zA-Z0-9._%+-]+@[a-zA-Z0-9.-]+\\.[a-zA-Z]{2,}$'
    ),
    phone VARCHAR(20) NOT NULL CHECK (
        phone REGEXP '^[0-9]+$'
        AND LENGTH(phone) >= 7
        AND LENGTH(phone) <= 15
    ),
    receive_promo_emails BOOLEAN DEFAULT FALSE,
    billing_address_id INT NOT NULL,
    shipping_address_id INT NOT NULL,
    FOREIGN KEY (billing_address_id) REFERENCES address(id) ON DELETE CASCADE,
    FOREIGN KEY (shipping_address_id) REFERENCES address(id) ON DELETE CASCADE
);

CREATE TABLE subscriber (
    id INT PRIMARY KEY AUTO_INCREMENT,
    name VARCHAR(255),
    email VARCHAR(100) NOT NULL CHECK (
        email REGEXP '^[a-zA-Z0-9._%+-]+@[a-zA-Z0-9.-]+\\.[a-zA-Z]{2,}$'
    ),
    receive_promo_emails BOOLEAN DEFAULT FALSE
);

CREATE TABLE shipment_carrier (
    id INT PRIMARY KEY AUTO_INCREMENT,
    carrier VARCHAR(255) NOT NULL UNIQUE,
    price DECIMAL(10, 2) NOT NULL,
    allowed BOOLEAN DEFAULT FALSE
);

INSERT INTO
    shipment_carrier (carrier, price, allowed)
VALUES
    ('DHL', 10.99, TRUE);

INSERT INTO
    shipment_carrier (carrier, price, allowed)
VALUES
    ('FREE', 0, TRUE);

CREATE TABLE shipment (
    id INT PRIMARY KEY AUTO_INCREMENT,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP NOT NULL,
    updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP NOT NULL,
    carrier_id INT NOT NULL,
    tracking_code VARCHAR(255),
    shipping_date DATETIME,
    estimated_arrival_date DATETIME,
    FOREIGN KEY (carrier_id) REFERENCES shipment_carrier(id)
);

CREATE TABLE promo_code (
    id INT PRIMARY KEY AUTO_INCREMENT,
    code VARCHAR(255) NOT NULL UNIQUE,
    free_shipping BOOLEAN DEFAULT FALSE,
    discount DECIMAL(10, 2) DEFAULT 0,
    expiration TIMESTAMP,
    voucher BOOLEAN DEFAULT FALSE,
    allowed BOOLEAN DEFAULT FALSE
);

CREATE TABLE customer_order (
    id INT PRIMARY KEY AUTO_INCREMENT,
    uuid CHAR(36) NOT NULL UNIQUE,
    buyer_id INT NOT NULL,
    placed DATETIME DEFAULT CURRENT_TIMESTAMP,
    modified DATETIME DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    payment_id INT NOT NULL,
    total_price DECIMAL(10, 2) NOT NULL,
    order_status_id INT NOT NULL,
    shipment_id INT NOT NULL,
    promo_id INT,
    FOREIGN KEY (buyer_id) REFERENCES buyer(id) ON DELETE CASCADE,
    FOREIGN KEY (payment_id) REFERENCES payment(id) ON DELETE CASCADE,
    FOREIGN KEY (shipment_id) REFERENCES shipment(id) ON DELETE CASCADE,
    FOREIGN KEY (promo_id) REFERENCES promo_code(id),
    FOREIGN KEY (order_status_id) REFERENCES order_status(id) ON DELETE CASCADE
);

CREATE TABLE order_item (
    id INT PRIMARY KEY AUTO_INCREMENT,
    order_id INT NOT NULL,
    product_id INT NOT NULL,
    product_price DECIMAL(10, 2) NOT NULL CHECK (product_price >= 0),
    product_sale_percentage DECIMAL(5, 2) DEFAULT 0 CHECK (
        product_sale_percentage >= 0
        AND product_sale_percentage <= 100
    ),
    quantity INT NOT NULL CHECK (quantity > 0),
    size_id INT NOT NULL,
    FOREIGN KEY (order_id) REFERENCES customer_order(id) ON DELETE CASCADE,
    FOREIGN KEY (product_id) REFERENCES product(id) ON DELETE CASCADE,
    FOREIGN KEY (size_id) REFERENCES size(id) ON DELETE CASCADE
);

CREATE TABLE admins (
    id INT PRIMARY KEY AUTO_INCREMENT,
    username VARCHAR(255) NOT NULL UNIQUE,
    password_hash VARCHAR(255) NOT NULL
);

CREATE TABLE archive (
    id INT PRIMARY KEY AUTO_INCREMENT,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP NOT NULL,
    updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP NOT NULL,
    title VARCHAR(255) NOT NULL,
    description TEXT NOT NULL
);

CREATE TABLE archive_item (
    id INT PRIMARY KEY AUTO_INCREMENT,
    media VARCHAR(255) NOT NULL,
    url VARCHAR(255),
    title VARCHAR(255),
    archive_id INT NOT NULL,
    FOREIGN KEY (archive_id) REFERENCES archive(id) ON DELETE CASCADE
);

CREATE TABLE media (
    id INT PRIMARY KEY AUTO_INCREMENT,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP NOT NULL,
    updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP NOT NULL,
    full_size VARCHAR(255) NOT NULL,
    thumbnail VARCHAR(255) NOT NULL,
    compressed VARCHAR(255) NOT NULL
);

CREATE TABLE hero (
    id INT PRIMARY KEY AUTO_INCREMENT,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP NOT NULL,
    content_link VARCHAR(255),
    content_type VARCHAR(255),
    explore_link VARCHAR(255),
    explore_text VARCHAR(255)
);

CREATE TABLE hero_product (
    hero_id INT NOT NULL,
    product_id INT NOT NULL,
    sequence_number INT NOT NULL DEFAULT 0,
    FOREIGN KEY (hero_id) REFERENCES hero(id) ON DELETE CASCADE,
    FOREIGN KEY (product_id) REFERENCES product(id) ON DELETE CASCADE,
    PRIMARY KEY (hero_id, product_id)
);

CREATE TABLE hero_ads (
    hero_id INT NOT NULL,
    content_link VARCHAR(255),
    content_type VARCHAR(255),
    explore_link VARCHAR(255),
    explore_text VARCHAR(255),
    FOREIGN KEY (hero_id) REFERENCES hero(id) ON DELETE CASCADE
);

CREATE TABLE send_email_request (
    id INT PRIMARY KEY AUTO_INCREMENT,
    from_email VARCHAR(255) NOT NULL,
    to_email VARCHAR(255) NOT NULL,
    html TEXT NOT NULL,
    subject VARCHAR(255) NOT NULL,
    reply_to VARCHAR(255),
    sent BOOLEAN DEFAULT FALSE,
    sent_at DATETIME NULL DEFAULT NULL,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP NOT NULL,
    error_msg VARCHAR(255) DEFAULT NULL
);

CREATE TABLE currency_rate (
    id INT PRIMARY KEY AUTO_INCREMENT,
    currency_code VARCHAR(255) NOT NULL UNIQUE,
    rate DECIMAL(10, 2) NOT NULL,
    updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP NOT NULL
);


CREATE INDEX idx_product_id_on_product_size ON product_size(product_id);

CREATE INDEX idx_product_id_on_product_media ON product_media(product_id);

CREATE INDEX idx_product_id_on_product_tag ON product_tag(product_id);

CREATE INDEX idx_buyer_id_on_order ON customer_order(buyer_id);

CREATE INDEX idx_payment_id_on_order ON customer_order(payment_id);

CREATE INDEX idx_shipment_id_on_order ON customer_order(shipment_id);

CREATE INDEX idx_product_size_id_on_size_measurement ON size_measurement(product_size_id);

CREATE INDEX idx_category_id_on_product ON product(category_id);