-- +migrate Up
-- Tech card (техкарта / tech pack) — the garment manufacturing spec a brand hands a
-- factory. A tech card is a standalone "style" document with a development stage
-- (proto|fit|sms|pp|prod) that links to zero or more catalog products it spawns
-- (one style, many colorways). This migration adds the core skeleton: the header
-- (identification + construction description), the size range (grade), product
-- links, sketch/preview media, sketch callouts, and the revision log. Materials
-- (BOM, colorways, POM) and production (operations, labels, packaging, costing)
-- arrive in later migrations.

-- tech_card: the tech pack header (Sheet «Титул»).
CREATE TABLE tech_card (
  id INT PRIMARY KEY AUTO_INCREMENT,
  style_number VARCHAR(255) NOT NULL COMMENT 'style / article number (Артикул)',
  name VARCHAR(255) NOT NULL COMMENT 'product name (Название изделия)',
  brand VARCHAR(255) NULL,
  season VARCHAR(255) NULL COMMENT 'free-text season (Сезон)',
  collection VARCHAR(255) NULL COMMENT 'free-text collection (Коллекция)',
  category_id INT NULL COMMENT 'FK category(id); optional category/type',
  target_gender VARCHAR(16) NULL COMMENT 'male|female|unisex (common.GenderEnum); NULL = unset'
    CHECK (target_gender IS NULL OR target_gender REGEXP '^(male|female|unisex)$'),
  stage VARCHAR(16) NOT NULL DEFAULT 'proto' COMMENT 'proto|fit|sms|pp|prod'
    CHECK (stage REGEXP '^(proto|fit|sms|pp|prod)$'),
  status VARCHAR(255) NULL COMMENT 'freeform workflow notes (soft, non-gating)',
  approval_state VARCHAR(16) NOT NULL DEFAULT 'draft'
    COMMENT 'gating release state, orthogonal to stage: draft|in_review|approved|released|obsolete'
    CHECK (approval_state REGEXP '^(draft|in_review|approved|released|obsolete)$'),
  approved_by VARCHAR(255) NULL COMMENT 'who approved the current revision',
  released_at TIMESTAMP NULL COMMENT 'when the card was released to manufacture',
  version VARCHAR(64) NULL COMMENT 'revision label (Версия / ревизия)',
  revision_date DATE NULL COMMENT 'date of the current revision (Дата ревизии)',
  base_model_id INT NULL COMMENT 'FK model(id); base fit model (Модель за основу)',
  base_sample_size_id INT NULL COMMENT 'FK size(id); base sample size (Базовый размер образца)',
  designer VARCHAR(255) NULL,
  constructor VARCHAR(255) NULL,
  technologist VARCHAR(255) NULL,
  target_cost DECIMAL(10, 2) NULL COMMENT 'target cost (Целевая себестоимость)'
    CHECK (target_cost IS NULL OR target_cost >= 0),
  target_retail_price DECIMAL(10, 2) NULL COMMENT 'target retail price (Целевая розн. цена)'
    CHECK (target_retail_price IS NULL OR target_retail_price >= 0),
  currency VARCHAR(3) NULL COMMENT 'ISO 4217 for target cost/price and costing',
  measurement_unit VARCHAR(8) NOT NULL DEFAULT 'cm'
    COMMENT 'cm|mm for callout dimensions and the POM chart (metric only)'
    CHECK (measurement_unit REGEXP '^(cm|mm)$'),
  -- construction description (lower block of Sheet «Титул»)
  description TEXT NULL COMMENT 'short product description',
  silhouette TEXT NULL COMMENT 'silhouette / length',
  collar TEXT NULL COMMENT 'collar / neckline',
  fastening TEXT NULL COMMENT 'fastening',
  pockets TEXT NULL,
  sleeve_cuff TEXT NULL COMMENT 'sleeve / cuff',
  extra_details TEXT NULL COMMENT 'additional details',
  topstitching TEXT NULL COMMENT 'topstitching (general description)',
  aux_materials TEXT NULL COMMENT 'applied / auxiliary materials',
  notes TEXT NULL COMMENT 'additional notes',
  created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  -- style/article numbers are reused across seasons (carryover / re-spec), so the
  -- article is unique per season, not globally. NOTE: season is NULLable and MySQL
  -- treats NULLs as distinct, so two cards with the same style_number and NULL season
  -- are both allowed (uniqueness only bites once a season is set).
  UNIQUE KEY uniq_tech_card_style_number_season (style_number, season),
  INDEX idx_tech_card_created (created_at),
  INDEX idx_tech_card_stage (stage),
  INDEX idx_tech_card_approval_state (approval_state),
  FOREIGN KEY (category_id) REFERENCES category(id),
  FOREIGN KEY (base_model_id) REFERENCES model(id) ON DELETE SET NULL,
  FOREIGN KEY (base_sample_size_id) REFERENCES size(id)
) COMMENT 'Garment tech pack (техкарта) header';

-- tech_card_size: the size range / grade of a tech card (defines POM columns later).
CREATE TABLE tech_card_size (
  id INT PRIMARY KEY AUTO_INCREMENT,
  tech_card_id INT NOT NULL,
  size_id INT NOT NULL COMMENT 'FK size(id)',
  display_order INT NOT NULL DEFAULT 0,
  UNIQUE KEY uniq_tech_card_size (tech_card_id, size_id),
  FOREIGN KEY (tech_card_id) REFERENCES tech_card(id) ON DELETE CASCADE,
  FOREIGN KEY (size_id) REFERENCES size(id)
) COMMENT 'Size range (grade) of a tech card';

-- tech_card_product: catalog products spawned from / linked to a tech card.
CREATE TABLE tech_card_product (
  id INT PRIMARY KEY AUTO_INCREMENT,
  tech_card_id INT NOT NULL,
  product_id INT NOT NULL COMMENT 'FK product(id)',
  display_order INT NOT NULL DEFAULT 0,
  UNIQUE KEY uniq_tech_card_product (tech_card_id, product_id),
  INDEX idx_tech_card_product_product (product_id),
  FOREIGN KEY (tech_card_id) REFERENCES tech_card(id) ON DELETE CASCADE,
  -- products are soft-deleted (product.deleted_at), so this CASCADE only fires on
  -- a hard delete and is effectively a safety net; the read path filters out
  -- soft-deleted products (see productIdsByTechCardIds).
  FOREIGN KEY (product_id) REFERENCES product(id) ON DELETE CASCADE
) COMMENT 'Catalog products linked to a tech card';

-- tech_card_media: sketch / preview media (Sheet «Тех. эскиз»).
CREATE TABLE tech_card_media (
  id INT PRIMARY KEY AUTO_INCREMENT,
  tech_card_id INT NOT NULL,
  media_id INT NOT NULL,
  kind VARCHAR(16) NOT NULL DEFAULT 'preview' COMMENT 'front|back|detail|lining|preview'
    CHECK (kind REGEXP '^(front|back|detail|lining|preview)$'),
  display_order INT NOT NULL DEFAULT 0,
  FOREIGN KEY (tech_card_id) REFERENCES tech_card(id) ON DELETE CASCADE,
  FOREIGN KEY (media_id) REFERENCES media(id)
) COMMENT 'Sketch / preview media for a tech card';

-- tech_card_callout: numbered sketch callouts / detail notes (Sheet «Тех. эскиз»).
CREATE TABLE tech_card_callout (
  id INT PRIMARY KEY AUTO_INCREMENT,
  tech_card_id INT NOT NULL,
  callout_number INT NOT NULL DEFAULT 0 COMMENT 'callout number on the sketch',
  part VARCHAR(255) NULL COMMENT 'деталь / узел',
  description TEXT NULL COMMENT 'описание, уточнение',
  dimensions VARCHAR(255) NULL COMMENT 'размеры / привязка',
  media_id INT NULL COMMENT 'FK media(id); the sketch this callout is pinned to (0/NULL = unanchored)',
  display_order INT NOT NULL DEFAULT 0,
  FOREIGN KEY (tech_card_id) REFERENCES tech_card(id) ON DELETE CASCADE,
  FOREIGN KEY (media_id) REFERENCES media(id) ON DELETE SET NULL
) COMMENT 'Sketch callouts (выноски) for a tech card';

-- tech_card_revision: changelog of the SPEC DOCUMENT itself (журнал ревизий —
-- what changed in which section, by whom). This is NOT fit history: fitting
-- verdicts/measurements live in the separate fitting feature (migration 0064).
CREATE TABLE tech_card_revision (
  id INT PRIMARY KEY AUTO_INCREMENT,
  tech_card_id INT NOT NULL,
  version VARCHAR(64) NULL,
  revision_date DATE NULL,
  author VARCHAR(255) NULL,
  section VARCHAR(255) NULL COMMENT 'раздел',
  change_note TEXT NULL COMMENT 'что изменено',
  display_order INT NOT NULL DEFAULT 0,
  FOREIGN KEY (tech_card_id) REFERENCES tech_card(id) ON DELETE CASCADE
) COMMENT 'Revision log (журнал ревизий) for a tech card';

-- +migrate Down
DROP TABLE IF EXISTS tech_card_revision;
DROP TABLE IF EXISTS tech_card_callout;
DROP TABLE IF EXISTS tech_card_media;
DROP TABLE IF EXISTS tech_card_product;
DROP TABLE IF EXISTS tech_card_size;
DROP TABLE IF EXISTS tech_card;
