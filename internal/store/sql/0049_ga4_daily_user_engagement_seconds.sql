-- +migrate Up
-- Total foreground engagement time (seconds) per day from GA4 userEngagementDuration.
-- Used to compute period average session engagement as SUM(user_engagement_seconds)/SUM(sessions),
-- independent of the legacy avg_session_duration column.

ALTER TABLE ga4_daily_metrics
  ADD COLUMN user_engagement_seconds BIGINT NOT NULL DEFAULT 0 AFTER avg_session_duration;

-- +migrate Down

ALTER TABLE ga4_daily_metrics
  DROP COLUMN user_engagement_seconds;
