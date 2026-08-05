-- +migrate Up
-- Fitting change requests get (a) many pieces instead of one, and (b) a garment-area zone vocabulary
-- of their own.
--
-- (a) One remark routinely spans several cut-pieces — «обработка низа бейкой на полочке и спинке» is
--     ONE decision about TWO pieces. The single fitting_change_request.piece_id column (0171) forced
--     splitting it into duplicate rows that then drift apart across rounds (each carries separately).
--     The column stays as read-only history; this table is the live set, backfilled from it below.
--
-- (b) chk_fcr_zone (0171) constrained zone to the tech_card_operation.zone dictionary (0076), which
--     groups SEWING OPERATIONS into material bands: outer | lining | interlining | other. A fitting
--     remark is about where on the GARMENT the problem is ("рукав короткий", "жмёт в плече") and
--     those bands cannot express it, so the front-end had nothing usable to pick. The bands are kept
--     (a remark can genuinely be about the lining as a layer) and the garment areas are added; the
--     two dictionaries are now independent, and entity.ValidFittingChangeZones is the Go mirror of
--     the list below. Widening a CHECK cannot fail on existing data — every stored value is from the
--     old set, which is a subset of the new one.
--
-- Crash-idempotent: guarded CREATE / constraint swaps, and a re-runnable INSERT..SELECT backfill.

-- NO table charset clause on purpose — prod/beta run the utf8mb3 server default while local
-- container tests connect utf8mb4 (see 0252). All columns here are integers, but the rule is the
-- table's, not the column's.
CREATE TABLE IF NOT EXISTS fitting_change_request_piece (
    id                INT PRIMARY KEY AUTO_INCREMENT,
    change_request_id INT NOT NULL,
    piece_id          INT NOT NULL COMMENT 'FK tech_card_piece(id); the cut-piece this remark is about',
    display_order     INT NOT NULL DEFAULT 0 COMMENT 'selection order, so the chips read back the way they were picked',
    -- A piece is pinned to a remark once. Re-picking it is a no-op, not a second row.
    CONSTRAINT uniq_fcrp_request_piece UNIQUE (change_request_id, piece_id),
    -- CASCADE: the pins are owned by the remark and have no meaning without it.
    CONSTRAINT fk_fcrp_request FOREIGN KEY (change_request_id) REFERENCES fitting_change_request(id) ON DELETE CASCADE,
    -- CASCADE is the set-shaped analogue of the 0171 column's ON DELETE SET NULL: deleting a piece
    -- drops that ONE pin and leaves the remark (and its other pins) standing.
    CONSTRAINT fk_fcrp_piece FOREIGN KEY (piece_id) REFERENCES tech_card_piece(id) ON DELETE CASCADE
) ENGINE=InnoDB COMMENT 'Which cut-pieces a fitting change request is about (replaces the single fitting_change_request.piece_id from 0171)';

-- Backfill the legacy single pin. Re-runnable: the NOT EXISTS guard makes a second pass a no-op, and
-- it also protects rows a partially-applied run already copied.
INSERT INTO fitting_change_request_piece (change_request_id, piece_id, display_order)
SELECT cr.id, cr.piece_id, 0
FROM fitting_change_request cr
WHERE cr.piece_id IS NOT NULL
  AND NOT EXISTS (
      SELECT 1 FROM fitting_change_request_piece p
      WHERE p.change_request_id = cr.id AND p.piece_id = cr.piece_id
  );

-- zone: swap chk_fcr_zone (0171) for the widened fitting dictionary. Dropped by its explicit name,
-- never an auto-generated <table>_chk_<n>. Guarded on both sides so a crash between the two
-- statements re-runs cleanly.
SET @has := (SELECT COUNT(*) FROM information_schema.TABLE_CONSTRAINTS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'fitting_change_request' AND CONSTRAINT_NAME = 'chk_fcr_zone');
SET @sql := IF(@has,
    'ALTER TABLE fitting_change_request DROP CHECK chk_fcr_zone',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

SET @has := (SELECT COUNT(*) FROM information_schema.TABLE_CONSTRAINTS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'fitting_change_request' AND CONSTRAINT_NAME = 'chk_fcr_zone_v2');
SET @sql := IF(@has = 0,
    'ALTER TABLE fitting_change_request ADD CONSTRAINT chk_fcr_zone_v2 CHECK (zone IS NULL OR zone REGEXP ''^(unknown|outer|lining|interlining|sleeve|collar|neckline|armhole|shoulder|chest|waist|hip|hem|pocket|closure|back|front|other)$'')',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

-- +migrate Down
DROP TABLE IF EXISTS fitting_change_request_piece;

SET @has := (SELECT COUNT(*) FROM information_schema.TABLE_CONSTRAINTS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'fitting_change_request' AND CONSTRAINT_NAME = 'chk_fcr_zone_v2');
SET @sql := IF(@has,
    'ALTER TABLE fitting_change_request DROP CHECK chk_fcr_zone_v2',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

-- Rows written under the widened dictionary would violate the narrow one, so blank them before
-- restoring it — a down-migration must not leave the table unable to satisfy its own CHECK.
UPDATE fitting_change_request SET zone = NULL
    WHERE zone IS NOT NULL AND zone NOT REGEXP '^(unknown|outer|lining|interlining|other)$';

SET @has := (SELECT COUNT(*) FROM information_schema.TABLE_CONSTRAINTS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'fitting_change_request' AND CONSTRAINT_NAME = 'chk_fcr_zone');
SET @sql := IF(@has = 0,
    'ALTER TABLE fitting_change_request ADD CONSTRAINT chk_fcr_zone CHECK (zone IS NULL OR zone REGEXP ''^(unknown|outer|lining|interlining|other)$'')',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;
