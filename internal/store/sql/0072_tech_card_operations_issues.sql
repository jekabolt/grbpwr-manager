-- +migrate Up
-- Tech card operations depth + a sewer issue-flag channel (Phase 3.5b).
--  * Per-operation: an explicit operation number (addressable «оп. 10»), the
--    machine/equipment, per-seam allowance, needle spec, and the time norm (SAM /
--    норма времени) the technologist owns. A labour rate on the construction sheet
--    lets the costing roll Σ(SAM) × rate into a computed labour cost.
--  * tech_card_issue: a first-class channel for the seamstress (or anyone) to flag
--    that an operation/detail is hard or impossible to execute, tracked to
--    resolution and pinned to the operation number / callout it concerns.

ALTER TABLE tech_card_operation
  ADD COLUMN operation_number INT NULL COMMENT 'human operation number (оп. 10, 20, …)' AFTER tech_card_id,
  ADD COLUMN machine VARCHAR(64) NULL COMMENT 'machine / equipment for this operation' AFTER seam_type,
  ADD COLUMN seam_allowance VARCHAR(64) NULL COMMENT 'seam allowance for this узел' AFTER topstitch_width,
  ADD COLUMN needle VARCHAR(64) NULL COMMENT 'needle size / type' AFTER thread,
  ADD COLUMN time_norm DECIMAL(7, 3) NULL COMMENT 'SAM / норма времени, minutes'
    CHECK (time_norm IS NULL OR time_norm >= 0);

ALTER TABLE tech_card_construction
  ADD COLUMN labour_rate DECIMAL(10, 4) NULL COMMENT 'labour cost per minute (× Σ SAM = labour cost)'
    CHECK (labour_rate IS NULL OR labour_rate >= 0),
  ADD COLUMN labour_rate_currency VARCHAR(3) NULL COMMENT 'ISO 4217 for labour_rate';

-- tech_card_issue: sewer/maker flags against an operation or callout.
CREATE TABLE tech_card_issue (
  id INT PRIMARY KEY AUTO_INCREMENT,
  tech_card_id INT NOT NULL,
  operation_number INT NULL COMMENT 'the operation this issue is about (matches tech_card_operation.operation_number)',
  callout_number INT NULL COMMENT 'the sketch callout this issue is about',
  raised_by VARCHAR(255) NULL COMMENT 'who flagged it (e.g. seamstress)',
  severity VARCHAR(8) NOT NULL DEFAULT 'medium' COMMENT 'low|medium|high'
    CHECK (severity REGEXP '^(low|medium|high)$'),
  status VARCHAR(12) NOT NULL DEFAULT 'open' COMMENT 'open|resolved|wontfix'
    CHECK (status REGEXP '^(open|resolved|wontfix)$'),
  description TEXT NOT NULL COMMENT 'what is hard / impossible',
  resolution_note TEXT NULL,
  display_order INT NOT NULL DEFAULT 0,
  INDEX idx_tech_card_issue_card (tech_card_id),
  FOREIGN KEY (tech_card_id) REFERENCES tech_card(id) ON DELETE CASCADE
) COMMENT 'Maker-flagged issues against a tech card operation / callout';

-- +migrate Down
DROP TABLE IF EXISTS tech_card_issue;
ALTER TABLE tech_card_construction
  DROP COLUMN labour_rate_currency,
  DROP COLUMN labour_rate;
ALTER TABLE tech_card_operation
  DROP COLUMN time_norm,
  DROP COLUMN needle,
  DROP COLUMN seam_allowance,
  DROP COLUMN machine,
  DROP COLUMN operation_number;
