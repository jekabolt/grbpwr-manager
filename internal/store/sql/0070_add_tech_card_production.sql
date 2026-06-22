-- +migrate Up
-- Tech card production (Phase 3): construction/workmanship, labels & packaging, and
-- costing. Builds on 0067 (core) and 0068 (materials).
--
-- Design notes:
--  * The 1:1 sections (construction, packaging, costing) use tech_card_id as the
--    PRIMARY KEY so a full-replace on UpdateTechCard is a plain DELETE+INSERT by
--    tech_card_id, with no surrogate id to leak.
--  * Costing stores only the manually-entered cost articles + its own currency;
--    the materials rollup (Σ BOM line_total per currency) and the total are
--    COMPUTED on read, never stored, and never auto-converted across currencies.
--    hardware_cost is "hardware if NOT already in the BOM" (per the template), so
--    it does not double-count the BOM hardware section.

-- tech_card_construction: general workmanship parameters (Sheet «Обработка», top).
CREATE TABLE tech_card_construction (
  tech_card_id INT PRIMARY KEY,
  main_stitch_type VARCHAR(255) NULL COMMENT 'тип основной машинной строчки',
  stitch_density VARCHAR(64) NULL COMMENT 'плотность строчки по умолчанию (стеж/см)',
  overlock_threads VARCHAR(32) NULL COMMENT 'краеобмёточная (оверлок), кол-во ниток',
  seam_allowances VARCHAR(255) NULL COMMENT 'припуски на швы (общие)',
  hem_finish VARCHAR(255) NULL COMMENT 'обработка низа / подгибка',
  pressing VARCHAR(255) NULL COMMENT 'ВТО / финишная отделка',
  machine_class VARCHAR(255) NULL COMMENT 'класс / группа машин',
  notes TEXT NULL,
  FOREIGN KEY (tech_card_id) REFERENCES tech_card(id) ON DELETE CASCADE
) COMMENT 'General workmanship parameters for a tech card (1:1)';

-- tech_card_operation: per-node sewing operations (Sheet «Обработка», operations).
CREATE TABLE tech_card_operation (
  id INT PRIMARY KEY AUTO_INCREMENT,
  tech_card_id INT NOT NULL,
  node VARCHAR(255) NOT NULL COMMENT 'узел / операция',
  description TEXT NULL COMMENT 'описание обработки',
  seam_type VARCHAR(255) NULL COMMENT 'тип шва / строчки',
  stitches_per_cm DECIMAL(5, 2) NULL COMMENT 'стежков/см'
    CHECK (stitches_per_cm IS NULL OR stitches_per_cm >= 0),
  topstitch_width VARCHAR(64) NULL COMMENT 'ширина отстрочки',
  thread VARCHAR(255) NULL COMMENT 'нитки (арт.)',
  note TEXT NULL COMMENT 'примечание',
  display_order INT NOT NULL DEFAULT 0,
  INDEX idx_tech_card_operation_card (tech_card_id),
  FOREIGN KEY (tech_card_id) REFERENCES tech_card(id) ON DELETE CASCADE
) COMMENT 'Per-node sewing operations for a tech card';

-- tech_card_label: labels & tags (Sheet «Этикетки и упаковка», labels).
CREATE TABLE tech_card_label (
  id INT PRIMARY KEY AUTO_INCREMENT,
  tech_card_id INT NOT NULL,
  label_type VARCHAR(16) NOT NULL
    COMMENT 'main|size|care|origin|flag|hangtag|barcode|special'
    CHECK (label_type REGEXP '^(main|size|care|origin|flag|hangtag|barcode|special)$'),
  content VARCHAR(255) NULL COMMENT 'содержание / артикул',
  placement VARCHAR(255) NULL COMMENT 'размещение',
  attachment VARCHAR(255) NULL COMMENT 'крепление',
  size VARCHAR(64) NULL COMMENT 'размер',
  note TEXT NULL COMMENT 'примечание',
  display_order INT NOT NULL DEFAULT 0,
  INDEX idx_tech_card_label_card (tech_card_id),
  FOREIGN KEY (tech_card_id) REFERENCES tech_card(id) ON DELETE CASCADE
) COMMENT 'Labels and tags for a tech card';

-- tech_card_packaging: packaging spec (Sheet «Этикетки и упаковка», packaging).
CREATE TABLE tech_card_packaging (
  tech_card_id INT PRIMARY KEY,
  folding_method VARCHAR(255) NULL COMMENT 'способ складывания',
  polybag VARCHAR(255) NULL COMMENT 'полибэг (тип / размер)',
  bag_sticker VARCHAR(255) NULL COMMENT 'стикер на пакет',
  inserts VARCHAR(255) NULL COMMENT 'доп. вложения (картон, булавки, крючок)',
  units_per_box INT NULL COMMENT 'кол-во в коробе'
    CHECK (units_per_box IS NULL OR units_per_box >= 0),
  box_marking VARCHAR(255) NULL COMMENT 'маркировка короба',
  box_dimensions VARCHAR(128) NULL COMMENT 'размеры короба (Д×Ш×В)',
  weight_net DECIMAL(8, 3) NULL COMMENT 'вес нетто'
    CHECK (weight_net IS NULL OR weight_net >= 0),
  weight_gross DECIMAL(8, 3) NULL COMMENT 'вес брутто'
    CHECK (weight_gross IS NULL OR weight_gross >= 0),
  notes TEXT NULL,
  FOREIGN KEY (tech_card_id) REFERENCES tech_card(id) ON DELETE CASCADE
) COMMENT 'Packaging spec for a tech card (1:1)';

-- tech_card_costing: manually-entered cost articles (Sheet «Калькуляция»).
-- Materials rollup + total are computed on read from the BOM, not stored.
CREATE TABLE tech_card_costing (
  tech_card_id INT PRIMARY KEY,
  cmt_cost DECIMAL(12, 2) NULL COMMENT 'пошив / работа (CMT)'
    CHECK (cmt_cost IS NULL OR cmt_cost >= 0),
  hardware_cost DECIMAL(12, 2) NULL COMMENT 'фурнитура (если вне BOM)'
    CHECK (hardware_cost IS NULL OR hardware_cost >= 0),
  packaging_cost DECIMAL(12, 2) NULL COMMENT 'упаковка и маркировка'
    CHECK (packaging_cost IS NULL OR packaging_cost >= 0),
  logistics_cost DECIMAL(12, 2) NULL COMMENT 'логистика / доставка'
    CHECK (logistics_cost IS NULL OR logistics_cost >= 0),
  overhead_cost DECIMAL(12, 2) NULL COMMENT 'накладные расходы'
    CHECK (overhead_cost IS NULL OR overhead_cost >= 0),
  defect_percent DECIMAL(5, 2) NULL COMMENT 'брак / запас (%)'
    CHECK (defect_percent IS NULL OR (defect_percent >= 0 AND defect_percent <= 100)),
  markup_multiplier DECIMAL(6, 3) NULL COMMENT 'наценка (×)'
    CHECK (markup_multiplier IS NULL OR markup_multiplier >= 0),
  wholesale_price DECIMAL(12, 2) NULL COMMENT 'оптовая цена'
    CHECK (wholesale_price IS NULL OR wholesale_price >= 0),
  retail_price DECIMAL(12, 2) NULL COMMENT 'розничная цена'
    CHECK (retail_price IS NULL OR retail_price >= 0),
  currency VARCHAR(3) NULL COMMENT 'ISO 4217 for the costing articles',
  notes TEXT NULL,
  FOREIGN KEY (tech_card_id) REFERENCES tech_card(id) ON DELETE CASCADE
) COMMENT 'Costing articles for a tech card (1:1); materials/total computed on read';

-- +migrate Down
DROP TABLE IF EXISTS tech_card_costing;
DROP TABLE IF EXISTS tech_card_packaging;
DROP TABLE IF EXISTS tech_card_label;
DROP TABLE IF EXISTS tech_card_operation;
DROP TABLE IF EXISTS tech_card_construction;
