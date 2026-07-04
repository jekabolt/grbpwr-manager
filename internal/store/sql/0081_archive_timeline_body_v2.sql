-- +migrate Up
-- Timeline body v2: every archive element is now a typed block in the JSON `body`
-- column (added in 0080) — main media, media lines, text, iframe, media+caption,
-- product, products-by-tag, hand-picked products.
--
-- DELIBERATE BREAKING CHANGE (product decision — "accept the wipe"): the block
-- model was restructured (ArchiveItemType renumbered, payloads moved into nested
-- objects) and the archive hero media moved out of its own join table into a
-- MAIN_MEDIA block, so pre-v2 content cannot be carried forward. Every archive
-- must be re-authored from admin after this deploys. Concretely:
--   1. Stale pre-v2 `body` blobs (old flat format) are cleared to NULL so an
--      un-re-authored archive is explicitly empty rather than silently skipped.
--   2. The per-archive main-media join table and the long-superseded flat
--      archive_item table (unused since 0080) are dropped.
--   3. The archive carries a title only now, so archive_translation.description
--      is removed (irreversible — description copy is discarded).
-- This is Up-only and irreversible (mirrors 0080); on prod it auto-applies via
-- MYSQL_AUTOMIGRATE and reaches prod through the feature->beta->master flow.
UPDATE archive SET body = NULL;
DROP TABLE IF EXISTS archive_main_media;
DROP TABLE IF EXISTS archive_item;
ALTER TABLE archive_translation DROP COLUMN description;
