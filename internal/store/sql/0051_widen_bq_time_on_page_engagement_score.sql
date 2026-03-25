-- +migrate Up
-- Description: Widen avg_engagement_score on bq_time_on_page.
-- DECIMAL(5,3) allows at most 99.999; GA4 time_on_page engagement_score averages can exceed 100.

ALTER TABLE bq_time_on_page
  MODIFY COLUMN avg_engagement_score DECIMAL(10,4) NULL DEFAULT 0.0000;

-- +migrate Down
-- Rollback is unsafe if any row has avg_engagement_score > 99.999; truncate or skip Down in that case.

ALTER TABLE bq_time_on_page
  MODIFY COLUMN avg_engagement_score DECIMAL(5,3) NULL DEFAULT 0.000;
