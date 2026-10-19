-- Assembly skeleton second opinion (2026-10-10, lane E) — the route of the new AI purpose
-- chat.assembly_skeleton.
--
-- The tech card's OPERATIONS screen reads an assembly skeleton off the pattern with deterministic code
-- (pieces → seams → units → order from a category template). «ask AI» sends that skeleton as data to
-- SuggestAssemblySkeleton for a SECOND OPINION: a suggested order of the steps, a reading per
-- ambiguous join and plausibility warnings. One JSON call per press, never automatic, never applied
-- without a second press. Same family as the pattern piece names (0408): sonnet-5.5 primary,
-- opus-5.5 as the position-2 fallback. Nothing is stored by the feature itself; the router books
-- ai_usage_event per call.
--
-- ROUTE: openrouter with explicit Claude slugs (the direct `anthropic` provider is seeded disabled).
-- INSERT IGNORE never overwrites what the owner saved later in the panel. THE OLD BINARY SURVIVES THE
-- ROWS: the store orders an unknown purpose after the known ones on read and refuses it only on a
-- panel write.
--
-- IDEMPOTENT: INSERT IGNORE skips existing rows; the Down deletes only the seeded values.

-- +migrate Up

INSERT IGNORE INTO ai_route (purpose, position, provider_key, model) VALUES
    ('chat.assembly_skeleton', 1, 'openrouter', 'anthropic/claude-sonnet-5.5'),
    ('chat.assembly_skeleton', 2, 'openrouter', 'anthropic/claude-opus-5.5');

-- +migrate Down

DELETE FROM ai_route
WHERE purpose = 'chat.assembly_skeleton' AND position = 2
  AND provider_key = 'openrouter' AND model = 'anthropic/claude-opus-5.5';

DELETE FROM ai_route
WHERE purpose = 'chat.assembly_skeleton' AND position = 1
  AND provider_key = 'openrouter' AND model = 'anthropic/claude-sonnet-5.5';
