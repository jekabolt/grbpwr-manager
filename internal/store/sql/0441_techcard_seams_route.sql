-- Seam hints (2026-10-31, unit-seams line F) — the route of the new AI purpose chat.techcard_seams.
--
-- SuggestTechCardSeamHints asks the model ONCE per tech card input (persistent cache by digest, 0440)
-- for a role per piece and a joins map per join step — a hint the client shows and the owner fixes,
-- never a seam. Same family as the assembly skeleton (0409): sonnet-5.5 primary, opus-5.5 as the
-- position-2 fallback. The router books ai_usage_event per call.
--
-- ROUTE: openrouter with explicit Claude slugs (the direct `anthropic` provider is seeded disabled).
-- INSERT IGNORE never overwrites what the owner saved later in the panel. THE OLD BINARY SURVIVES THE
-- ROWS: the store orders an unknown purpose after the known ones on read and refuses it only on a
-- panel write.
--
-- IDEMPOTENT: INSERT IGNORE skips existing rows; the Down deletes only the seeded values.

-- +migrate Up

INSERT IGNORE INTO ai_route (purpose, position, provider_key, model) VALUES
    ('chat.techcard_seams', 1, 'openrouter', 'anthropic/claude-sonnet-5.5'),
    ('chat.techcard_seams', 2, 'openrouter', 'anthropic/claude-opus-5.5');

-- +migrate Down

DELETE FROM ai_route
WHERE purpose = 'chat.techcard_seams' AND position = 2
  AND provider_key = 'openrouter' AND model = 'anthropic/claude-opus-5.5';

DELETE FROM ai_route
WHERE purpose = 'chat.techcard_seams' AND position = 1
  AND provider_key = 'openrouter' AND model = 'anthropic/claude-sonnet-5.5';
