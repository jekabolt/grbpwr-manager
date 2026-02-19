-- +migrate Up

-- GA4 daily aggregated metrics
CREATE TABLE IF NOT EXISTS ga4_daily_metrics (
    id INT AUTO_INCREMENT PRIMARY KEY,
    date DATE NOT NULL,
    sessions INT NOT NULL DEFAULT 0,
    users INT NOT NULL DEFAULT 0,
    new_users INT NOT NULL DEFAULT 0,
    page_views INT NOT NULL DEFAULT 0,
    bounce_rate DECIMAL(5,4) NOT NULL DEFAULT 0,
    avg_session_duration DECIMAL(10,2) NOT NULL DEFAULT 0,
    pages_per_session DECIMAL(10,2) NOT NULL DEFAULT 0,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    UNIQUE KEY idx_date (date),
    KEY idx_created_at (created_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- GA4 product page metrics
CREATE TABLE IF NOT EXISTS ga4_product_page_metrics (
    id INT AUTO_INCREMENT PRIMARY KEY,
    date DATE NOT NULL,
    product_id INT NOT NULL,
    page_path VARCHAR(512) NOT NULL,
    page_views INT NOT NULL DEFAULT 0,
    add_to_carts INT NOT NULL DEFAULT 0,
    sessions INT NOT NULL DEFAULT 0,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    UNIQUE KEY idx_date_product (date, product_id),
    KEY idx_product_id (product_id),
    KEY idx_date (date),
    KEY idx_page_views (page_views)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- GA4 traffic source metrics
CREATE TABLE IF NOT EXISTS ga4_traffic_source_metrics (
    id INT AUTO_INCREMENT PRIMARY KEY,
    date DATE NOT NULL,
    source VARCHAR(255) NOT NULL,
    medium VARCHAR(255) NOT NULL,
    sessions INT NOT NULL DEFAULT 0,
    users INT NOT NULL DEFAULT 0,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    UNIQUE KEY idx_date_source_medium (date, source, medium),
    KEY idx_date (date),
    KEY idx_sessions (sessions)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- GA4 device metrics
CREATE TABLE IF NOT EXISTS ga4_device_metrics (
    id INT AUTO_INCREMENT PRIMARY KEY,
    date DATE NOT NULL,
    device_category VARCHAR(50) NOT NULL,
    sessions INT NOT NULL DEFAULT 0,
    users INT NOT NULL DEFAULT 0,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    UNIQUE KEY idx_date_device (date, device_category),
    KEY idx_date (date)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- GA4 country metrics
CREATE TABLE IF NOT EXISTS ga4_country_metrics (
    id INT AUTO_INCREMENT PRIMARY KEY,
    date DATE NOT NULL,
    country VARCHAR(255) NOT NULL,
    sessions INT NOT NULL DEFAULT 0,
    users INT NOT NULL DEFAULT 0,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    UNIQUE KEY idx_date_country (date, country),
    KEY idx_date (date),
    KEY idx_sessions (sessions)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- GA4 sync status tracking
CREATE TABLE IF NOT EXISTS ga4_sync_status (
    id INT AUTO_INCREMENT PRIMARY KEY,
    sync_type VARCHAR(50) NOT NULL,
    last_sync_date DATE NOT NULL,
    last_sync_at TIMESTAMP NOT NULL,
    status VARCHAR(50) NOT NULL,
    error_message TEXT,
    records_synced INT NOT NULL DEFAULT 0,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    UNIQUE KEY idx_sync_type (sync_type),
    KEY idx_last_sync_at (last_sync_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- +migrate Down

DROP TABLE IF EXISTS ga4_sync_status;
DROP TABLE IF EXISTS ga4_country_metrics;
DROP TABLE IF EXISTS ga4_device_metrics;
DROP TABLE IF EXISTS ga4_traffic_source_metrics;
DROP TABLE IF EXISTS ga4_product_page_metrics;
DROP TABLE IF EXISTS ga4_daily_metrics;
