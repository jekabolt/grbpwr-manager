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
    ('bag'),
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
    ('card-test'),
    ('eth'),
    ('eth-test'),
    ('usdt-tron'),
    ('usdt-shasta');

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

CREATE TABLE media (
    id INT PRIMARY KEY AUTO_INCREMENT,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP NOT NULL,
    full_size VARCHAR(255) NOT NULL,
    full_size_width INT NOT NULL,
    full_size_height INT NOT NULL,
    thumbnail VARCHAR(255) NOT NULL,
    thumbnail_width INT NOT NULL,
    thumbnail_height INT NOT NULL,
    compressed VARCHAR(255) NOT NULL,
    compressed_width INT NOT NULL,
    compressed_height INT NOT NULL,
    blur_hash VARCHAR(255) NULL
);

CREATE TABLE product (
    id INT PRIMARY KEY AUTO_INCREMENT,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP NOT NULL,
    updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP NOT NULL,
    preorder TIMESTAMP NULL,
    name VARCHAR(255) NOT NULL,
    brand VARCHAR(255) NOT NULL,
    sku VARCHAR(255) NOT NULL UNIQUE,
    color VARCHAR(255) NOT NULL,
    color_hex VARCHAR(255) NOT NULL CHECK (
        color_hex REGEXP '^#([A-Fa-f0-9]{6}|[A-Fa-f0-9]{3})$'
    ),
    country_of_origin VARCHAR(50) NOT NULL,
    thumbnail_id INT NOT NULL,
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
    FOREIGN KEY (category_id) REFERENCES category(id) ON DELETE CASCADE,
    FOREIGN KEY (thumbnail_id) REFERENCES media(id)
);

CREATE TABLE product_size (
    id INT PRIMARY KEY AUTO_INCREMENT,
    product_id INT NOT NULL,
    size_id INT NOT NULL,
    quantity INT CHECK (quantity IS NULL OR quantity >= 0),
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
    media_id INT NOT NULL,
    FOREIGN KEY (product_id) REFERENCES product(id) ON DELETE CASCADE,
    FOREIGN KEY (media_id) REFERENCES media(id)
);

CREATE TABLE product_tag (
    id INT PRIMARY KEY AUTO_INCREMENT,
    product_id INT NOT NULL,
    tag VARCHAR(255) NOT NULL,
    FOREIGN KEY(product_id) REFERENCES product(id) ON DELETE CASCADE
);


CREATE TABLE subscriber (
    id INT PRIMARY KEY AUTO_INCREMENT,
    email VARCHAR(100) NOT NULL UNIQUE CHECK (
        email REGEXP '^[a-zA-Z0-9._%+-]+@[a-zA-Z0-9.-]+\\.[a-zA-Z]{2,}$'
    ),
    receive_promo_emails BOOLEAN DEFAULT FALSE
);

CREATE TABLE shipment_carrier (
    id INT PRIMARY KEY AUTO_INCREMENT,
    carrier VARCHAR(255) NOT NULL UNIQUE,
    price DECIMAL(10, 2) NOT NULL,
    tracking_url VARCHAR(255) NOT NULL,
    allowed BOOLEAN DEFAULT FALSE,
    description TEXT
);

INSERT INTO shipment_carrier (carrier, price, tracking_url, allowed, description)
VALUES
    ('DHL', 10.99, 'https://www.dhl.com/pl-en/home/tracking/tracking-express.html?submit=1&tracking-id=%s', TRUE, 'DHL global shipping services with fast international tracking.'),
    ('FREE', 0, 'https://www.dhl.com/pl-en/home/tracking/tracking-express.html?submit=1&tracking-id=%s', TRUE, 'Complimentary shipping option with basic tracking features.');


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
    placed DATETIME DEFAULT CURRENT_TIMESTAMP,
    modified DATETIME DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    total_price DECIMAL(10, 2) NOT NULL,
    order_status_id INT NOT NULL,
    promo_id INT DEFAULT NULL,
    FOREIGN KEY (promo_id) REFERENCES promo_code(id),
    FOREIGN KEY (order_status_id) REFERENCES order_status(id) ON DELETE CASCADE
);

CREATE TABLE shipment (
    id INT PRIMARY KEY AUTO_INCREMENT,
    order_id INT NOT NULL UNIQUE,
    cost DECIMAL(10, 2) NOT NULL,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP NOT NULL,
    updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP NOT NULL,
    carrier_id INT NOT NULL,
    tracking_code VARCHAR(255),
    shipping_date DATETIME,
    estimated_arrival_date DATETIME,
    FOREIGN KEY (carrier_id) REFERENCES shipment_carrier(id),
    FOREIGN KEY (order_id) REFERENCES customer_order(id) ON DELETE CASCADE
);


CREATE TABLE address (
    id INT PRIMARY KEY AUTO_INCREMENT,
    country VARCHAR(255) NOT NULL,
    state VARCHAR(255),
    city VARCHAR(255) NOT NULL,
    address_line_one VARCHAR(255) NOT NULL,
    address_line_two VARCHAR(255),
    company VARCHAR(255),
    postal_code VARCHAR(20) NOT NULL
);

CREATE TABLE buyer (
    id INT PRIMARY KEY AUTO_INCREMENT,
    order_id INT NOT NULL UNIQUE,
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
    billing_address_id INT NOT NULL,
    shipping_address_id INT NOT NULL,
    FOREIGN KEY (order_id) REFERENCES customer_order(id) ON DELETE CASCADE,
    FOREIGN KEY (billing_address_id) REFERENCES address(id) ON DELETE CASCADE,
    FOREIGN KEY (shipping_address_id) REFERENCES address(id) ON DELETE CASCADE
);

CREATE TABLE payment (
    id INT PRIMARY KEY AUTO_INCREMENT,
    order_id INT NOT NULL UNIQUE,
    payment_method_id INT NOT NULL,
    transaction_id VARCHAR(255) UNIQUE,
    transaction_amount DECIMAL(10, 2) NOT NULL,
    transaction_amount_payment_currency DECIMAL(20, 2) NOT NULL,
    payer VARCHAR(255),
    payee VARCHAR(255),
    client_secret VARCHAR(255),
    is_transaction_done BOOLEAN NOT NULL DEFAULT FALSE,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    modified_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    FOREIGN KEY(order_id) REFERENCES customer_order(id) ON DELETE CASCADE,
    FOREIGN KEY(payment_method_id) REFERENCES payment_method(id)
);


CREATE TABLE order_item (
    id INT PRIMARY KEY AUTO_INCREMENT,
    order_id INT NOT NULL UNIQUE,
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
    heading VARCHAR(255) NOT NULL,
    text TEXT NOT NULL 
);

CREATE TABLE archive_item (
    id INT PRIMARY KEY AUTO_INCREMENT,
    media_id INT NOT NULL,
    name VARCHAR(255),
    url VARCHAR(255),
    archive_id INT NOT NULL,
    sequence_number INT NOT NULL DEFAULT 0,
    FOREIGN KEY (archive_id) REFERENCES archive(id) ON DELETE CASCADE,
    FOREIGN KEY (media_id) REFERENCES media(id)
);

CREATE TABLE hero (
    id INT PRIMARY KEY AUTO_INCREMENT,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP NOT NULL,
    media_id INT NOT NULL,
    explore_link VARCHAR(255),
    explore_text VARCHAR(255),
    main BOOLEAN DEFAULT FALSE NOT NULL,
    FOREIGN KEY (media_id) REFERENCES media(id)
);

CREATE TABLE hero_product (
    product_id INT NOT NULL,
    sequence_number INT NOT NULL DEFAULT 0,
    FOREIGN KEY (product_id) REFERENCES product(id) ON DELETE CASCADE
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

CREATE INDEX idx_product_size_id_on_size_measurement ON size_measurement(product_size_id);

CREATE INDEX idx_category_id_on_product ON product(category_id);