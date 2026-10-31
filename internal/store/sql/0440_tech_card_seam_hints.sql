-- SEAM HINTS · AI ONCE PER TECH CARD (2026-10-31, tmp/plans/unit-seams/SPEC-F.md §2, line F).
--
-- WHAT IT STORES. SuggestTechCardSeamHints asks the model (chat.techcard_seams) ONCE per card input for
-- a HINT: a role per piece (closed vocab) and a joins map per join step (which pieces go onto which,
-- an edge word on each side, a relation). A hint never writes a seam: the client shows it and the
-- owner fixes it.
--
-- tech_card_seam_hint_run — one row per (card, input digest, schema version): the validated hints
--   JSON the model gave for exactly that input. The digest (sha256 hex of the canonical input: pieces,
--   ops, sketch media + their content hash, matched dictionary rows, schema version) is the persistent
--   cache key: the same digest never calls the provider again unless the owner forces a rerun, which
--   replaces the row in place.
-- tech_card_seam_hint_override — the owner's word on one hint: kind role (hint_key = piece name) or
--   join (hint_key = operation number as text), value JSON, state ok | fixed | off. Overrides SURVIVE
--   RERUNS: no run ever touches this table.
-- piece_code_dictionary — the owner's code dictionary «все карты»: a normalized upper token of a piece
--   name (FLP, CLR_SECOND …) → role (+ hand). Per-card fixes stay in the override table (kind role).
--   No seed here: the client seeds it once from its static token list.
--
-- VARCHAR, NOT ENUM, AND NO CHECK (0439 precedent): the vocabularies (kind, state, role, hand) are
-- enforced in Go with field-addressed errors, so widening one never needs a migration.
--
-- ADDITIVE ONLY: new tables, no backfill, no charset clause (0411 precedent). FKs to the card ON
-- DELETE CASCADE like every child of tech_card; the dictionary belongs to no card. The old binary
-- never reads these tables, so a rollback is safe. CREATE TABLE IF NOT EXISTS — idempotent.

-- +migrate Up

CREATE TABLE IF NOT EXISTS tech_card_seam_hint_run (
    id BIGINT AUTO_INCREMENT PRIMARY KEY,
    tech_card_id INT NOT NULL,
    input_digest CHAR(64) NOT NULL,
    schema_version INT NOT NULL,
    model VARCHAR(255) NOT NULL DEFAULT '',
    hints JSON NOT NULL,
    created_by VARCHAR(255) NOT NULL DEFAULT '',
    created_at TIMESTAMP(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    CONSTRAINT fk_tcshr_tech_card FOREIGN KEY (tech_card_id) REFERENCES tech_card (id) ON DELETE CASCADE,
    CONSTRAINT uniq_tcshr_card_digest UNIQUE (tech_card_id, input_digest, schema_version),
    INDEX idx_tcshr_card_created (tech_card_id, created_at)
) ENGINE=InnoDB;

CREATE TABLE IF NOT EXISTS tech_card_seam_hint_override (
    tech_card_id INT NOT NULL,
    kind VARCHAR(8) NOT NULL,
    hint_key VARCHAR(64) NOT NULL,
    value JSON NULL,
    state VARCHAR(8) NOT NULL,
    updated_at TIMESTAMP(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
    updated_by VARCHAR(255) NOT NULL DEFAULT '',
    PRIMARY KEY (tech_card_id, kind, hint_key),
    CONSTRAINT fk_tcsho_tech_card FOREIGN KEY (tech_card_id) REFERENCES tech_card (id) ON DELETE CASCADE
) ENGINE=InnoDB;

CREATE TABLE IF NOT EXISTS piece_code_dictionary (
    code VARCHAR(32) NOT NULL PRIMARY KEY,
    role VARCHAR(16) NOT NULL,
    hand VARCHAR(1) NULL,
    updated_at TIMESTAMP(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
    updated_by VARCHAR(255) NOT NULL DEFAULT ''
) ENGINE=InnoDB;

-- +migrate Down

DROP TABLE IF EXISTS piece_code_dictionary;
DROP TABLE IF EXISTS tech_card_seam_hint_override;
DROP TABLE IF EXISTS tech_card_seam_hint_run;
