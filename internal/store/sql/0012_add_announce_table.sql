-- +migrate Up
-- Create announce table to store global announcement settings
-- This includes a single link that applies to all translated announcements

CREATE TABLE announce (
    id INT PRIMARY KEY AUTO_INCREMENT,
    link VARCHAR(500) DEFAULT '' NOT NULL,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP NOT NULL,
    updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP NOT NULL
);

-- Insert default row (we only need one row in this table)
INSERT INTO announce (link) VALUES ('');

-- +migrate Down
-- Remove announce table
DROP TABLE IF EXISTS announce;

