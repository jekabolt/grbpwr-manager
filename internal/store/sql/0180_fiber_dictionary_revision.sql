-- +migrate Up
-- P0.4 (#37): register the `fiber` dictionary in the cache-revision ledger. The controlled fibre
-- vocabulary (table `fiber`, added by 0167 and seeded by 0177) gains admin CRUD (CreateFiber/
-- ArchiveFiber) in this wave; every mutation bumps its dictionary_revision row in the same
-- transaction (see internal/store/dictionary mutateWithRevision), so the namespace MUST have a row or
-- the FOR UPDATE lock read fails. The other namespaces (color/collection/tag/country/size/measurement,
-- category_size_system) were seeded by 0154 / 0175; 'fiber' was missed there because it had no CRUD
-- until now.
--
-- Idempotent / crash-safe: INSERT IGNORE on the table's PRIMARY KEY (namespace); a rerun or a mid-file
-- crash is a no-op and never resets an already-advanced revision. Single-quoted string literal is
-- ANSI_QUOTES-safe (double quotes would be an identifier).
INSERT IGNORE INTO dictionary_revision (namespace, revision) VALUES ('fiber', 1);

-- +migrate Down
DELETE FROM dictionary_revision WHERE namespace = 'fiber';
