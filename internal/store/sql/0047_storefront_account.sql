-- +migrate Up
-- Storefront customer accounts: passwordless login (OTP + magic link), refresh sessions, saved addresses, access jti denylist.

CREATE TABLE storefront_account (
    id INT PRIMARY KEY AUTO_INCREMENT,
    email VARCHAR(100) NOT NULL UNIQUE,
    first_name VARCHAR(255) NOT NULL DEFAULT '',
    last_name VARCHAR(255) NOT NULL DEFAULT '',
    birth_date DATE NULL,
    shopping_preference ENUM('male', 'female', 'all') NULL DEFAULT NULL,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    CHECK (email REGEXP '^[a-zA-Z0-9._%+-]+@[a-zA-Z0-9.-]+\\.[a-zA-Z]{2,}$')
) COMMENT 'Customer storefront profile (login email)';

CREATE TABLE email_login_challenge (
    id BIGINT PRIMARY KEY AUTO_INCREMENT,
    email VARCHAR(100) NOT NULL,
    otp_code_hash CHAR(64) NOT NULL,
    magic_token_hash CHAR(64) NOT NULL,
    expires_at TIMESTAMP NOT NULL,
    consumed_at TIMESTAMP NULL,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    INDEX idx_email_login_challenge_email (email),
    INDEX idx_email_login_challenge_magic (magic_token_hash),
    INDEX idx_email_login_challenge_expires (expires_at)
) COMMENT 'One-time OTP + magic link credentials for passwordless login';

CREATE TABLE storefront_refresh_token (
    id BIGINT PRIMARY KEY AUTO_INCREMENT,
    account_id INT NOT NULL,
    token_hash CHAR(64) NOT NULL,
    family_id CHAR(36) NOT NULL,
    expires_at TIMESTAMP NOT NULL,
    revoked_at TIMESTAMP NULL,
    replaced_by_id BIGINT NULL,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE KEY uniq_storefront_refresh_token_hash (token_hash),
    INDEX idx_storefront_refresh_account (account_id),
    INDEX idx_storefront_refresh_family (family_id),
    FOREIGN KEY (account_id) REFERENCES storefront_account(id) ON DELETE CASCADE,
    FOREIGN KEY (replaced_by_id) REFERENCES storefront_refresh_token(id) ON DELETE SET NULL
) COMMENT 'Opaque refresh tokens with rotation and reuse detection';

CREATE TABLE storefront_saved_address (
    id INT PRIMARY KEY AUTO_INCREMENT,
    account_id INT NOT NULL,
    label VARCHAR(100) NOT NULL DEFAULT '',
    country VARCHAR(255) NOT NULL,
    state VARCHAR(255) NULL,
    city VARCHAR(255) NOT NULL,
    address_line_one VARCHAR(255) NOT NULL,
    address_line_two VARCHAR(255) NULL,
    company VARCHAR(255) NULL,
    postal_code VARCHAR(20) NOT NULL,
    is_default BOOLEAN NOT NULL DEFAULT FALSE,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    FOREIGN KEY (account_id) REFERENCES storefront_account(id) ON DELETE CASCADE,
    INDEX idx_storefront_saved_address_account (account_id)
) COMMENT 'Saved shipping addresses for storefront accounts';

CREATE TABLE storefront_access_jti_denylist (
    jti CHAR(36) PRIMARY KEY,
    account_id INT NOT NULL,
    expires_at TIMESTAMP NOT NULL,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    INDEX idx_storefront_access_jti_denylist_expires (expires_at),
    FOREIGN KEY (account_id) REFERENCES storefront_account(id) ON DELETE CASCADE
) COMMENT 'Revoked access token jti values; rows can be pruned after expires_at';

-- +migrate Down

DROP TABLE IF EXISTS storefront_access_jti_denylist;
DROP TABLE IF EXISTS storefront_saved_address;
DROP TABLE IF EXISTS storefront_refresh_token;
DROP TABLE IF EXISTS email_login_challenge;
DROP TABLE IF EXISTS storefront_account;
