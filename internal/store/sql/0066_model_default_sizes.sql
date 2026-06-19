-- +migrate Up
-- Models can now have multiple default sample sizes (e.g. a top size and a
-- bottom size), so the single model.default_sample_size_id column is replaced
-- by a model_default_size join table.

CREATE TABLE model_default_size (
  id INT PRIMARY KEY AUTO_INCREMENT,
  model_id INT NOT NULL,
  size_id INT NOT NULL,
  UNIQUE KEY uniq_model_default_size (model_id, size_id),
  FOREIGN KEY (model_id) REFERENCES model(id) ON DELETE CASCADE,
  FOREIGN KEY (size_id) REFERENCES size(id)
) COMMENT 'Default sample sizes a model wears (multi)';

-- Carry over any existing single default size into the new table.
INSERT INTO model_default_size (model_id, size_id)
SELECT id, default_sample_size_id FROM model WHERE default_sample_size_id IS NOT NULL;

-- Drop the old single-size foreign key (auto-named by MySQL in 0064) and column.
-- The constraint name is looked up so this works regardless of its generated name.
SET @fk := (
  SELECT CONSTRAINT_NAME FROM information_schema.KEY_COLUMN_USAGE
  WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'model'
    AND COLUMN_NAME = 'default_sample_size_id' AND REFERENCED_TABLE_NAME = 'size'
  LIMIT 1
);
SET @sql := IF(@fk IS NOT NULL, CONCAT('ALTER TABLE model DROP FOREIGN KEY ', @fk), 'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

ALTER TABLE model DROP COLUMN default_sample_size_id;

-- +migrate Down
ALTER TABLE model
  ADD COLUMN default_sample_size_id INT NULL COMMENT 'FK size(id); typical size this model wears',
  ADD CONSTRAINT fk_model_default_size FOREIGN KEY (default_sample_size_id) REFERENCES size(id);

-- Best-effort restore: keep the smallest size id per model.
UPDATE model m
  JOIN (SELECT model_id, MIN(size_id) AS size_id FROM model_default_size GROUP BY model_id) d
    ON d.model_id = m.id
  SET m.default_sample_size_id = d.size_id;

DROP TABLE IF EXISTS model_default_size;
