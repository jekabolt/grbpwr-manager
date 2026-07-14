-- +migrate Up
-- Economics audit task 14: a light journal of a style's DEVELOPMENT (R&D) costs, at the tech
-- card level. Production COGS (materials + CMT + …) is measured elsewhere; the money spent
-- getting a style ready — sample fabric + sewing, constructor/technologist/designer labour,
-- outsourcing — was recorded nowhere, so "full style economics" (revenue − production COGS −
-- development) could not be computed. This is a journal (append + delete, NOT full-replace like
-- the spec): one-off "spent X on Y" rows, not time-tracking.
--
-- amount is in the purchase currency; amount_base folds it to base (EUR) via costing_fx_rate (or
-- entered manually) so totals are a plain SUM with no read-time FX. fitting_id optionally ties a
-- cost to a specific try-on round (a sample made for that round) — FK SET NULL so deleting a
-- fitting keeps the expense. Development cost is a PERIOD cost and is deliberately NOT seeded
-- into product.cost_price: gross margin's COGS must stay production-only (else it double-counts
-- against a future contribution P&L). The amortized unit_cost_with_dev is computed on read only.
CREATE TABLE IF NOT EXISTS tech_card_dev_expense (
  id INT PRIMARY KEY AUTO_INCREMENT,
  tech_card_id INT NOT NULL,
  kind VARCHAR(16) NOT NULL
    CHECK (kind REGEXP '^(sample|materials|labour|outsourcing|other)$'),
  description VARCHAR(255) NULL,
  amount DECIMAL(12, 2) NOT NULL CHECK (amount >= 0),
  currency CHAR(3) NOT NULL COMMENT 'ISO 4217, uppercase',
  amount_base DECIMAL(12, 2) NULL COMMENT 'amount in base currency (folded via costing_fx_rate or manual)',
  fitting_id INT NULL COMMENT 'optional link to the try-on round this cost belongs to (e.g. a sample)',
  incurred_at DATE NULL,
  created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  FOREIGN KEY (tech_card_id) REFERENCES tech_card(id) ON DELETE CASCADE,
  FOREIGN KEY (fitting_id) REFERENCES fitting(id) ON DELETE SET NULL,
  INDEX idx_tcde_tech_card (tech_card_id)
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4 COMMENT 'Development (R&D) cost journal per tech card';

-- +migrate Down
DROP TABLE IF EXISTS tech_card_dev_expense;
