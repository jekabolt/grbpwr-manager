-- +migrate Up
-- Add announce translations support
-- This migration adds internationalization support for site announcements

-- Create announce_translation table to store site announcements in different languages
CREATE TABLE announce_translation (
    id INT PRIMARY KEY AUTO_INCREMENT,
    language_id INT NOT NULL,
    text TEXT NOT NULL COMMENT 'Translated announcement text',
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP NOT NULL,
    updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP NOT NULL,
    UNIQUE KEY unique_announce_language (language_id),
    FOREIGN KEY (language_id) REFERENCES language(id) ON DELETE CASCADE
) COMMENT 'Site announcement translations for different languages';

-- Create indexes for optimized translation queries
CREATE INDEX idx_announce_translation_language_id ON announce_translation(language_id);

-- Insert default announcement for the default language (English) if needed
-- This can be updated via the admin interface
INSERT INTO announce_translation (language_id, text)
SELECT 
    id,
    ''
FROM language 
WHERE is_default = TRUE
LIMIT 1;

-- +migrate Down
-- Remove announce translations table
DROP TABLE IF EXISTS announce_translation;
