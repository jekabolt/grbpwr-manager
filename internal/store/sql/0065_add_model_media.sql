-- +migrate Up
-- Optional photos for fit-models: a single thumbnail plus a photo gallery.
-- Both are optional; existing models keep a NULL thumbnail and no gallery rows.

ALTER TABLE model
  ADD COLUMN thumbnail_id INT NULL COMMENT 'FK media(id); optional thumbnail',
  ADD CONSTRAINT fk_model_thumbnail FOREIGN KEY (thumbnail_id) REFERENCES media(id);

-- model_media: photo gallery for a model (mirrors fitting_media).
CREATE TABLE model_media (
  id INT PRIMARY KEY AUTO_INCREMENT,
  model_id INT NOT NULL,
  media_id INT NOT NULL,
  display_order INT NOT NULL DEFAULT 0,
  FOREIGN KEY (model_id) REFERENCES model(id) ON DELETE CASCADE,
  FOREIGN KEY (media_id) REFERENCES media(id)
) COMMENT 'Photos attached to a model (gallery)';

-- +migrate Down
DROP TABLE IF EXISTS model_media;
ALTER TABLE model
  DROP FOREIGN KEY fk_model_thumbnail,
  DROP COLUMN thumbnail_id;
