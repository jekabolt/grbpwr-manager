-- Pattern import piece names (2026-10-09, F9) — the route of the new AI purpose chat.pattern_pieces.
--
-- The pattern importer (client) finds the pieces of a sewing pattern itself, renders the assembled
-- sheet with a number on every piece (Set-of-Mark) and asks SuggestPatternPieces to NAME them: code,
-- English name, fabrics, cut quantity, fold, pair. One vision+JSON call per import. Same family as the
-- flat parts labeller (0390) and the callout suggestions (0396): sonnet-5.5 primary, opus-5.5 as the
-- position-2 fallback. Nothing is stored by the feature itself; the router books ai_usage_event per call.
--
-- ROUTE: openrouter with explicit Claude slugs (the direct `anthropic` provider is seeded disabled).
-- INSERT IGNORE never overwrites what the owner saved later in the panel. THE OLD BINARY SURVIVES THE
-- ROWS: the store orders an unknown purpose after the known ones on read and refuses it only on a
-- panel write.
--
-- IDEMPOTENT: INSERT IGNORE skips existing rows; the Down deletes only the seeded values.

-- +migrate Up

INSERT IGNORE INTO ai_route (purpose, position, provider_key, model) VALUES
    ('chat.pattern_pieces', 1, 'openrouter', 'anthropic/claude-sonnet-5.5'),
    ('chat.pattern_pieces', 2, 'openrouter', 'anthropic/claude-opus-5.5');

-- +migrate Down

DELETE FROM ai_route
WHERE purpose = 'chat.pattern_pieces' AND position = 2
  AND provider_key = 'openrouter' AND model = 'anthropic/claude-opus-5.5';

DELETE FROM ai_route
WHERE purpose = 'chat.pattern_pieces' AND position = 1
  AND provider_key = 'openrouter' AND model = 'anthropic/claude-sonnet-5.5';
