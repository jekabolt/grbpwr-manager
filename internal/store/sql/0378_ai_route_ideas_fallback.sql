-- AI providers wave (27.09), B-18 — the Ideas button's FALLBACK becomes a route row.
--
-- Before the router, SuggestPrompts retried once, in the handler, on a 404 "model not served" from the
-- ideas slug, with openrouter.IdeasFallbackModel (openai/gpt-5-mini). The router has no such retry: it
-- walks the purpose's route in position order and moves on where the failure allows it (router.Chat).
-- So the same fallback is the route's position-2 row, visible and editable in admin → AI providers.
--
-- Position 1 stays the 0373 seed (openrouter, model '' = OPENROUTER_MODEL_IDEAS / its default slug).
-- OPENROUTER_MODEL_IDEAS=off still closes the whole purpose, this row included (router.Defaults.IdeasOff).
--
-- AND ONE PURPOSE LEAVES THE ROUTE TABLE. chat.techcard_operations_draft was retired with its button
-- (O-66) and left the Go catalogue (entity.AIPurposes); the row 0373 seeded for it is deleted here so
-- the panel lists no route for a button that does not exist. Down leaves it deleted: re-seeding a
-- route for a removed feature would restore a label for nothing.
--
-- IDEMPOTENT and never overwriting: INSERT IGNORE leaves an existing (purpose, 2) row alone, so a re-run
-- keeps what is there, and the DELETE of a row already gone is a no-op. It does not look at position 1: a route the owner saved with ONE row before this
-- file ran gains the fallback, which the panel shows and the owner can remove.

-- +migrate Up
INSERT IGNORE INTO ai_route (purpose, position, provider_key, model) VALUES
    ('chat.playground_ideas', 2, 'openrouter', 'openai/gpt-5-mini');

DELETE FROM ai_route WHERE purpose = 'chat.techcard_operations_draft';

-- +migrate Down
DELETE FROM ai_route
WHERE purpose = 'chat.playground_ideas' AND position = 2
  AND provider_key = 'openrouter' AND model = 'openai/gpt-5-mini';
