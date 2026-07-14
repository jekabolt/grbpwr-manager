-- +migrate Up

-- gap-07 v2 (A): an employee registry so salaries stop being anonymous free-text opex_recurring
-- rows. `employee` is the roster (name, role, employment window, an informational default monthly
-- cost); a salary is still a recurring OPEX template (category=salaries) but now carries an optional
-- employee_id linking it to the person, so the two-sided view (who ⇄ which recurring lines) exists.
-- The link is ON DELETE SET NULL: removing an employee never deletes the booked OPEX history.
--
-- Idempotent: CREATE TABLE IF NOT EXISTS + guarded ADD COLUMN / ADD CONSTRAINT via information_schema
-- (MySQL 8 has no ADD COLUMN/CONSTRAINT IF NOT EXISTS), so a mid-file failure re-runs from the top.

CREATE TABLE IF NOT EXISTS employee (
    id INT AUTO_INCREMENT PRIMARY KEY,
    full_name VARCHAR(191) NOT NULL,
    role VARCHAR(64) NULL,                       -- free-text title (швея / конструктор / дизайнер / …)
    employment_start DATE NULL,
    employment_end DATE NULL,                    -- NULL = still employed
    default_currency CHAR(3) NULL,               -- informational default salary currency
    default_monthly_cost DECIMAL(12,2) NULL,     -- informational default gross monthly cost
    note VARCHAR(255) NULL,
    archived BOOLEAN NOT NULL DEFAULT FALSE,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    CONSTRAINT chk_employee_cost CHECK (default_monthly_cost IS NULL OR default_monthly_cost >= 0),
    INDEX idx_employee_active (archived, full_name)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- opex_recurring.employee_id (guarded add), then the FK (guarded add).
SET @need_col := (SELECT COUNT(*) = 0 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'opex_recurring' AND COLUMN_NAME = 'employee_id');
SET @sql := IF(@need_col,
    'ALTER TABLE opex_recurring ADD COLUMN employee_id INT NULL',
    'SELECT 1');
PREPARE s FROM @sql; EXECUTE s; DEALLOCATE PREPARE s;

SET @need_fk := (SELECT COUNT(*) = 0 FROM information_schema.TABLE_CONSTRAINTS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'opex_recurring'
      AND CONSTRAINT_NAME = 'fk_opex_rec_employee');
SET @sql := IF(@need_fk,
    'ALTER TABLE opex_recurring
        ADD CONSTRAINT fk_opex_rec_employee FOREIGN KEY (employee_id) REFERENCES employee(id) ON DELETE SET NULL',
    'SELECT 1');
PREPARE s FROM @sql; EXECUTE s; DEALLOCATE PREPARE s;

-- +migrate Down

-- Drop the FK + column first (guarded), then the table.
SET @has_fk := (SELECT COUNT(*) FROM information_schema.TABLE_CONSTRAINTS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'opex_recurring'
      AND CONSTRAINT_NAME = 'fk_opex_rec_employee');
SET @sql := IF(@has_fk,
    'ALTER TABLE opex_recurring DROP FOREIGN KEY fk_opex_rec_employee',
    'SELECT 1');
PREPARE s FROM @sql; EXECUTE s; DEALLOCATE PREPARE s;

DROP TABLE IF EXISTS employee;
SELECT 1;
