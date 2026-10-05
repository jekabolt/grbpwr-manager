-- Moodboard quiz — route swap after the live A/B (2026-10-05, tmp/plans/moodboard-quiz/63-AB-RESULT.md,
-- live/summary.md: 3 fixtures × 3 models × 2 runs, production prompts and parser).
--
-- RESULT: sonnet-5.5 matches opus-5.5 on quality — fit questions 3–7 vs 3–6, fit cm/% 0 for both,
-- clarify on the shirt re-run 1/1 for both, banned words 1 each — at $0.041–0.048 per run against
-- $0.108–0.128 (≈ 2.5× cheaper) and 1.5–3.0 s against 2.1–4.9 s. The defects the A/B found (padding
-- to 15, re-asking a Known waistband) are shared by every model and are fixed in the prompt, not by
-- the route. DECISION: chat.design_quiz → sonnet-5.5 primary, opus-5.5 fallback (reverses 0391).
--
-- CONDITIONAL UPDATE: each row moves ONLY while it still equals the 0391 value — a model the owner
-- chose in the panel is never overwritten.
--
-- IDEMPOTENT: on a re-run neither WHERE matches.

-- +migrate Up

UPDATE ai_route SET model = 'anthropic/claude-sonnet-5.5'
WHERE purpose = 'chat.design_quiz' AND position = 1
  AND provider_key = 'openrouter' AND model = 'anthropic/claude-opus-5.5';

UPDATE ai_route SET model = 'anthropic/claude-opus-5.5'
WHERE purpose = 'chat.design_quiz' AND position = 2
  AND provider_key = 'openrouter' AND model = 'anthropic/claude-sonnet-5.5';

-- +migrate Down

UPDATE ai_route SET model = 'anthropic/claude-opus-5.5'
WHERE purpose = 'chat.design_quiz' AND position = 1
  AND provider_key = 'openrouter' AND model = 'anthropic/claude-sonnet-5.5';

UPDATE ai_route SET model = 'anthropic/claude-sonnet-5.5'
WHERE purpose = 'chat.design_quiz' AND position = 2
  AND provider_key = 'openrouter' AND model = 'anthropic/claude-opus-5.5';
