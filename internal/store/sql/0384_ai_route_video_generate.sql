-- AI providers wave (28.09), B-32 — the VIDEO purpose gets its seeded route row.
--
-- OWNER, 28.09: «в runblob есть и видео и фото — делай и то и то». The playground's «Image to Video»
-- tile (design_run.kind = 'video', a Go-closed VARCHAR vocabulary — 0340 says «CHECK намеренно нет»,
-- so the new kind is one Go line and no ALTER) spends under the purpose `video.generate`, capability
-- `video`, and runblob (Kling image-to-video) is the only provider that serves it today. Without a
-- route row the registry answers «no candidate» and the door refuses every video run as
-- kind_not_available; with it the panel shows the row under «3d & video» and the owner can edit it.
--
-- The model column is '' = the transport's own default (kling_2.5_turbo); the owner may name another
-- `kling_*` slug on the row.
--
-- IDEMPOTENT and never overwriting: INSERT IGNORE leaves an existing (purpose, 1) row alone, so a
-- re-run keeps whatever the owner saved. THE OLD BINARY SURVIVES THIS ROW: the store orders an
-- unknown purpose after the known ones on read (store/ai groupRoutes) and refuses it only on a panel
-- WRITE (normaliseRoute), so a rollback to a binary without `video.generate` boots and serves; the row
-- simply is not listed until the binary that knows it returns. Down removes exactly the seeded row.

-- +migrate Up
INSERT IGNORE INTO ai_route (purpose, position, provider_key, model) VALUES
    ('video.generate', 1, 'runblob', '');

-- +migrate Down
DELETE FROM ai_route
WHERE purpose = 'video.generate' AND position = 1
  AND provider_key = 'runblob' AND model = '';
