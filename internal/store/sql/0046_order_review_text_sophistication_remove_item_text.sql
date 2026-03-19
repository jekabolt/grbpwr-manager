-- +migrate Up
-- Add review_text and sophistication_rating to order-level review.
-- Remove text from item-level review (moved to order-level).

ALTER TABLE order_review
  ADD COLUMN review_text TEXT NULL DEFAULT NULL AFTER packaging_rating,
  ADD COLUMN sophistication_rating ENUM('poor', 'fair', 'good', 'very_good', 'excellent') NULL DEFAULT NULL AFTER review_text;

ALTER TABLE order_item_review
  DROP COLUMN text;

-- +migrate Down
ALTER TABLE order_review
  DROP COLUMN review_text,
  DROP COLUMN sophistication_rating;

ALTER TABLE order_item_review
  ADD COLUMN text TEXT NULL DEFAULT NULL AFTER recommend;
