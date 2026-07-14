-- +migrate Up

-- new-flow NF-04: a sample (сэмпл/образец) is a sewn prototype of a style — a real object with its
-- own size, purpose and status — distinct from a fitting (a try-on session; one sample can be tried
-- on several times). A sample's cost is composed on read from material issues (NF-01) tied to it and
-- the manual dev-expense journal; nothing is stored here beyond identity + lifecycle.
CREATE TABLE IF NOT EXISTS sample (
    id            INT AUTO_INCREMENT PRIMARY KEY,
    tech_card_id  INT NOT NULL,
    number        INT NOT NULL,                       -- sequential within the card (auto MAX+1)
    purpose       VARCHAR(8) NOT NULL DEFAULT 'proto', -- proto|fit|sms|pp (mirrors the card stages)
    size_id       INT NULL,                            -- the size it was sewn in (first size, e.g. S)
    colorway_id   INT NULL,                            -- for pp / colour-model samples
    status        VARCHAR(16) NOT NULL DEFAULT 'planned', -- planned|in_sewing|done|scrapped
    fabric_source VARCHAR(16) NOT NULL DEFAULT 'sample',  -- sample|production fabric
    notes         TEXT NULL,
    started_at    DATE NULL,
    finished_at   DATE NULL,
    created_at    TIMESTAMP DEFAULT CURRENT_TIMESTAMP NOT NULL,
    updated_at    TIMESTAMP DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP NOT NULL,
    CONSTRAINT fk_sample_tech_card FOREIGN KEY (tech_card_id) REFERENCES tech_card(id) ON DELETE CASCADE,
    CONSTRAINT fk_sample_size FOREIGN KEY (size_id) REFERENCES size(id),
    CONSTRAINT fk_sample_colorway FOREIGN KEY (colorway_id) REFERENCES tech_card_colorway(id) ON DELETE SET NULL,
    CONSTRAINT chk_sample_purpose CHECK (purpose REGEXP '^(proto|fit|sms|pp)$'),
    CONSTRAINT chk_sample_status CHECK (status REGEXP '^(planned|in_sewing|done|scrapped)$'),
    CONSTRAINT chk_sample_fabric CHECK (fabric_source REGEXP '^(sample|production)$'),
    UNIQUE KEY uniq_sample_number (tech_card_id, number),
    INDEX idx_sample_tech_card (tech_card_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- Link existing entities to a sample. All SET NULL — deleting a sample keeps the history. The
-- column adds + FK adds are guarded via information_schema so the file is idempotent.

-- fitting.sample_id
SET @need := (SELECT COUNT(*) = 0 FROM information_schema.COLUMNS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'fitting' AND COLUMN_NAME = 'sample_id');
SET @sql := IF(@need, 'ALTER TABLE fitting ADD COLUMN sample_id INT NULL', 'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;
SET @need := (SELECT COUNT(*) = 0 FROM information_schema.TABLE_CONSTRAINTS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'fitting' AND CONSTRAINT_NAME = 'fk_fitting_sample');
SET @sql := IF(@need, 'ALTER TABLE fitting ADD CONSTRAINT fk_fitting_sample FOREIGN KEY (sample_id) REFERENCES sample(id) ON DELETE SET NULL', 'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

-- tech_card_dev_expense.sample_id
SET @need := (SELECT COUNT(*) = 0 FROM information_schema.COLUMNS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_dev_expense' AND COLUMN_NAME = 'sample_id');
SET @sql := IF(@need, 'ALTER TABLE tech_card_dev_expense ADD COLUMN sample_id INT NULL', 'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;
SET @need := (SELECT COUNT(*) = 0 FROM information_schema.TABLE_CONSTRAINTS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_dev_expense' AND CONSTRAINT_NAME = 'fk_dev_expense_sample');
SET @sql := IF(@need, 'ALTER TABLE tech_card_dev_expense ADD CONSTRAINT fk_dev_expense_sample FOREIGN KEY (sample_id) REFERENCES sample(id) ON DELETE SET NULL', 'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

-- task.sample_id (deep-link, continuing the tech_card/product/order/archive/fitting/production_run row)
SET @need := (SELECT COUNT(*) = 0 FROM information_schema.COLUMNS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'task' AND COLUMN_NAME = 'sample_id');
SET @sql := IF(@need, 'ALTER TABLE task ADD COLUMN sample_id INT NULL', 'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;
SET @need := (SELECT COUNT(*) = 0 FROM information_schema.TABLE_CONSTRAINTS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'task' AND CONSTRAINT_NAME = 'fk_task_sample');
SET @sql := IF(@need, 'ALTER TABLE task ADD CONSTRAINT fk_task_sample FOREIGN KEY (sample_id) REFERENCES sample(id) ON DELETE SET NULL', 'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

-- material_stock_movement.sample_id FK (the column was created in 0105 without an FK; sample exists now)
SET @need := (SELECT COUNT(*) = 0 FROM information_schema.TABLE_CONSTRAINTS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'material_stock_movement' AND CONSTRAINT_NAME = 'fk_msm_sample');
SET @sql := IF(@need, 'ALTER TABLE material_stock_movement ADD CONSTRAINT fk_msm_sample FOREIGN KEY (sample_id) REFERENCES sample(id) ON DELETE SET NULL', 'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

-- +migrate Down

DROP TABLE IF EXISTS sample;
