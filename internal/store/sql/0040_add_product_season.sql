-- +migrate Up
ALTER TABLE product ADD COLUMN season VARCHAR(2) NOT NULL DEFAULT 'SS' CHECK (
    season IN ('SS', 'FW', 'PF', 'RC')
);

-- Backfill existing products with default season 'SS' (Spring/Summer)
UPDATE product SET season = 'SS' WHERE season = 'SS';

-- +migrate Down
ALTER TABLE product DROP COLUMN season;