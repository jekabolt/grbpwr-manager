-- Callout suggestions (2026-10-05, T28 / R36) — the route of the new AI purpose chat.callout_suggest.
--
-- The `suggest ✦` chip of the ARTIFACTS sheet sends the card's flats (≤ 4 pictures) and the list of
-- callouts the card's own data implies to a vision model, which places them on the flats. Same family
-- as the moodboard quiz after its A/B (0393): sonnet-5.5 primary, opus-5.5 as the position-2 fallback.
-- Nothing is stored by the feature itself; the router books ai_usage_event per call.
--
-- ROUTE: openrouter with explicit Claude slugs (the direct `anthropic` provider is seeded disabled).
-- INSERT IGNORE never overwrites what the owner saved later in the panel. THE OLD BINARY SURVIVES THE
-- ROWS: the store orders an unknown purpose after the known ones on read and refuses it only on a
-- panel write.
--
-- IDEMPOTENT: INSERT IGNORE skips existing rows; the Down deletes only the seeded values.

-- +migrate Up

INSERT IGNORE INTO ai_route (purpose, position, provider_key, model) VALUES
    ('chat.callout_suggest', 1, 'openrouter', 'anthropic/claude-sonnet-5.5'),
    ('chat.callout_suggest', 2, 'openrouter', 'anthropic/claude-opus-5.5');

-- +migrate Down

DELETE FROM ai_route
WHERE purpose = 'chat.callout_suggest' AND position = 2
  AND provider_key = 'openrouter' AND model = 'anthropic/claude-opus-5.5';

DELETE FROM ai_route
WHERE purpose = 'chat.callout_suggest' AND position = 1
  AND provider_key = 'openrouter' AND model = 'anthropic/claude-sonnet-5.5';
