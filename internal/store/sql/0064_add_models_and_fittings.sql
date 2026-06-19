-- +migrate Up
-- Fit/fashion models and their try-on (fitting) sessions, for the admin panel.
-- A model is a person who wears garments for photos/fit checks; it carries a sparse
-- set of body measurements (only filled ones are stored). A fitting records that a
-- specific product was tried on a given date, on one or more sizes, with comments.

-- model: a fit/fashion model profile.
CREATE TABLE model (
  id INT PRIMARY KEY AUTO_INCREMENT,
  name VARCHAR(255) NOT NULL COMMENT 'model display name',
  comment TEXT NULL COMMENT 'freeform admin note',
  gender VARCHAR(16) NULL COMMENT 'male|female|unisex (common.GenderEnum); NULL = unset'
    CHECK (gender IS NULL OR gender REGEXP '^(male|female|unisex)$'),
  default_sample_size_id INT NULL COMMENT 'FK size(id); typical size this model wears',
  created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  INDEX idx_model_created (created_at),
  INDEX idx_model_name (name),
  FOREIGN KEY (default_sample_size_id) REFERENCES size(id)
) COMMENT 'Fit/fashion model profiles';

-- model_measurement: sparse body measurements (mm); only filled values are stored.
-- measurement_name is the canonical key from the MeasurementName proto enum (e.g. chest);
-- it is intentionally decoupled from the garment measurement_name dictionary.
CREATE TABLE model_measurement (
  id INT PRIMARY KEY AUTO_INCREMENT,
  model_id INT NOT NULL,
  measurement_name VARCHAR(40) NOT NULL COMMENT 'canonical MeasurementName key, e.g. chest',
  measurement_value_mm INT NOT NULL COMMENT 'value in millimetres',
  UNIQUE KEY uniq_model_measurement (model_id, measurement_name),
  FOREIGN KEY (model_id) REFERENCES model(id) ON DELETE CASCADE
) COMMENT 'Per-model body measurements (mm); sparse';

-- fitting: a try-on session for a product.
CREATE TABLE fitting (
  id INT PRIMARY KEY AUTO_INCREMENT,
  product_id INT NOT NULL COMMENT 'FK product(id) — the garment tried on',
  model_id INT NULL COMMENT 'FK model(id) — who wore it; NULL = unspecified',
  fitting_date DATE NOT NULL COMMENT 'date the fitting took place',
  comment TEXT NULL COMMENT 'overall freeform note',
  status VARCHAR(16) NOT NULL DEFAULT 'planned' COMMENT 'planned|done|cancelled'
    CHECK (status REGEXP '^(planned|done|cancelled)$'),
  verdict VARCHAR(16) NOT NULL DEFAULT 'pending' COMMENT 'pending|approved|needs_rework|rejected'
    CHECK (verdict REGEXP '^(pending|approved|needs_rework|rejected)$'),
  recorded_by VARCHAR(255) NULL COMMENT 'admin email / free text',
  created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  INDEX idx_fitting_product (product_id),
  INDEX idx_fitting_model (model_id),
  INDEX idx_fitting_date (fitting_date),
  FOREIGN KEY (product_id) REFERENCES product(id),
  FOREIGN KEY (model_id) REFERENCES model(id) ON DELETE SET NULL
) COMMENT 'Garment try-on sessions';

-- fitting_size: sizes measured in a fitting + optional per-size fit note.
CREATE TABLE fitting_size (
  id INT PRIMARY KEY AUTO_INCREMENT,
  fitting_id INT NOT NULL,
  size_id INT NOT NULL COMMENT 'FK size(id) — m/l/xxs…',
  fit_note TEXT NULL COMMENT 'optional per-size fit note',
  UNIQUE KEY uniq_fitting_size (fitting_id, size_id),
  FOREIGN KEY (fitting_id) REFERENCES fitting(id) ON DELETE CASCADE,
  FOREIGN KEY (size_id) REFERENCES size(id)
) COMMENT 'Sizes tried per fitting + per-size fit note';

-- fitting_media: photos attached to a fitting (mirrors archive_item).
CREATE TABLE fitting_media (
  id INT PRIMARY KEY AUTO_INCREMENT,
  fitting_id INT NOT NULL,
  media_id INT NOT NULL,
  display_order INT NOT NULL DEFAULT 0,
  FOREIGN KEY (fitting_id) REFERENCES fitting(id) ON DELETE CASCADE,
  FOREIGN KEY (media_id) REFERENCES media(id)
) COMMENT 'Photos attached to a fitting';

-- +migrate Down
DROP TABLE IF EXISTS fitting_media;
DROP TABLE IF EXISTS fitting_size;
DROP TABLE IF EXISTS fitting;
DROP TABLE IF EXISTS model_measurement;
DROP TABLE IF EXISTS model;
