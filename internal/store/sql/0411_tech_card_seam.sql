-- CONFIRMED SEAMS (2026-10-10, tmp/plans/assembly-3d-doll/03-SEAMS-DESIGN.md §3.1).
--
-- WHAT IT STORES. A technologist accepts or rejects the seams the assembly engine proposes, or
-- connects two edges by hand on the pieces map. Until now that judgement did not exist anywhere:
-- the doll, the skeleton, the assembly map and the POM engine guessed the seams again on every open.
-- One row = one decision, keyed by a client-minted ULID (seam_key).
--
-- AN EDGE IS NOT ADDRESSED BY ITS CORNER NUMBER. `pieceKey#k` is counted from the DXF's first vertex
-- and shifts with the exporter's start point or with any corner appearing. Each side is a JSON list
-- of ANCHORS (piece line_key + the edge's shape in the piece's own frame + length / notches / turn),
-- resolved geometrically by the client. A side is read whole and nobody joins on an anchor, hence
-- JSON and not an anchor table. NOT NULL — a NULL JSON value is the classic way to break a whole
-- table read; a seam with no side is not a seam.
--
-- WHY A TABLE AND NOT A COLUMN ON THE CARD. Rows are upserted one at a time by a reviewer while the
-- form autosaves the card every few seconds; a card-level JSON would ride UpdateTechCard, enter the
-- lock_version race and be clobbered by a stale tab. The seams RPCs never bump lock_version and the
-- rows enter no section digest (the same ruling 0410 made for `draft`).
--
-- NO FK TO THE PIECE: line_key is the contract's identity (0297's argument); a deleted piece leaves
-- an orphan row the screen offers to remove, instead of a RESTRICT that would abort a card save.
-- FK to the card ON DELETE CASCADE, as every child table of tech_card.
--
-- source_fingerprint IS SERVER-WRITTEN (entity.PieceAreaSourceFingerprint over the sheets + block
-- links of the seam's pieces' fabric scopes); a read recomputes it and reports `stale`.
--
-- size_id is RESERVED (NULL = every size, no per-size rows in v1); the generated size_key keeps the
-- UNIQUE honest over NULL, the device 0297 uses.
--
-- ADDITIVE ONLY: a new table, no CHECK (dictionaries are enforced in Go with field-addressed errors),
-- no backfill, no charset clause (0252/0280/0297 precedent). The old binary never reads the table, so
-- a rollback is safe. CREATE TABLE IF NOT EXISTS — idempotent, one statement.

-- +migrate Up

CREATE TABLE IF NOT EXISTS tech_card_seam (
    id INT AUTO_INCREMENT PRIMARY KEY,
    tech_card_id INT NOT NULL,
    seam_key CHAR(26) NOT NULL,
    status VARCHAR(12) NOT NULL,
    kind VARCHAR(12) NOT NULL,
    direction VARCHAR(10) NOT NULL DEFAULT 'reversed',
    source VARCHAR(10) NOT NULL,
    side_a JSON NOT NULL,
    side_b JSON NOT NULL,
    size_id INT NULL,
    size_key INT AS (COALESCE(size_id, 0)) STORED,
    anchored_size VARCHAR(16) NOT NULL DEFAULT '',
    note VARCHAR(255) NOT NULL DEFAULT '',
    source_fingerprint CHAR(64) NOT NULL,
    created_by VARCHAR(255) NOT NULL DEFAULT '',
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_by VARCHAR(255) NOT NULL DEFAULT '',
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    CONSTRAINT uniq_tcs_card_key UNIQUE (tech_card_id, seam_key, size_key),
    CONSTRAINT fk_tcs_tech_card FOREIGN KEY (tech_card_id) REFERENCES tech_card (id) ON DELETE CASCADE,
    INDEX idx_tcs_card (tech_card_id)
) ENGINE=InnoDB;

-- +migrate Down

DROP TABLE IF EXISTS tech_card_seam;
