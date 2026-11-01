-- PIECE FOLDS (2026-11-01, tmp/plans/doll-folds/02-FOLD-SYNTHESIS.md, SPEC-T2).
--
-- WHAT IT STORES. A fold line on one piece of the card: a placket strip folded in half, a cuff, a hem
-- turned up. Construction data the paper doll folds BEFORE the solve — the solver never discovers or
-- moves a fold. One row = one straight line, applied in `seq` order on its piece: a double turn-up is
-- two rows (seq 1, 2), a grown-on placket folded twice is two rows. Keyed by a client-minted ULID.
--
-- GEOMETRY IN THE PIECE'S OWN FRAME, as the seams (0411): `line_uv` = two (u, v) points in the piece's
-- grain-upright bbox-normalised frame, `flip_at` = a point on the part that flips (which side moves
-- is data). The optional EDGE LOCATOR (edge_samples = five (u, v) points of the edge the fold runs
-- along, offset_mm = its distance from that edge, edge_hint = the engine's run id, a fast path only)
-- keeps a hem allowance exact in mm across graded sizes; the client resolves it, the (u, v) line is
-- the fallback. JSON NOT NULL (a NULL JSON value is the classic way to break a whole table read);
-- `edge_samples` is '[]' without a locator.
--
-- WHY A TABLE AND NOT A COLUMN ON THE CARD OR THE PIECE: the 0411 argument — rows are written one at a
-- time from the doll / picker while the form autosaves the card; a card- or piece-level JSON would
-- ride UpdateTechCard, enter the lock_version race and be clobbered by a stale tab. The fold RPCs never
-- bump lock_version and the rows enter no section digest.
--
-- NO FK TO THE PIECE: line_key is the contract's identity (0297/0411); a deleted piece leaves an
-- orphan row the screen offers to remove. FK to the card ON DELETE CASCADE, as every child of tech_card.
-- seq is unique per (card, piece, size_key) — checked in Go after each write (a reorder swaps two seqs
-- inside one transaction, which a UNIQUE index would refuse half-way).
--
-- source_fingerprint IS SERVER-WRITTEN (entity.TechCardSeamSourceFingerprint over the piece's fabric
-- scopes, the seams' device); a read recomputes it and reports `stale`.
--
-- size_id is RESERVED (NULL = every size, no per-size rows in v1); the generated size_key keeps the
-- UNIQUE honest over NULL, the device 0297/0411 use.
--
-- ADDITIVE ONLY: a new table, no CHECK (dictionaries and bounds are enforced in Go with
-- field-addressed errors), no backfill, no charset clause (0411 precedent), no double-quoted strings
-- (the server runs ANSI_QUOTES). The old binary never reads the table, so a rollback is safe.
-- CREATE TABLE IF NOT EXISTS — idempotent, one statement.

-- +migrate Up

CREATE TABLE IF NOT EXISTS tech_card_piece_fold (
    id INT AUTO_INCREMENT PRIMARY KEY,
    tech_card_id INT NOT NULL,
    fold_key CHAR(26) NOT NULL,
    piece_line_key CHAR(26) NOT NULL,
    seq SMALLINT NOT NULL DEFAULT 1,
    kind VARCHAR(8) NOT NULL,
    source VARCHAR(16) NOT NULL,
    line_uv JSON NOT NULL,
    flip_at JSON NOT NULL,
    edge_hint VARCHAR(64) NOT NULL DEFAULT '',
    edge_samples JSON NOT NULL,
    offset_mm DECIMAL(7,2) NOT NULL DEFAULT 0,
    contour_sig VARCHAR(64) NOT NULL DEFAULT '',
    size_id INT NULL,
    size_key INT AS (COALESCE(size_id, 0)) STORED,
    anchored_size VARCHAR(16) NOT NULL DEFAULT '',
    note VARCHAR(255) NOT NULL DEFAULT '',
    source_fingerprint CHAR(64) NOT NULL,
    created_by VARCHAR(255) NOT NULL DEFAULT '',
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_by VARCHAR(255) NOT NULL DEFAULT '',
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    CONSTRAINT uniq_tcpf_card_key UNIQUE (tech_card_id, fold_key, size_key),
    CONSTRAINT fk_tcpf_tech_card FOREIGN KEY (tech_card_id) REFERENCES tech_card (id) ON DELETE CASCADE,
    INDEX idx_tcpf_card_piece (tech_card_id, piece_line_key)
) ENGINE=InnoDB;

-- +migrate Down

DROP TABLE IF EXISTS tech_card_piece_fold;
