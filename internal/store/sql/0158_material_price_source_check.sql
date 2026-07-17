-- +migrate Up
-- PLM-rework WS3 / S16 (A3.4): material_price.source was the only PLM field with NO validation at
-- all — a typo silently entered the append-only price history and then read back as a bogus
-- provenance. Add a named CHECK enumerating the three real sources (manual | production_run |
-- purchase); the app also validates it (field-tagged) before the DB would. Guarded/idempotent on
-- the constraint name. Existing rows only ever carry code-set values, so the CHECK is safe to add.

SET @need := (SELECT COUNT(*) = 0 FROM information_schema.TABLE_CONSTRAINTS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'material_price' AND CONSTRAINT_NAME = 'chk_material_price_source');
SET @sql := IF(@need,
    'ALTER TABLE material_price ADD CONSTRAINT chk_material_price_source CHECK (source REGEXP ''^(manual|production_run|purchase)$'')',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

-- +migrate Down
ALTER TABLE material_price DROP CONSTRAINT chk_material_price_source;
