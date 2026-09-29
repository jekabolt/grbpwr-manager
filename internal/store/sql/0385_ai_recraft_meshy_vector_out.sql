-- AI providers wave (29.09), commit H1 — Recraft, vector generation and the direct Meshy provider leave.
--
-- OWNER, 29.09: «рекрафт не должен быть как отдельная модель — мы его берём только из OpenRouter …
-- функциональность создания вектора можно полностью выкинуть, как и интеграцию с рекрафтом … meshy мы его
-- берём с fal ai». So the binary no longer knows the providers `recraft` and `meshy` (entity.AIProviderKeys)
-- nor the purpose `vector` (entity.AIPurposes); 3D is fal only (fal hosts Meshy's models, default slug
-- meshy/v7/multi-image-to-3d). This file removes what 0373 seeded for them:
--   - the `vector` route (a whole purpose — the same statement shape 0378 used, which the aiprov catalogue
--     test reads as «retired»);
--   - every route row that names meshy or recraft (the 3D route keeps its fal rows; a route left with a
--     gap in its positions reads fine — the store orders candidates by position — and the next panel save
--     renumbers it; a threed route left with no row at all resolves to fal, the only 3D provider);
--   - the two provider rows, their stored keys with them (ai_model rows cascade, fk_ai_model_provider).
--
-- HISTORY STAYS. ai_usage_event, ai_provider_cost_daily, ai_provider_usage_snapshot and design_run keep
-- their meshy / recraft / vector rows: no foreign key ties them to ai_provider (the house rule for the AI
-- tables), the spend report lists a key it does not know after the known ones, and a design_run of kind
-- 'vector' is read as stored.
--
-- IDEMPOTENT: DELETE of a row already gone is a no-op, so a re-run after a mid-file failure is safe.
-- Down re-seeds the two provider rows and the vector route exactly as 0373 did (keys are not restored —
-- they were sealed secrets and are gone), never overwriting a row that exists.

-- +migrate Up
DELETE FROM ai_route WHERE purpose = 'vector';

DELETE FROM ai_route WHERE provider_key IN ('meshy', 'recraft');

DELETE FROM ai_provider WHERE provider_key IN ('meshy', 'recraft');

-- +migrate Down
INSERT INTO ai_provider (provider_key, label, enabled) VALUES
    ('meshy', 'Meshy', 1),
    ('recraft', 'Recraft', 1)
ON DUPLICATE KEY UPDATE provider_key = provider_key;

INSERT IGNORE INTO ai_route (purpose, position, provider_key, model) VALUES
    ('vector', 1, 'recraft', '');
