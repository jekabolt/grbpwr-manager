-- +migrate Up

-- Links a construction operation to the cut-pieces it works on. Until now an operation carried only
-- `placement`, a free-text label ("collar", "воротник"), so nothing could join an operation to the
-- piece it actually assembles -- the cut list, the operation list and the recipe each named parts in
-- their own words.
--
-- A JOIN TABLE rather than a piece_id column on tech_card_operation, because the relation is
-- genuinely many-to-many: an assembly operation spans as many pieces as it joins (a shoulder seam
-- takes the front and the back; there is no useful bound). This is deliberately a different
-- cardinality from tech_card_colorway_usage.piece_id, which stays 1:1 -- a consumption norm is about
-- exactly one piece.
--
-- ON DELETE CASCADE on the operation side: the link is part of the operation, and the tech-card write
-- replaces operations wholesale, so a removed operation must take its links with it.
-- ON DELETE RESTRICT on the piece side: mirrors fk_op_bom / usage.piece_id (0159). A piece that an
-- operation references must not vanish silently underneath the spec -- the write path unlinks first.
--
-- Numbering note: 0189-0197 are claimed by unmerged accounting branches, and 0198 by the style
-- category backfill on this same branch, so this is 0199. The gap is intentional; do not "fix" it.
--
-- Idempotent: CREATE TABLE IF NOT EXISTS covers the table and its inline constraints, so a rerun is a
-- no-op. No data backfill is possible or wanted -- `placement` is free text and guessing which piece
-- a string meant would invent links the operator never made.

CREATE TABLE IF NOT EXISTS tech_card_operation_piece (
    id            INT PRIMARY KEY AUTO_INCREMENT,
    operation_id  INT NOT NULL COMMENT 'FK tech_card_operation(id)',
    piece_id      INT NOT NULL COMMENT 'FK tech_card_piece(id)',
    display_order INT NOT NULL DEFAULT 0 COMMENT 'order the client sent the pieces in',
    created_at    TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT fk_op_piece_operation FOREIGN KEY (operation_id) REFERENCES tech_card_operation(id) ON DELETE CASCADE,
    CONSTRAINT fk_op_piece_piece     FOREIGN KEY (piece_id)     REFERENCES tech_card_piece(id)     ON DELETE RESTRICT,
    CONSTRAINT uniq_op_piece UNIQUE (operation_id, piece_id),
    INDEX idx_op_piece_piece (piece_id)
) ENGINE = InnoDB COMMENT 'WS4: cut-pieces a construction operation works on (many-to-many)';

-- +migrate Down
DROP TABLE IF EXISTS tech_card_operation_piece;
