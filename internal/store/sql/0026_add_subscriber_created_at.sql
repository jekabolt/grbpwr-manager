-- +migrate Up
-- Migration: Add created_at to subscriber for "new subscribers per period" metric
-- Purpose: Track when each subscriber was added to enable period-based reporting
-- Affected tables: subscriber
-- Note: Existing rows get NULL (unknown signup date); new inserts set created_at explicitly

ALTER TABLE subscriber
ADD COLUMN created_at TIMESTAMP NULL DEFAULT NULL
COMMENT 'When the subscriber was first added; NULL for legacy rows';

-- +migrate Down
ALTER TABLE subscriber DROP COLUMN created_at;
