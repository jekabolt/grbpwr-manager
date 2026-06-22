-- +migrate Up
-- A sewing operation's workaid (folder/binder/guide/hemmer/presser-foot). For
-- binding/hemming/elastic operations the attachment IS the operation and the SAM
-- is undefined without it, so capture it as a first-class field.
ALTER TABLE tech_card_operation
  ADD COLUMN attachment VARCHAR(64) NULL COMMENT 'folder/binder/guide/hemmer/presser-foot (приспособление)' AFTER needle;

-- +migrate Down
ALTER TABLE tech_card_operation DROP COLUMN attachment;
