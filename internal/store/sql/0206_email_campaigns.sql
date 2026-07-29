-- +migrate Up
-- Phase 0 foundation for admin-authored email campaigns. Campaign and variant
-- bodies are stored as ordered typed-block JSON blobs; recipients, delivery
-- state, metrics, and aggregates are intentionally deferred to later phases.

CREATE TABLE IF NOT EXISTS email_segment (
    id            INT PRIMARY KEY AUTO_INCREMENT,
    name          VARCHAR(255) NOT NULL,
    description   TEXT NOT NULL,
    predicate     JSON NOT NULL,
    last_count    INT NULL,
    last_count_at DATETIME NULL,
    created_by    VARCHAR(255) NOT NULL,
    created_at    TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at    TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    CONSTRAINT uniq_email_segment_name UNIQUE (name)
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4 COLLATE = utf8mb4_unicode_ci COMMENT 'Saved audience predicates for email campaigns';

CREATE TABLE IF NOT EXISTS email_campaign (
    id                        INT PRIMARY KEY AUTO_INCREMENT,
    name                      VARCHAR(255) NOT NULL,
    status                    ENUM('draft', 'scheduled', 'sending', 'paused', 'sent', 'cancelled') NOT NULL DEFAULT 'draft',
    segment_id                INT NULL,
    topic                     ENUM('newsletter', 'new_arrivals', 'events') NOT NULL,
    body                      JSON NOT NULL,
    background_color          VARCHAR(32) NOT NULL DEFAULT '',
    from_name                 VARCHAR(255) NULL,
    from_email                VARCHAR(255) NULL,
    reply_to                  VARCHAR(255) NULL,
    schedule_at               DATETIME NULL,
    ab_enabled                BOOLEAN NOT NULL DEFAULT FALSE,
    ab_dimension              ENUM('subject', 'content') NULL,
    ab_test_pct               TINYINT UNSIGNED NOT NULL DEFAULT 0,
    ab_decision_after_minutes INT NOT NULL DEFAULT 0,
    ab_winner_variant_id      INT NULL,
    sending_started_at        DATETIME NULL,
    sent_at                   DATETIME NULL,
    created_by                VARCHAR(255) NOT NULL,
    created_at                TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at                TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    CONSTRAINT fk_email_campaign_segment FOREIGN KEY (segment_id) REFERENCES email_segment(id) ON DELETE SET NULL,
    INDEX idx_email_campaign_status_topic (status, topic),
    INDEX idx_email_campaign_segment (segment_id),
    INDEX idx_email_campaign_schedule (schedule_at)
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4 COLLATE = utf8mb4_unicode_ci COMMENT 'Email campaign definition and campaign-level body';

CREATE TABLE IF NOT EXISTS email_campaign_variant (
    id           INT PRIMARY KEY AUTO_INCREMENT,
    campaign_id  INT NOT NULL,
    label        VARCHAR(8) NOT NULL,
    subject_i18n JSON NOT NULL,
    body         JSON NULL,
    is_winner    BOOLEAN NOT NULL DEFAULT FALSE,
    created_at   TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at   TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    CONSTRAINT fk_email_campaign_variant_campaign FOREIGN KEY (campaign_id) REFERENCES email_campaign(id) ON DELETE CASCADE,
    CONSTRAINT uniq_email_campaign_variant_label UNIQUE (campaign_id, label)
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4 COLLATE = utf8mb4_unicode_ci COMMENT 'Subject/content variants belonging to an email campaign';

-- +migrate Down
DROP TABLE IF EXISTS email_campaign_variant;
DROP TABLE IF EXISTS email_campaign;
DROP TABLE IF EXISTS email_segment;
