-- Pattern import page roles (2026-10-11, A9) — the route of the new AI purpose chat.pattern_pages.
--
-- The pattern importer (client) extracts the pages of a pattern PDF and must tell the 1:1 sheet
-- (tiles, a whole sheet) from instructions, an overview, a cutting layout, a cover or a table before
-- it cleans anything. ClassifyPatternPages asks ONE vision+JSON call per file (a small picture of
-- every page + the client's page statistics) what each page is; the client weighs that against its
-- own geometry. Same family as the piece names (0408): sonnet-5.5 primary, opus-5.5 as the
-- position-2 fallback. Nothing is stored by the feature itself; the router books ai_usage_event per
-- call.
--
-- ROUTE: openrouter with explicit Claude slugs (the direct `anthropic` provider is seeded disabled).
-- INSERT IGNORE never overwrites what the owner saved later in the panel. THE OLD BINARY SURVIVES THE
-- ROWS: the store orders an unknown purpose after the known ones on read and refuses it only on a
-- panel write.
--
-- IDEMPOTENT: INSERT IGNORE skips existing rows; the Down deletes only the seeded values.

-- +migrate Up

INSERT IGNORE INTO ai_route (purpose, position, provider_key, model) VALUES
    ('chat.pattern_pages', 1, 'openrouter', 'anthropic/claude-sonnet-5.5'),
    ('chat.pattern_pages', 2, 'openrouter', 'anthropic/claude-opus-5.5');

-- +migrate Down

DELETE FROM ai_route
WHERE purpose = 'chat.pattern_pages' AND position = 2
  AND provider_key = 'openrouter' AND model = 'anthropic/claude-opus-5.5';

DELETE FROM ai_route
WHERE purpose = 'chat.pattern_pages' AND position = 1
  AND provider_key = 'openrouter' AND model = 'anthropic/claude-sonnet-5.5';
