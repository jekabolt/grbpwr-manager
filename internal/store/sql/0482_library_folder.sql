-- +migrate Up

-- Library folders: one shared tree for media rows and library files (media-files wave, D4).
--
-- A row lives in at most one folder: media.lib_folder_id (0483) and library_file.folder_id (0484),
-- NULL = no folder. Topics and projects stay tags on files; a folder is a container.
--
-- parent_key makes sibling names unique at the root too: a UNIQUE index treats NULLs as distinct, so
-- (parent_id, name) alone would let two root folders share a name. A FK on the base column of a STORED
-- generated column is allowed only with RESTRICT / NO ACTION, which is what a tree wants anyway.
-- utf8mb4_unicode_ci makes names case- and accent-insensitive: «Shoots» and «shoots» collide. The store
-- trims names before writing. The unique key is 4 + 480 bytes, under the 3072 InnoDB limit.
-- created_by_id has no FK on purpose, so deleting an admin is never blocked by a folder.
--
-- The self FK is safe: the table is created here, empty. The folder columns on media and
-- library_file get NO FK (see 0483) and the store enforces integrity instead.
--
-- IDEMPOTENCY: CREATE TABLE IF NOT EXISTS re-runs safely by itself.

CREATE TABLE IF NOT EXISTS library_folder (
  id INT NOT NULL AUTO_INCREMENT,
  parent_id INT NULL,
  parent_key INT AS (IFNULL(parent_id, 0)) STORED,
  name VARCHAR(120) CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci NOT NULL,
  created_by_id INT NULL,
  created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (id),
  UNIQUE KEY uq_library_folder_sibling (parent_key, name),
  KEY idx_library_folder_parent (parent_id),
  CONSTRAINT fk_library_folder_parent FOREIGN KEY (parent_id) REFERENCES library_folder (id) ON DELETE RESTRICT
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- +migrate Down

DROP TABLE IF EXISTS library_folder;
