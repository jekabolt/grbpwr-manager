-- Moodboard labels — the two routes (tmp/plans/flat-consistency/101-MOODBOARD-ROLES.md §2.2 B):
--   chat.board_label — the view of a target picture, a first read of a new picture: the cheap
--                      google/gemini-3.1-flash-lite, fallback openai/gpt-5-mini (≈ $0.001 a picture);
--   chat.board_read  — an unclear view, the detail a picture shows: anthropic/claude-sonnet-5.5,
--                      fallback anthropic/claude-opus-5.5 (as the quiz after 0393).
-- The router's fallback is for an ERROR; «not sure» moves from the first purpose to the second, it is
-- not a fallback. The admin → AI providers panel lists both purposes by itself (aiprov/purposes.go).
--
-- INSERT IGNORE: a route the owner already set in the panel is never overwritten; IDEMPOTENT.

-- +migrate Up

INSERT IGNORE INTO ai_route (purpose, position, provider_key, model) VALUES
    ('chat.board_label', 1, 'openrouter', 'google/gemini-3.1-flash-lite'),
    ('chat.board_label', 2, 'openrouter', 'openai/gpt-5-mini'),
    ('chat.board_read', 1, 'openrouter', 'anthropic/claude-sonnet-5.5'),
    ('chat.board_read', 2, 'openrouter', 'anthropic/claude-opus-5.5');

-- +migrate Down

DELETE FROM ai_route WHERE purpose IN ('chat.board_label', 'chat.board_read');
