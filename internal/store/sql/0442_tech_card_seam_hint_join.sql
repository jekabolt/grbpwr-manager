-- SEAM HINTS · THE SPLIT CACHE (2026-10-31, tmp/plans/unit-seams/SPEC-F.md §5a, line F round 2).
--
-- The AI now runs by itself and INCREMENTALLY: roles once per pieces digest (DXF + matched dictionary
-- + sketch hashes), joins once per (card, operation number, op digest), so an edit of one operation
-- re-asks only that operation.
--
-- tech_card_seam_hint_run (0440) keeps the ROLES runs: input_digest = the roles digest, schema_version
--   = the store version (2). The v1 whole-card rows stay and are never read again.
-- tech_card_seam_hint_join — one row per asked operation and op digest (op fields + the summaries of
--   the pieces its inputs stand for + the roles digest): outcome join (hint = the validated join row
--   JSON), not_inferred (the model could not tell; hint NULL) or none (no join in this step: pressing,
--   finishing; hint NULL). A forced rerun replaces the row of the same digest in place. Older digests
--   stay: Get shows the latest row of an op as stale until the op is asked again, and an edit undone
--   hits its old row.
--
-- VARCHAR, NOT ENUM, AND NO CHECK (0439 / 0440 precedent): outcome is enforced in Go.
-- ADDITIVE ONLY: one new table, no backfill, FK to the card ON DELETE CASCADE. The old binary never
-- reads it, so a rollback is safe. CREATE TABLE IF NOT EXISTS — idempotent.

-- +migrate Up

CREATE TABLE IF NOT EXISTS tech_card_seam_hint_join (
    id BIGINT AUTO_INCREMENT PRIMARY KEY,
    tech_card_id INT NOT NULL,
    op_number INT NOT NULL,
    op_digest CHAR(64) NOT NULL,
    schema_version INT NOT NULL,
    outcome VARCHAR(16) NOT NULL,
    hint JSON NULL,
    model VARCHAR(255) NOT NULL DEFAULT '',
    created_by VARCHAR(255) NOT NULL DEFAULT '',
    created_at TIMESTAMP(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    CONSTRAINT fk_tcshj_tech_card FOREIGN KEY (tech_card_id) REFERENCES tech_card (id) ON DELETE CASCADE,
    CONSTRAINT uniq_tcshj_card_op_digest UNIQUE (tech_card_id, op_number, op_digest, schema_version),
    INDEX idx_tcshj_card_version_op (tech_card_id, schema_version, op_number, created_at)
) ENGINE=InnoDB;

-- +migrate Down

DROP TABLE IF EXISTS tech_card_seam_hint_join;
