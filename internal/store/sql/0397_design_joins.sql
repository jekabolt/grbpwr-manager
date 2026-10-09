-- Flat route (2026-10-05, tmp/plans/flat-consistency 40-BUILD-SPEC) — the card's JOIN LIST, the route
-- of the AI purpose that writes it, and the quality flags of a generated picture.
--
-- design_joins — ONE current row per card: the garment's construction on the fixed landmark ruler
-- (layers, items, absences), written by chat.design_joins from the reference photos and corrected by
-- the designer under CAS (`rev`). `joins` is entity.DesignJoinsDoc, `consistency` the photos verdict
-- (entity.DesignJoinsConsistency); shapes closed in Go, no ENUM, no CHECK. `source_fingerprint` =
-- the photos + note the model read (a cached answer is reused only for the same source). A flat run
-- copies `joins` into its own input snapshot, so editing the list never rewrites history. The row
-- goes with the card (CASCADE).
--
-- design_picture.qa_flags — comma-separated labels the worker read off the pixels («grey»: a flat
-- candidate with a mid-grey fill inside its silhouette). A label, never a refusal. '' = nothing.
-- ADD COLUMN with a DEFAULT at the end of the table: INSTANT, no row rewritten; the old binary's
-- `SELECT p.*` reads it into nothing (the store handle is Unsafe) and its INSERTs leave ''.
--
-- ROUTE: openrouter with an explicit Claude slug (the direct `anthropic` provider is seeded disabled).
-- INSERT IGNORE never overwrites what the owner saved later. THE OLD BINARY SURVIVES THE ROW: the
-- store orders an unknown purpose after the known ones on read and refuses it only on a panel write.
--
-- IDEMPOTENT: IF NOT EXISTS + INSERT IGNORE + an information_schema guard (one statement per
-- PREPARE: prod has no multiStatements); a half-applied file re-runs from the top.

-- +migrate Up

CREATE TABLE IF NOT EXISTS design_joins (
    id INT NOT NULL PRIMARY KEY AUTO_INCREMENT,
    tech_card_id INT NOT NULL,
    rev INT NOT NULL DEFAULT 1 COMMENT 'CAS revision; every write + 1',
    joins JSON NOT NULL COMMENT 'entity.DesignJoinsDoc: layers, items, absences',
    consistency JSON NULL COMMENT 'entity.DesignJoinsConsistency: do the photos show one garment',
    model VARCHAR(128) NOT NULL DEFAULT '',
    source_fingerprint VARCHAR(64) NOT NULL DEFAULT '' COMMENT 'hash of the photos (id, role) and garment note the model read',
    edited_by VARCHAR(255) NOT NULL DEFAULT '',
    edited_at DATETIME(6) NULL COMMENT 'set when a designer saved the list; NULL = as the model wrote it',
    created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    CONSTRAINT uniq_design_joins_card UNIQUE (tech_card_id),
    CONSTRAINT fk_design_joins_card FOREIGN KEY (tech_card_id) REFERENCES tech_card(id) ON DELETE CASCADE
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4 COMMENT 'Flat route: the current join list of a tech card';

INSERT IGNORE INTO ai_route (purpose, position, provider_key, model) VALUES
    ('chat.design_joins', 1, 'openrouter', 'anthropic/claude-opus-5.5');

SET @dp_qa_flags := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'design_picture'
      AND COLUMN_NAME = 'qa_flags');
SET @ddl := IF(@dp_qa_flags = 0,
    'ALTER TABLE design_picture ADD COLUMN qa_flags VARCHAR(64) NOT NULL DEFAULT '''' COMMENT ''comma-separated pixel labels of a generated picture (grey); empty = none''',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- +migrate Down

SET @dp_qa_flags := (SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'design_picture'
      AND COLUMN_NAME = 'qa_flags');
SET @ddl := IF(@dp_qa_flags = 1,
    'ALTER TABLE design_picture DROP COLUMN qa_flags',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

DELETE FROM ai_route
WHERE purpose = 'chat.design_joins' AND position = 1
  AND provider_key = 'openrouter' AND model = 'anthropic/claude-opus-5.5';

DROP TABLE IF EXISTS design_joins;
