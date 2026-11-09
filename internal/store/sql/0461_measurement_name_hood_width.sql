-- Hood width, across the hood at its widest point: hooded size chart icons draw it next to
-- hood-height. INSERT IGNORE makes a retry a no-op. Safe under ANSI_QUOTES (single quotes only).

-- +migrate Up

INSERT IGNORE INTO measurement_name (name) VALUES ('hood-width');

-- +migrate Down

-- The name stays, like the names 0459 added: rows of many tables may already point at it
-- (ON DELETE RESTRICT), and an unused dictionary name is harmless.
SELECT 1;
