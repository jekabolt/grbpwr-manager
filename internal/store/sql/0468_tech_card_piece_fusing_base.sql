-- Staleness key of a piece's fusing markup: the base the edges / zone were drawn against.
--
-- Fusing markup (fusing_edges, fusing_zone) is stored in base-size coordinates. When the pattern file
-- is replaced, the client needs to know which base the markup belongs to, so it can tell "still fits"
-- from "check the markup". Two nullable columns on tech_card_piece, NULL for every existing row (no
-- backfill: an unknown key reads as "drawn before keys existed"):
--   fusing_base_size_id    the size whose coordinates the markup is in (size.id). No FK: the key is
--                          opaque to the server, and a key that names a deleted size simply never
--                          matches the current pattern.
--   fusing_base_signature  the client-computed signature of that size's piece contour, 1..64 printable
--                          ASCII characters; binary collation, it is compared byte for byte.
-- Both are set together or both are NULL; the server writes them as one pair and clears them when the
-- piece stops being fused or loses its edges / zone.
--
-- WHAT A ROLLBACK OF THE BINARY DOES. The columns stay; the old binary's piece INSERT and UPDATE name
-- their columns and never touch these two, so an UPDATE keeps a stored key and a new piece gets NULL.
-- A piece the old binary unfuses keeps its key until the next save by a binary that knows it.
--
-- Safe under ANSI_QUOTES (single-quoted literals only). One statement per PREPARE, no multiStatements.
-- Both columns are one INSTANT ALTER (atomic, no table rebuild), gated on one of them; a retry after a
-- partial failure is a no-op.

-- +migrate Up

SET @has_col = (SELECT COUNT(*) FROM information_schema.COLUMNS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_piece' AND COLUMN_NAME = 'fusing_base_size_id');
SET @ddl = IF(@has_col = 0, 'ALTER TABLE tech_card_piece ADD COLUMN fusing_base_size_id INT NULL COMMENT ''size(id) the fusing markup was drawn in; NULL = no key'', ADD COLUMN fusing_base_signature VARCHAR(64) COLLATE utf8mb4_bin NULL COMMENT ''client signature of the base contour; NULL = no key'', ALGORITHM=INSTANT', 'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- +migrate Down

SET @has_col = (SELECT COUNT(*) FROM information_schema.COLUMNS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_piece' AND COLUMN_NAME = 'fusing_base_size_id');
SET @ddl = IF(@has_col > 0, 'ALTER TABLE tech_card_piece DROP COLUMN fusing_base_size_id, DROP COLUMN fusing_base_signature', 'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;
