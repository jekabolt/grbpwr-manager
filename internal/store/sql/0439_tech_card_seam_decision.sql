-- SEAM DECISIONS (2026-10-31, tmp/plans/unit-seams/labels/SPEC-G2-backend.md).
--
-- WHAT IT STORES. Every time the technologist closes a join step in the seam picker / quiz, ONE
-- immutable row: accept (saved the engine's guess unchanged), own (saved their own pick), skip
-- (skipped the step, no seam row written), refuse (rejected a proposal). operation_number 0 = the
-- card-level seam review, which has no step. tier is the confidence tier the UI showed, shown the
-- proposal itself (client JSON, at most 8 KB, NULL when the client sent none), client_version the
-- client sha / ranker version — so the guess can be measured and learned from.
--
-- WHY A SEPARATE TABLE AND NOT COLUMNS ON tech_card_seam. The seam row keeps only the OUTCOME and is
-- replaced whole on every re-decision: accept and own both land as source = manual, and a skip
-- leaves no row at all. A decision is an append-only event, several per seam over time; folding it
-- into the seam row would lose all but the last and could never hold a skip. Written by its own RPC
-- (AddTechCardSeamDecisions): no lock_version bump, no section digest, not card content, so it does
-- not travel with clone / archive / restore.
--
-- NO FK TO THE SEAM. seam_key is a client-minted ULID, the seam's identity; a seam deleted or
-- re-keyed later must not erase or block the history of how it was decided, and a skip has no seam.
-- seam_key '' = skip. FK to the card ON DELETE CASCADE, as every child table of tech_card.
--
-- ADDITIVE ONLY: a new table, no CHECK (dictionaries and bounds are enforced in Go with
-- field-addressed errors), no backfill, no charset clause (0411 precedent). The old binary never
-- reads the table, so a rollback is safe. CREATE TABLE IF NOT EXISTS — idempotent, one statement.

-- +migrate Up

CREATE TABLE IF NOT EXISTS tech_card_seam_decision (
    id BIGINT AUTO_INCREMENT PRIMARY KEY,
    tech_card_id INT NOT NULL,
    operation_number INT NOT NULL,
    action VARCHAR(8) NOT NULL,
    seam_key CHAR(26) NOT NULL DEFAULT '',
    tier VARCHAR(10) NOT NULL DEFAULT '',
    shown JSON NULL,
    client_version VARCHAR(40) NOT NULL DEFAULT '',
    created_by VARCHAR(255) NOT NULL DEFAULT '',
    created_at TIMESTAMP(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    CONSTRAINT fk_tcsd_tech_card FOREIGN KEY (tech_card_id) REFERENCES tech_card (id) ON DELETE CASCADE,
    INDEX idx_tcsd_card_op (tech_card_id, operation_number)
) ENGINE=InnoDB;

-- +migrate Down

DROP TABLE IF EXISTS tech_card_seam_decision;
