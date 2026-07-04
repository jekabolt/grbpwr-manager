-- +migrate Up
-- Timeline body: the archive body is now an ordered, heterogeneous list of typed
-- blocks (media / text / iframe / product / products-by-tag / hand-picked),
-- stored as a JSON array on the archive row, mirroring the hero Insert-form
-- storage. Breaking change: the previous media-only body (archive_item rows) is
-- superseded and each archive must be re-saved from admin. The archive_item
-- table is left in place (no longer written or read) and can be dropped in a
-- later migration once every environment has been re-saved.
ALTER TABLE archive ADD COLUMN body JSON NULL;
