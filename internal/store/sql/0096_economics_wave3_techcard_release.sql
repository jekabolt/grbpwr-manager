-- +migrate Up
-- Economics audit — Wave 3 / task 11: immutable released-snapshot for tech cards.
-- A tech card is edited by full-replacing all its child tables, so it has no history: the legal
-- path re-open (→ draft) → edit → re-release irreversibly destroys the prior specification and its
-- planned costing, and costing is always recomputed from the CURRENT card state — "what was the
-- planned unit cost when the first production run started" is unrecoverable. Store a full JSON
-- snapshot of the enriched read-model (proto-JSON of the contract TechCard) at each release, plus
-- the computed unit_cost alongside so reports need not parse the blob. Follows the hero-v2 pattern:
-- a document is snapshotted whole; a broken/incompatible blob degrades on read (metadata + a field
-- error), it never crashes. Production runs (task 09) reference a release; disputes and cost audits
-- read the frozen spec.

CREATE TABLE IF NOT EXISTS tech_card_release (
  id INT PRIMARY KEY AUTO_INCREMENT,
  tech_card_id INT NOT NULL,
  version VARCHAR(32) NULL COMMENT 'tech_card.version at release time',
  released_by VARCHAR(255) NULL COMMENT 'acting admin username at release time',
  snapshot JSON NOT NULL COMMENT 'proto-JSON of the enriched contract TechCard (frozen spec)',
  unit_cost DECIMAL(10, 2) NULL COMMENT 'base-currency planned unit cost at release, if foldable',
  currency CHAR(3) NULL COMMENT 'currency of unit_cost',
  created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
  FOREIGN KEY (tech_card_id) REFERENCES tech_card(id) ON DELETE CASCADE,
  UNIQUE KEY uq_tcr_card_created (tech_card_id, created_at),
  INDEX idx_tcr_card (tech_card_id)
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4 COMMENT 'Immutable per-release snapshot of a tech card';

-- +migrate Down
DROP TABLE IF EXISTS tech_card_release;
