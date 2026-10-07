-- PARTS · the pieces list (2026-10-07, tmp/plans/flat-consistency 107-M6) — the closed list of part
-- names the parts labeller (chat.design_parts) may use, read by the same route from the card's
-- ACCEPTED FRONT/BACK flats (the plates of the FLAT bench slots — never the photos, never the join
-- list) and edited by the designer. Never sent to image generation.
--
-- ONE row per card. `pieces` is entity.DesignPartsPiecesDoc {pieces:[{name, views}], openings}; shape
-- closed in Go, no ENUM, no CHECK. source_front_media_id / source_back_media_id — the plates the list
-- (as read) came from (0 = that side had none). A READ NEVER WRITES OVER A DESIGNER'S EDITS: once
-- edited_at is set, a read of changed plates lands in `proposal` (entity.DesignPartsPiecesProposal)
-- until the designer takes or keeps it. `rev` is the CAS of the designer's save and the key the
-- labeller's cached answers are tagged with. The row goes with the card (CASCADE).
--
-- No route row: the read runs on chat.design_parts (0390). design_joins is left as it is (data only).
--
-- IDEMPOTENT: the table is created only if it does not exist; a half-applied file re-runs from the top.

-- +migrate Up

CREATE TABLE IF NOT EXISTS design_parts_pieces (
    id INT NOT NULL PRIMARY KEY AUTO_INCREMENT,
    tech_card_id INT NOT NULL,
    rev INT NOT NULL DEFAULT 1 COMMENT 'CAS revision; every write + 1',
    pieces JSON NOT NULL COMMENT 'entity.DesignPartsPiecesDoc: pieces [{name, views}], openings',
    source_front_media_id INT NOT NULL DEFAULT 0 COMMENT 'FRONT flat plate the list was read from; 0 = none',
    source_back_media_id INT NOT NULL DEFAULT 0 COMMENT 'BACK flat plate the list was read from; 0 = none',
    proposal JSON NULL COMMENT 'entity.DesignPartsPiecesProposal: a newer read held because a designer edited the list',
    model VARCHAR(128) NOT NULL DEFAULT '',
    edited_by VARCHAR(255) NOT NULL DEFAULT '',
    edited_at DATETIME(6) NULL COMMENT 'set when a designer saved the list; NULL = as the model read it',
    updated_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    CONSTRAINT uniq_design_parts_pieces_card UNIQUE (tech_card_id),
    CONSTRAINT fk_design_parts_pieces_card FOREIGN KEY (tech_card_id) REFERENCES tech_card(id) ON DELETE CASCADE
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4 COMMENT 'PARTS: the pieces list of a tech card';

-- +migrate Down

DROP TABLE IF EXISTS design_parts_pieces;
