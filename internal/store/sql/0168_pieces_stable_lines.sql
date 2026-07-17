-- +migrate Up
-- PLM-rework WS4 / S8 + the deferred half of 0159: cut-pieces (tech_card_piece) had a stable PK but
-- were FULL-REPLACED (delete-all + reinsert with new ids) by UpdateTechCard, so a colourway recipe
-- usage referencing a piece (tech_card_colorway_usage.piece_id, backfilled in 0159) would dangle
-- after every piece save — which is exactly why 0159 added bom_item_id's FK RESTRICT but deliberately
-- left piece_id's FK OUT (see the note at the tail of 0159). WS4 gives each cut-piece a stable wire
-- identity (line_key) so the store keyed-upserts it (id stays stable), and only THEN lands the
-- deferred fk_usage_piece ... ON DELETE RESTRICT.
--
-- `detached` marks a piece whose source sketch callout was removed: orphan-control keeps a piece that
-- still has recipe history (referenced by a usage) visibly detached instead of silently dropping it.
--
-- Additive + backfill + FK in one file: the store write-path becomes a keyed upsert-diff in the same
-- change, so a full-replace that deleted a piece a usage referenced would now hit the RESTRICT (an
-- actionable field-tagged error, not a silent dangle). Crash-idempotent: guarded on information_schema;
-- the backfill is re-runnable (WHERE ... IS NULL); dangling piece_id is NULLed BEFORE the FK so it
-- cannot fail on prod data (NULL bypasses the constraint), the same prod-safety pattern as 0159.

-- --- tech_card_piece: line_key + detached (guarded on line_key) ---
SET @need := (SELECT COUNT(*) = 0 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_piece' AND COLUMN_NAME = 'line_key');
SET @sql := IF(@need,
    'ALTER TABLE tech_card_piece ADD COLUMN line_key CHAR(26) NULL, ADD COLUMN detached BOOLEAN NOT NULL DEFAULT FALSE',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

-- Backfill a deterministic, unique 26-char line_key for legacy rows (real ULIDs come from clients on
-- new pieces). 'LEGACY' + zero-padded id = 26 chars; id is unique so the key is unique.
UPDATE tech_card_piece SET line_key = CONCAT('LEGACY', LPAD(id, 20, '0'))
    WHERE line_key IS NULL OR line_key = '';

-- UNIQUE (tech_card_id, line_key): the upsert-diff matches on it, so the invariant must hold now.
SET @need := (SELECT COUNT(*) = 0 FROM information_schema.STATISTICS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_piece' AND INDEX_NAME = 'uniq_piece_line_key');
SET @sql := IF(@need,
    'ALTER TABLE tech_card_piece ADD CONSTRAINT uniq_piece_line_key UNIQUE (tech_card_id, line_key)',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

-- NULL out any dangling usage.piece_id (a piece_index converted to an id in 0159 that a later
-- full-replace then invalidated) so the RESTRICT FK below cannot fail on prod data.
UPDATE tech_card_colorway_usage u
    LEFT JOIN tech_card_piece p ON p.id = u.piece_id
    SET u.piece_id = NULL
    WHERE u.piece_id IS NOT NULL AND p.id IS NULL;

-- --- the deferred FK from 0159: usage.piece_id -> tech_card_piece(id) RESTRICT. Safe to add now that
--     pieces are keyed (stable ids) and the store upsert-diffs them, so future writes can't orphan a
--     referenced piece. Guarded per constraint name. ---
SET @need := (SELECT COUNT(*) = 0 FROM information_schema.TABLE_CONSTRAINTS
    WHERE TABLE_SCHEMA = DATABASE() AND CONSTRAINT_NAME = 'fk_usage_piece');
SET @sql := IF(@need,
    'ALTER TABLE tech_card_colorway_usage ADD CONSTRAINT fk_usage_piece FOREIGN KEY (piece_id) REFERENCES tech_card_piece(id) ON DELETE RESTRICT',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

-- +migrate Down
ALTER TABLE tech_card_colorway_usage DROP FOREIGN KEY fk_usage_piece;
ALTER TABLE tech_card_piece DROP INDEX uniq_piece_line_key, DROP COLUMN detached, DROP COLUMN line_key;
