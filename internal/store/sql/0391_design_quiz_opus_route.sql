-- Moodboard quiz — question quality (2026-10-04, Q09): the chat.design_quiz route moves to Opus 5.5
-- with Sonnet 5.5 as the position-2 fallback.
--
-- OWNER: «как будто еще не хватает вопросов про посадку … стоит ли генерить вопросы другой моделью».
-- The new prompt is a long rubric (what a picture settles, a per-group fit checklist, a closed part
-- vocabulary): the class of task where Opus stays disciplined and Sonnet drifts to templated
-- collar / pocket / label questions. The quiz is low-frequency and its answers feed every later
-- draft, so ≈ $0.08 more per run is noise. Beta is the A/B ground: the run log carries model +
-- fit_questions, and the panel switch back costs nothing.
--
-- CONDITIONAL UPDATE: the primary row moves ONLY while it still equals the 0389 seed — a model the
-- owner chose in the panel is never overwritten. The fallback row is INSERT IGNORE for the same reason.
--
-- IDEMPOTENT: the UPDATE matches nothing on a re-run; INSERT IGNORE skips an existing row.

-- +migrate Up

UPDATE ai_route SET model = 'anthropic/claude-opus-5.5'
WHERE purpose = 'chat.design_quiz' AND position = 1
  AND provider_key = 'openrouter' AND model = 'anthropic/claude-sonnet-5';

INSERT IGNORE INTO ai_route (purpose, position, provider_key, model) VALUES
    ('chat.design_quiz', 2, 'openrouter', 'anthropic/claude-sonnet-5.5');

-- +migrate Down

DELETE FROM ai_route
WHERE purpose = 'chat.design_quiz' AND position = 2
  AND provider_key = 'openrouter' AND model = 'anthropic/claude-sonnet-5.5';

UPDATE ai_route SET model = 'anthropic/claude-sonnet-5'
WHERE purpose = 'chat.design_quiz' AND position = 1
  AND provider_key = 'openrouter' AND model = 'anthropic/claude-opus-5.5';
