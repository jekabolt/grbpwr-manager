-- +migrate Up
-- PLM-rework WS6 / Q7 (round-spine) + Q2 (dev-cost invariant, unchanged). A sample is the OBJECT of a
-- development round; a fitting is an EVENT on it (they are NOT merged — object vs event, §2.7). Give the
-- sample the round spine: round_number (the style's iteration index), spec_release_id (the immutable
-- tech_card_release snapshot the sample was sewn from — reuses the existing release mechanism §2.1, not
-- a new entity) and previous_sample_id (the prior round's sample, the iteration chain). Add the
-- cross-cutting audit stamps (§2.11) and the optimistic-lock counter (§2.12/S25).
--
-- Also add sample_substitution (§2.7): "in this sample, BOM line X was sewn with material Y instead of
-- its spec material". A dev-only deviation record — Q2 invariant holds: substitutions never touch
-- product.cost_price; the authoritative spend stays in material_stock_movement (actual) and the BOM
-- line (plan). planned_qty/actual_qty here are optional documentation of the substitution itself.
-- bom_item_id is ON DELETE RESTRICT: BOM lines are stable (0159 line_key), and a referenced line must
-- not be silently deleted out from under a substitution that pins it.
--
-- Crash-idempotent: MySQL 8 has no ADD COLUMN IF NOT EXISTS, so every ADD is guarded on
-- information_schema (a retried partial apply is a no-op); the new table is created only IF NOT EXISTS.
-- All new columns are nullable / defaulted, so the ALTER is safe against existing rows (additive, M1).

-- sample: round spine + audit + lock. One atomic multi-column ALTER (MySQL applies it whole or not at
-- all), guarded on the sentinel column lock_version (added last).
SET @need := (SELECT COUNT(*) = 0 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'sample' AND COLUMN_NAME = 'lock_version');
SET @sql := IF(@need,
    'ALTER TABLE sample
        ADD COLUMN round_number INT NULL,
        ADD COLUMN spec_release_id INT NULL,
        ADD COLUMN previous_sample_id INT NULL,
        ADD COLUMN created_by VARCHAR(255) NOT NULL DEFAULT '''',
        ADD COLUMN updated_by VARCHAR(255) NOT NULL DEFAULT '''',
        ADD COLUMN lock_version INT NOT NULL DEFAULT 0',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

-- spec_release_id FK -> tech_card_release (SET NULL: a deleted release keeps the sample, drops the link).
SET @need := (SELECT COUNT(*) = 0 FROM information_schema.TABLE_CONSTRAINTS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'sample' AND CONSTRAINT_NAME = 'fk_sample_spec_release');
SET @sql := IF(@need,
    'ALTER TABLE sample ADD CONSTRAINT fk_sample_spec_release FOREIGN KEY (spec_release_id) REFERENCES tech_card_release(id) ON DELETE SET NULL',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

-- previous_sample_id FK -> sample self (SET NULL: deleting an earlier sample keeps later rounds).
SET @need := (SELECT COUNT(*) = 0 FROM information_schema.TABLE_CONSTRAINTS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'sample' AND CONSTRAINT_NAME = 'fk_sample_previous');
SET @sql := IF(@need,
    'ALTER TABLE sample ADD CONSTRAINT fk_sample_previous FOREIGN KEY (previous_sample_id) REFERENCES sample(id) ON DELETE SET NULL',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

-- Index for the per-card round lookups (auto-numbering MAX+1, carry-over, chain walks). Not unique: a
-- round may sew more than one sample (sizes/colourways), so (tech_card_id, round_number) is not a key.
SET @need := (SELECT COUNT(*) = 0 FROM information_schema.STATISTICS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'sample' AND INDEX_NAME = 'idx_sample_round');
SET @sql := IF(@need,
    'ALTER TABLE sample ADD INDEX idx_sample_round (tech_card_id, round_number)',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

-- sample_substitution: a physical dev-time deviation from the spec BOM on a specific sample (§2.7).
CREATE TABLE IF NOT EXISTS sample_substitution (
    id                      INT AUTO_INCREMENT PRIMARY KEY,
    sample_id               INT NOT NULL,                       -- the sample this substitution was made on
    bom_item_id             INT NULL,                           -- which stable BOM line (0159) was replaced
    original_material_id    INT NULL,                           -- snapshot of the material the line specified
    substituted_material_id INT NULL,                           -- the material actually used instead
    reason                  VARCHAR(255) NULL,                  -- why (out of stock, trial, ...)
    planned_qty             DECIMAL(12,3) NULL,                 -- optional: planned consumption of the substitute
    actual_qty              DECIMAL(12,3) NULL,                 -- optional: actual consumption of the substitute
    created_by              VARCHAR(255) NOT NULL DEFAULT '',   -- audit: acting admin username (server-stamped)
    created_at              TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT fk_subst_sample   FOREIGN KEY (sample_id) REFERENCES sample(id) ON DELETE CASCADE,
    CONSTRAINT fk_subst_bom_item FOREIGN KEY (bom_item_id) REFERENCES tech_card_bom_item(id) ON DELETE RESTRICT,
    CONSTRAINT fk_subst_orig_mat FOREIGN KEY (original_material_id) REFERENCES material(id) ON DELETE SET NULL,
    CONSTRAINT fk_subst_repl_mat FOREIGN KEY (substituted_material_id) REFERENCES material(id) ON DELETE SET NULL,
    INDEX idx_subst_sample (sample_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci
  COMMENT='Dev-time material substitution on a sample (Q2: never COGS)';

-- +migrate Down
DROP TABLE IF EXISTS sample_substitution;
ALTER TABLE sample DROP FOREIGN KEY fk_sample_spec_release;
ALTER TABLE sample DROP FOREIGN KEY fk_sample_previous;
ALTER TABLE sample DROP INDEX idx_sample_round;
ALTER TABLE sample
    DROP COLUMN round_number,
    DROP COLUMN spec_release_id,
    DROP COLUMN previous_sample_id,
    DROP COLUMN created_by,
    DROP COLUMN updated_by,
    DROP COLUMN lock_version;
