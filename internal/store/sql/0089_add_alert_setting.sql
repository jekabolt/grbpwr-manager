-- +migrate Up
-- Operator-tunable thresholds for the server-computed dashboard alerts (#6/#7). Moving these
-- off the frontend keeps alerting consistent across clients and lets an admin adjust them
-- without a deploy. Key/value (numeric) so new thresholds can be added without a schema change.
-- Seeded with the same defaults hardcoded in entity.DefaultAlertThresholds so behaviour is
-- unchanged until an operator edits them.
CREATE TABLE alert_setting (
  setting_key VARCHAR(191) NOT NULL PRIMARY KEY,
  value DECIMAL(12, 4) NOT NULL,
  updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4;

INSERT INTO alert_setting (setting_key, value) VALUES
  ('coverage_warn_pct', 70.0),
  ('refund_rate_warn_pct', 10.0),
  ('rate_floor_n', 30),
  ('contribution_trust_pct', 50.0);

-- +migrate Down
DROP TABLE IF EXISTS alert_setting;
