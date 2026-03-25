-- +migrate Up
-- Fix GA4 bounce_rate scale: convert from ratio (0-1) to percentage (0-100).
-- GA4 API returns bounce rate as a ratio (e.g., 0.4872 = 48.72%), but the frontend
-- expects percentage values. This migration changes the column type first to support
-- 0-100 range, then multiplies existing values by 100.

-- Step 1: Change column type to support 0-100 range (was DECIMAL(5,4), now DECIMAL(5,2))
ALTER TABLE ga4_daily_metrics
  MODIFY COLUMN bounce_rate DECIMAL(5,2) NOT NULL DEFAULT 0
  COMMENT 'Bounce rate as percentage (0-100)';

-- Step 2: Multiply existing values by 100 (convert ratio to percentage)
UPDATE ga4_daily_metrics
SET bounce_rate = bounce_rate * 100
WHERE bounce_rate < 1.01;

-- +migrate Down
-- Revert to ratio (0-1) scale

ALTER TABLE ga4_daily_metrics
  MODIFY COLUMN bounce_rate DECIMAL(5,4) NOT NULL DEFAULT 0;

UPDATE ga4_daily_metrics
SET bounce_rate = bounce_rate / 100
WHERE bounce_rate > 1.0;
