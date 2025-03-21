-- +migrate Up
CREATE TABLE category_level (
    id INT PRIMARY KEY AUTO_INCREMENT,
    name VARCHAR(50) NOT NULL COMMENT 'Describes the level: top_category, sub_category, or type'
);

INSERT INTO category_level (name) VALUES
    ('top_category'),
    ('sub_category'),
    ('type');

-- Categories hierarchy table
CREATE TABLE category (
    id INT PRIMARY KEY AUTO_INCREMENT,
    name VARCHAR(255) NOT NULL,
    level_id INT NOT NULL,
    parent_id INT,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    FOREIGN KEY (level_id) REFERENCES category_level(id),
    FOREIGN KEY (parent_id) REFERENCES category(id) ON DELETE CASCADE,
    UNIQUE KEY unique_name_parent (name, parent_id)
);

-- Insert top-level categories
INSERT INTO category (name, level_id, parent_id) VALUES
    ('outerwear', 1, NULL),
    ('tops', 1, NULL),
    ('bottoms', 1, NULL),
    ('dresses', 1, NULL),
    ('loungewear_sleepwear', 1, NULL),
    ('accessories', 1, NULL),
    ('shoes', 1, NULL),
    ('bags', 1, NULL),
    ('objects', 1, NULL);

-- Outerwear sub-categories and types
INSERT INTO category (name, level_id, parent_id) 
SELECT 'jackets', 2, id FROM category WHERE name = 'outerwear';

INSERT INTO category (name, level_id, parent_id)
SELECT 'bomber', 3, id FROM category WHERE name = 'jackets'
UNION ALL
SELECT 'leather', 3, id FROM category WHERE name = 'jackets'
UNION ALL
SELECT 'puffer', 3, id FROM category WHERE name = 'jackets'
UNION ALL
SELECT 'rain', 3, id FROM category WHERE name = 'jackets'
UNION ALL
SELECT 'softshell', 3, id FROM category WHERE name = 'jackets'
UNION ALL
SELECT 'hardshell', 3, id FROM category WHERE name = 'jackets'
UNION ALL
SELECT 'blazer', 3, id FROM category WHERE name = 'jackets';

INSERT INTO category (name, level_id, parent_id)
SELECT 'coats', 2, id FROM category WHERE name = 'outerwear';

INSERT INTO category (name, level_id, parent_id)
SELECT 'trench', 3, id FROM category WHERE name = 'coats'
UNION ALL
SELECT 'peacoats', 3, id FROM category WHERE name = 'coats'
UNION ALL
SELECT 'duvet', 3, id FROM category WHERE name = 'coats'
UNION ALL
SELECT 'parkas', 3, id FROM category WHERE name = 'coats'
UNION ALL
SELECT 'duffle', 3, id FROM category WHERE name = 'coats';

INSERT INTO category (name, level_id, parent_id)
SELECT 'vests', 2, id FROM category WHERE name = 'outerwear';

INSERT INTO category (name, level_id, parent_id)
SELECT 'puffer', 3, id FROM category WHERE name = 'vests'
UNION ALL
SELECT 'fleece', 3, id FROM category WHERE name = 'vests'
UNION ALL
SELECT 'leather', 3, id FROM category WHERE name = 'vests'
UNION ALL
SELECT 'down', 3, id FROM category WHERE name = 'vests'
UNION ALL
SELECT 'cargo', 3, id FROM category WHERE name = 'vests'
UNION ALL
SELECT 'softshell', 3, id FROM category WHERE name = 'vests';

-- Tops sub-categories and types
INSERT INTO category (name, level_id, parent_id)
SELECT 'shirts', 2, id FROM category WHERE name = 'tops';

INSERT INTO category (name, level_id, parent_id)
SELECT 'short_sleeve', 3, id FROM category WHERE name = 'shirts'
UNION ALL
SELECT 'overshirts', 3, id FROM category WHERE name = 'shirts'
UNION ALL
SELECT 'linen', 3, id FROM category WHERE name = 'shirts'
UNION ALL
SELECT 'mesh', 3, id FROM category WHERE name = 'shirts';

INSERT INTO category (name, level_id, parent_id)
SELECT 'tshirts', 2, id FROM category WHERE name = 'tops';

INSERT INTO category (name, level_id, parent_id)
SELECT 'crew_neck', 3, id FROM category WHERE name = 'tshirts'
UNION ALL
SELECT 'v_neck', 3, id FROM category WHERE name = 'tshirts'
UNION ALL
SELECT 'graphic', 3, id FROM category WHERE name = 'tshirts'
UNION ALL
SELECT 'long_sleeve', 3, id FROM category WHERE name = 'tshirts'
UNION ALL
SELECT 'pocket', 3, id FROM category WHERE name = 'tshirts';

INSERT INTO category (name, level_id, parent_id)
SELECT 'sweaters_knits', 2, id FROM category WHERE name = 'tops';

INSERT INTO category (name, level_id, parent_id)
SELECT 'pullovers', 3, id FROM category WHERE name = 'sweaters_knits'
UNION ALL
SELECT 'cardigans', 3, id FROM category WHERE name = 'sweaters_knits'
UNION ALL
SELECT 'turtlenecks', 3, id FROM category WHERE name = 'sweaters_knits'
UNION ALL
SELECT 'lightweight', 3, id FROM category WHERE name = 'sweaters_knits'
UNION ALL
SELECT 'mesh', 3, id FROM category WHERE name = 'sweaters_knits';

INSERT INTO category (name, level_id, parent_id)
SELECT 'tanks', 2, id FROM category WHERE name = 'tops';

INSERT INTO category (name, level_id, parent_id)
SELECT 'hoodies_sweatshirts', 2, id FROM category WHERE name = 'tops';

INSERT INTO category (name, level_id, parent_id)
SELECT 'pullover', 3, id FROM category WHERE name = 'hoodies_sweatshirts'
UNION ALL
SELECT 'zip', 3, id FROM category WHERE name = 'hoodies_sweatshirts'
UNION ALL
SELECT 'graphic', 3, id FROM category WHERE name = 'hoodies_sweatshirts'
UNION ALL
SELECT 'crewneck', 3, id FROM category WHERE name = 'hoodies_sweatshirts'
UNION ALL
SELECT 'cropped', 3, id FROM category WHERE name = 'hoodies_sweatshirts';

INSERT INTO category (name, level_id, parent_id)
SELECT 'crop', 2, id FROM category WHERE name = 'tops';

-- Bottoms sub-categories and types
INSERT INTO category (name, level_id, parent_id)
SELECT 'pants', 2, id FROM category WHERE name = 'bottoms';

INSERT INTO category (name, level_id, parent_id)
SELECT 'trousers', 3, id FROM category WHERE name = 'pants'
UNION ALL
SELECT 'cargo', 3, id FROM category WHERE name = 'pants'
UNION ALL
SELECT 'drop_crotch', 3, id FROM category WHERE name = 'pants'
UNION ALL
SELECT 'cropped', 3, id FROM category WHERE name = 'pants'
UNION ALL
SELECT 'joggers', 3, id FROM category WHERE name = 'pants'
UNION ALL
SELECT 'denim', 3, id FROM category WHERE name = 'pants'
UNION ALL
SELECT 'chinos', 3, id FROM category WHERE name = 'pants'
UNION ALL
SELECT 'leather', 3, id FROM category WHERE name = 'pants';

INSERT INTO category (name, level_id, parent_id)
SELECT 'shorts', 2, id FROM category WHERE name = 'bottoms';

INSERT INTO category (name, level_id, parent_id)
SELECT 'cargo', 3, id FROM category WHERE name = 'shorts'
UNION ALL
SELECT 'drop_crotch', 3, id FROM category WHERE name = 'shorts'
UNION ALL
SELECT 'cropped', 3, id FROM category WHERE name = 'shorts'
UNION ALL
SELECT 'athletic', 3, id FROM category WHERE name = 'shorts'
UNION ALL
SELECT 'denim', 3, id FROM category WHERE name = 'shorts'
UNION ALL
SELECT 'chinos', 3, id FROM category WHERE name = 'shorts'
UNION ALL
SELECT 'leather', 3, id FROM category WHERE name = 'shorts';

INSERT INTO category (name, level_id, parent_id)
SELECT 'skirts', 2, id FROM category WHERE name = 'bottoms';

INSERT INTO category (name, level_id, parent_id)
SELECT 'mini', 3, id FROM category WHERE name = 'skirts'
UNION ALL
SELECT 'midi', 3, id FROM category WHERE name = 'skirts'
UNION ALL
SELECT 'maxi', 3, id FROM category WHERE name = 'skirts'
UNION ALL
SELECT 'pencil', 3, id FROM category WHERE name = 'skirts'
UNION ALL
SELECT 'pleated', 3, id FROM category WHERE name = 'skirts'
UNION ALL
SELECT 'wrap', 3, id FROM category WHERE name = 'skirts';

-- Dresses types (no sub-category)
INSERT INTO category (name, level_id, parent_id)
SELECT 'shirt', 3, id FROM category WHERE name = 'dresses'
UNION ALL
SELECT 'maxi', 3, id FROM category WHERE name = 'dresses'
UNION ALL
SELECT 'mini', 3, id FROM category WHERE name = 'dresses'
UNION ALL
SELECT 'mesh', 3, id FROM category WHERE name = 'dresses';

-- Loungewear/Sleepwear sub-categories and types
INSERT INTO category (name, level_id, parent_id)
SELECT 'boxers', 2, id FROM category WHERE name = 'loungewear_sleepwear';

INSERT INTO category (name, level_id, parent_id)
SELECT 'classic', 3, id FROM category WHERE name = 'boxers'
UNION ALL
SELECT 'boxer', 3, id FROM category WHERE name = 'boxers'
UNION ALL
SELECT 'relaxed', 3, id FROM category WHERE name = 'boxers';

INSERT INTO category (name, level_id, parent_id)
SELECT 'bralettes', 2, id FROM category WHERE name = 'loungewear_sleepwear';

INSERT INTO category (name, level_id, parent_id)
SELECT 'cotton', 3, id FROM category WHERE name = 'bralettes'
UNION ALL
SELECT 'lace', 3, id FROM category WHERE name = 'bralettes'
UNION ALL
SELECT 'sports', 3, id FROM category WHERE name = 'bralettes'
UNION ALL
SELECT 'mesh', 3, id FROM category WHERE name = 'bralettes';

INSERT INTO category (name, level_id, parent_id)
SELECT 'briefs', 2, id FROM category WHERE name = 'loungewear_sleepwear';

INSERT INTO category (name, level_id, parent_id)
SELECT 'lace', 3, id FROM category WHERE name = 'briefs'
UNION ALL
SELECT 'sports', 3, id FROM category WHERE name = 'briefs'
UNION ALL
SELECT 'mesh', 3, id FROM category WHERE name = 'briefs'
UNION ALL
SELECT 'cotton', 3, id FROM category WHERE name = 'briefs';

INSERT INTO category (name, level_id, parent_id)
SELECT 'robes', 2, id FROM category WHERE name = 'loungewear_sleepwear';

INSERT INTO category (name, level_id, parent_id)
SELECT 'waffle', 3, id FROM category WHERE name = 'robes'
UNION ALL
SELECT 'belted', 3, id FROM category WHERE name = 'robes'
UNION ALL
SELECT 'wrap', 3, id FROM category WHERE name = 'robes';

INSERT INTO category (name, level_id, parent_id)
SELECT 'swimwear_w', 2, id FROM category WHERE name = 'loungewear_sleepwear';

INSERT INTO category (name, level_id, parent_id)
SELECT 'swimwear_m', 2, id FROM category WHERE name = 'loungewear_sleepwear';

-- Accessories sub-categories and types
INSERT INTO category (name, level_id, parent_id)
SELECT 'jewelry', 2, id FROM category WHERE name = 'accessories';

INSERT INTO category (name, level_id, parent_id)
SELECT 'necklaces', 3, id FROM category WHERE name = 'jewelry'
UNION ALL
SELECT 'earrings', 3, id FROM category WHERE name = 'jewelry'
UNION ALL
SELECT 'rings', 3, id FROM category WHERE name = 'jewelry'
UNION ALL
SELECT 'bracelets', 3, id FROM category WHERE name = 'jewelry';

INSERT INTO category (name, level_id, parent_id)
SELECT 'gloves', 2, id FROM category WHERE name = 'accessories';

INSERT INTO category (name, level_id, parent_id)
SELECT 'leather', 3, id FROM category WHERE name = 'gloves'
UNION ALL
SELECT 'fingerless', 3, id FROM category WHERE name = 'gloves'
UNION ALL
SELECT 'mittens', 3, id FROM category WHERE name = 'gloves';

INSERT INTO category (name, level_id, parent_id)
SELECT 'hats', 2, id FROM category WHERE name = 'accessories';

INSERT INTO category (name, level_id, parent_id)
SELECT 'beanies', 3, id FROM category WHERE name = 'hats'
UNION ALL
SELECT 'caps', 3, id FROM category WHERE name = 'hats'
UNION ALL
SELECT 'panama', 3, id FROM category WHERE name = 'hats'
UNION ALL
SELECT 'bucket', 3, id FROM category WHERE name = 'hats'
UNION ALL
SELECT 'sun', 3, id FROM category WHERE name = 'hats';

INSERT INTO category (name, level_id, parent_id)
SELECT 'socks', 2, id FROM category WHERE name = 'accessories';

INSERT INTO category (name, level_id, parent_id)
SELECT 'crew', 3, id FROM category WHERE name = 'socks'
UNION ALL
SELECT 'ankle', 3, id FROM category WHERE name = 'socks'
UNION ALL
SELECT 'knee_high', 3, id FROM category WHERE name = 'socks';

INSERT INTO category (name, level_id, parent_id)
SELECT 'belts', 2, id FROM category WHERE name = 'accessories';

INSERT INTO category (name, level_id, parent_id)
SELECT 'scarves', 2, id FROM category WHERE name = 'accessories';

INSERT INTO category (name, level_id, parent_id)
SELECT 'silk', 3, id FROM category WHERE name = 'scarves'
UNION ALL
SELECT 'cashmere', 3, id FROM category WHERE name = 'scarves'
UNION ALL
SELECT 'bandanas', 3, id FROM category WHERE name = 'scarves'
UNION ALL
SELECT 'shawls', 3, id FROM category WHERE name = 'scarves';

-- Shoes sub-categories and types
INSERT INTO category (name, level_id, parent_id)
SELECT 'boots', 2, id FROM category WHERE name = 'shoes';

INSERT INTO category (name, level_id, parent_id)
SELECT 'ankle', 3, id FROM category WHERE name = 'boots'
UNION ALL
SELECT 'tall', 3, id FROM category WHERE name = 'boots'
UNION ALL
SELECT 'mid_calf', 3, id FROM category WHERE name = 'boots';

INSERT INTO category (name, level_id, parent_id)
SELECT 'heels', 2, id FROM category WHERE name = 'shoes';

INSERT INTO category (name, level_id, parent_id)
SELECT 'flats', 2, id FROM category WHERE name = 'shoes';

INSERT INTO category (name, level_id, parent_id)
SELECT 'ballerina', 3, id FROM category WHERE name = 'flats'
UNION ALL
SELECT 'lace_ups', 3, id FROM category WHERE name = 'flats'
UNION ALL
SELECT 'slippers_loafers', 3, id FROM category WHERE name = 'flats';

INSERT INTO category (name, level_id, parent_id)
SELECT 'slippers_loafers', 2, id FROM category WHERE name = 'shoes';

INSERT INTO category (name, level_id, parent_id)
SELECT 'sneakers', 2, id FROM category WHERE name = 'shoes';

INSERT INTO category (name, level_id, parent_id)
SELECT 'high_top', 3, id FROM category WHERE name = 'sneakers'
UNION ALL
SELECT 'low_top', 3, id FROM category WHERE name = 'sneakers'
UNION ALL
SELECT 'wedge', 3, id FROM category WHERE name = 'sneakers';
INSERT INTO category (name, level_id, parent_id)
SELECT 'sandals', 2, id FROM category WHERE name = 'shoes';

INSERT INTO category (name, level_id, parent_id)
SELECT 'flat', 3, id FROM category WHERE name = 'sandals'
UNION ALL
SELECT 'heeled', 3, id FROM category WHERE name = 'sandals';

-- Bags sub-categories
INSERT INTO category (name, level_id, parent_id)
SELECT 'backpacks', 2, id FROM category WHERE name = 'bags';

INSERT INTO category (name, level_id, parent_id)
SELECT 'handle', 2, id FROM category WHERE name = 'bags';

INSERT INTO category (name, level_id, parent_id)
SELECT 'shoulder', 2, id FROM category WHERE name = 'bags';

INSERT INTO category (name, level_id, parent_id)
SELECT 'tote', 2, id FROM category WHERE name = 'bags';

INSERT INTO category (name, level_id, parent_id)
SELECT 'home', 2, id FROM category WHERE name = 'objects'
UNION ALL
SELECT 'body', 2, id FROM category WHERE name = 'objects'
UNION ALL
SELECT 'other', 2, id FROM category WHERE name = 'objects';

CREATE VIEW view_categories AS
SELECT 
    c1.id AS category_id,
    c1.name AS category_name,
    c1.level_id,
    cl.name AS level_name,
    c1.parent_id,
    c2.name AS parent_name,
    c1.created_at,
    c1.updated_at
FROM 
    category c1
LEFT JOIN 
    category c2 ON c1.parent_id = c2.id
JOIN 
    category_level cl ON c1.level_id = cl.id;

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

-- EU shoe sizes for women (35–43 with 0.5 step)
INSERT INTO
    size (name)
VALUES
    ('35'),
    ('35.5'),
    ('36'),
    ('36.5'),
    ('37'),
    ('37.5'),
    ('38'),
    ('38.5'),
    ('39'),
    ('39.5'),
    ('40'),
    ('40.5'),
    ('41'),
    ('41.5'),
    ('42'),
    ('42.5'),
    ('43'),
    ('43.5'),
    ('44'),
    ('44.5'),
    ('45'),
    ('45.5'),
    ('46'),
    ('46.5'),
    ('47'),
    ('47.5'),
    ('48');

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
    ('chest'),
    ('sleeve'),
    ('width'),
    ('leg-opening'),
    ('hip'),
    ('bottom-width'),
    ('depth'),
    ('start-fit-length'),
    ('end-fit-length'),
    ('height');

CREATE TABLE payment_method (
    id INT PRIMARY KEY AUTO_INCREMENT,
    name VARCHAR(50) NOT NULL UNIQUE,
    allowed BOOLEAN DEFAULT TRUE 
);

INSERT INTO
    payment_method (name, allowed)
VALUES
    ('card', true),
    ('card-test', true),
    ('eth', true),
    ('eth-test', true),
    ('usdt-tron', true),
    ('usdt-shasta', true);

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
    top_category_id INT NOT NULL,
    sub_category_id INT NULL,
    type_id INT NULL,
    model_wears_height_cm INT NULL,
    model_wears_size_id INT NULL REFERENCES size(id),
    description TEXT NOT NULL,
    hidden BOOLEAN DEFAULT FALSE,
    target_gender VARCHAR(255) NOT NULL CHECK (
        target_gender REGEXP '^(male|female|unisex)$'
    ),
    care_instructions VARCHAR(255) NULL CHECK (
        care_instructions IS NULL OR 
        care_instructions REGEXP '^(\s*|((MW(N|30|40|50|60|95)|GW|VGW|HW|DNW|BA|NCB|DNB|TD(N|L|M|H|D)|LD|DF|DD|DIS|LDS|DFS|DDS|I(L|M|H)|DN(S|I)|DC(AS|PS|ASE)|GD?C|VG?DC|PWC|G?PWC|DN(DC|WC))(\s*,\s*(MW(N|30|40|50|60|95)|GW|VGW|HW|DNW|BA|NCB|DNB|TD(N|L|M|H|D)|LD|DF|DD|DIS|LDS|DFS|DDS|I(L|M|H)|DN(S|I)|DC(AS|PS|ASE)|GD?C|VG?DC|PWC|G?PWC|DN(DC|WC)))*\s*))$'
    ),
    composition VARCHAR(255) NULL CHECK (
        composition IS NULL OR 
        composition REGEXP '^([A-Z]+(?:-[A-Z]+)*:(100|[1-9][0-9]?))(,\s*[A-Z]+(?:-[A-Z]+)*:(100|[1-9][0-9]?))*$'
    ),
    FOREIGN KEY (top_category_id) REFERENCES category(id),
    FOREIGN KEY (sub_category_id) REFERENCES category(id),
    FOREIGN KEY (type_id) REFERENCES category(id),
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
    start TIMESTAMP,
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
    expired_at TIMESTAMP NULL DEFAULT NULL,
    FOREIGN KEY(order_id) REFERENCES customer_order(id) ON DELETE CASCADE,
    FOREIGN KEY(payment_method_id) REFERENCES payment_method(id)
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
    heading VARCHAR(255) NOT NULL,
    description TEXT NOT NULL,
    tag VARCHAR(255) NOT NULL,
    video_id INT,
    FOREIGN KEY (video_id) REFERENCES media(id)
);

CREATE TABLE archive_item (
    id INT PRIMARY KEY AUTO_INCREMENT,
    archive_id INT NOT NULL,
    media_id INT NOT NULL,
    is_video BOOLEAN DEFAULT FALSE,
    FOREIGN KEY (archive_id) REFERENCES archive(id) ON DELETE CASCADE,
    FOREIGN KEY (media_id) REFERENCES media(id)
);

CREATE TABLE hero (
    id INT PRIMARY KEY AUTO_INCREMENT,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP NOT NULL,
    data JSON NOT NULL
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


CREATE INDEX idx_order_item_order_id ON order_item(order_id);

CREATE INDEX idx_customer_order_status_id ON customer_order(order_status_id);

CREATE INDEX idx_payment_method_id ON payment(payment_method_id);

CREATE INDEX idx_buyer_email ON buyer(email);

CREATE INDEX idx_customer_order_promo_id ON customer_order(promo_id);

CREATE INDEX idx_payment_method_order ON payment(payment_method_id, order_id);

CREATE UNIQUE INDEX idx_buyer_order_email ON buyer(order_id, email);

CREATE INDEX idx_product_size_size_id_product_id ON product_size(size_id, product_id);

CREATE INDEX idx_product_tag_tag_product_id ON product_tag(tag, product_id);

CREATE INDEX idx_product_target_gender ON product(target_gender);

CREATE INDEX idx_product_media_product_id_media_id ON product_media(product_id, media_id);

CREATE INDEX idx_product_thumbnail_id ON product(thumbnail_id);