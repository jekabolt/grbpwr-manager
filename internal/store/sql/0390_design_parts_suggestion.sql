-- Auto parts (2026-10-04, Ф2) — the cache of named parts per side flat and the route of the new AI
-- purpose.
--
-- The client cuts a side's flat into numbered regions (its own deterministic cut, revision
-- algo_rev), draws the numbers on a picture and asks the model (chat.design_parts) to group them into
-- garment parts and name them. The answer is kept HERE, one row per (card, view, flat media, cut
-- revision): the same flat cut the same way is never paid for twice. A new flat on the side is a new
-- base_media_id, so an old row simply stops being read; it goes with the card (CASCADE).
--
-- `parts` is the cleaned answer {"parts":[{label, regions}], "split_needed":[{region, why}]}, shape
-- closed in Go. No ENUM, no CHECK.
--
-- ROUTE: openrouter with an explicit Claude slug (the direct `anthropic` provider is seeded disabled).
-- INSERT IGNORE never overwrites what the owner saved later. THE OLD BINARY SURVIVES THE ROW: the
-- store orders an unknown purpose after the known ones on read and refuses it only on a panel write.
--
-- IDEMPOTENT: IF NOT EXISTS + INSERT IGNORE; a half-applied file re-runs from the top.

-- +migrate Up

CREATE TABLE IF NOT EXISTS design_parts_suggestion (
    id INT NOT NULL PRIMARY KEY AUTO_INCREMENT,
    tech_card_id INT NOT NULL,
    view_key VARCHAR(32) NOT NULL COMMENT 'front|back|side_l|side_r',
    base_media_id INT NOT NULL COMMENT 'media of the side flat the regions were cut from',
    algo_rev VARCHAR(32) NOT NULL COMMENT 'the client cut revision',
    parts JSON NOT NULL,
    model VARCHAR(128) NOT NULL DEFAULT '',
    created_by VARCHAR(255) NOT NULL DEFAULT '',
    created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    CONSTRAINT uniq_design_parts_suggestion UNIQUE (tech_card_id, view_key, base_media_id, algo_rev),
    CONSTRAINT fk_design_parts_suggestion_card FOREIGN KEY (tech_card_id) REFERENCES tech_card(id) ON DELETE CASCADE
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4 COMMENT 'Auto parts: named region groups per side flat and cut revision';

INSERT IGNORE INTO ai_route (purpose, position, provider_key, model) VALUES
    ('chat.design_parts', 1, 'openrouter', 'anthropic/claude-sonnet-5.5');

-- +migrate Down

DELETE FROM ai_route
WHERE purpose = 'chat.design_parts' AND position = 1
  AND provider_key = 'openrouter' AND model = 'anthropic/claude-sonnet-5.5';

DROP TABLE IF EXISTS design_parts_suggestion;
