-- +migrate Up
-- PLM-rework WS6 / Q7 + S26. The fitting is the EVENT on a sample (§2.7). Give it the cross-cutting
-- audit stamps (§2.11) and the optimistic-lock counter (§2.12/S25), and backfill created_by from the
-- deprecated client-supplied recorded_by so its history is preserved before recorded_by is dropped
-- (0172). recorded_by stays for now (dropped behind a guard in 0172).
--
-- S26 (structured fitting remarks, added by the owner 2026-07-17): the free comment stays a free note;
-- structured items become first-class. Extend fitting_change_request with:
--   zone            — WHERE on the garment, reusing the tech_card_operation.zone dictionary (0076),
--                     not a new enum (§2.7: single source of truth).
--   piece_id        — WHICH piece (FK tech_card_piece), SET NULL if the piece is deleted.
--   status          — open|resolved, replacing the boolean `resolved` (dropped in 0172 after backfill).
--   carried_from_id — the item in a PREVIOUS round this one continues (self-FK), giving a visible
--                     carry-over history (acceptance E.15): raised in round N -> carried to round N+1.
--   created_by      — audit stamp (server-set).
--
-- Crash-idempotent: every ADD/backfill is guarded or re-runnable; no new tables here.

-- fitting: audit + lock (atomic multi-column ALTER, guarded on the sentinel column lock_version).
SET @need := (SELECT COUNT(*) = 0 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'fitting' AND COLUMN_NAME = 'lock_version');
SET @sql := IF(@need,
    'ALTER TABLE fitting
        ADD COLUMN created_by VARCHAR(255) NOT NULL DEFAULT '''',
        ADD COLUMN updated_by VARCHAR(255) NOT NULL DEFAULT '''',
        ADD COLUMN lock_version INT NOT NULL DEFAULT 0',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

-- Preserve the deprecated recorded_by (client-supplied "who recorded") into the server-audit created_by
-- before it is dropped in 0172. Re-runnable: only fills rows still at the default empty stamp.
UPDATE fitting SET created_by = recorded_by
    WHERE created_by = '' AND recorded_by IS NOT NULL AND recorded_by <> '';
UPDATE fitting SET updated_by = recorded_by
    WHERE updated_by = '' AND recorded_by IS NOT NULL AND recorded_by <> '';

-- fitting_change_request: S26 structured-remark columns (atomic ALTER, guarded on the sentinel `status`).
SET @need := (SELECT COUNT(*) = 0 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'fitting_change_request' AND COLUMN_NAME = 'status');
SET @sql := IF(@need,
    'ALTER TABLE fitting_change_request
        ADD COLUMN zone VARCHAR(24) NULL,
        ADD COLUMN piece_id INT NULL,
        ADD COLUMN carried_from_id INT NULL,
        ADD COLUMN created_by VARCHAR(255) NOT NULL DEFAULT '''',
        ADD COLUMN status VARCHAR(12) NOT NULL DEFAULT ''open''',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

-- Backfill status from the boolean resolved before that column is dropped (0172). Re-runnable.
UPDATE fitting_change_request SET status = 'resolved' WHERE resolved = TRUE AND status <> 'resolved';

-- status CHECK (named explicitly so it is stable across schema history).
SET @need := (SELECT COUNT(*) = 0 FROM information_schema.TABLE_CONSTRAINTS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'fitting_change_request' AND CONSTRAINT_NAME = 'chk_fcr_status');
SET @sql := IF(@need,
    'ALTER TABLE fitting_change_request ADD CONSTRAINT chk_fcr_status CHECK (status REGEXP ''^(open|resolved)$'')',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

-- zone CHECK: NULL (unspecified) or a value from the tech_card_operation.zone dictionary (0076).
SET @need := (SELECT COUNT(*) = 0 FROM information_schema.TABLE_CONSTRAINTS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'fitting_change_request' AND CONSTRAINT_NAME = 'chk_fcr_zone');
SET @sql := IF(@need,
    'ALTER TABLE fitting_change_request ADD CONSTRAINT chk_fcr_zone CHECK (zone IS NULL OR zone REGEXP ''^(unknown|outer|lining|interlining|other)$'')',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

-- piece_id FK -> tech_card_piece (SET NULL: deleting a piece keeps the remark, drops the pin).
SET @need := (SELECT COUNT(*) = 0 FROM information_schema.TABLE_CONSTRAINTS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'fitting_change_request' AND CONSTRAINT_NAME = 'fk_fcr_piece');
SET @sql := IF(@need,
    'ALTER TABLE fitting_change_request ADD CONSTRAINT fk_fcr_piece FOREIGN KEY (piece_id) REFERENCES tech_card_piece(id) ON DELETE SET NULL',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

-- carried_from_id FK -> self (SET NULL: deleting the predecessor keeps the successor, drops the link).
SET @need := (SELECT COUNT(*) = 0 FROM information_schema.TABLE_CONSTRAINTS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'fitting_change_request' AND CONSTRAINT_NAME = 'fk_fcr_carried');
SET @sql := IF(@need,
    'ALTER TABLE fitting_change_request ADD CONSTRAINT fk_fcr_carried FOREIGN KEY (carried_from_id) REFERENCES fitting_change_request(id) ON DELETE SET NULL',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

-- +migrate Down
ALTER TABLE fitting_change_request DROP FOREIGN KEY fk_fcr_piece;
ALTER TABLE fitting_change_request DROP FOREIGN KEY fk_fcr_carried;
ALTER TABLE fitting_change_request DROP CONSTRAINT chk_fcr_status;
ALTER TABLE fitting_change_request DROP CONSTRAINT chk_fcr_zone;
ALTER TABLE fitting_change_request
    DROP COLUMN zone,
    DROP COLUMN piece_id,
    DROP COLUMN carried_from_id,
    DROP COLUMN created_by,
    DROP COLUMN status;
ALTER TABLE fitting
    DROP COLUMN created_by,
    DROP COLUMN updated_by,
    DROP COLUMN lock_version;
