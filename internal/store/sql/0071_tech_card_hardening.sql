-- +migrate Up
-- Tech card hardening (Phase 3.5a): optimistic locking so concurrent role edits
-- don't silently clobber each other; a stable unique POM code so a measurement
-- point is addressable across revisions; POM actuals tied to the size they were
-- measured at (so QC can compare to that size's grade ± tolerance); plus an
-- approved_at timestamp and a design-concept field.

ALTER TABLE tech_card
  ADD COLUMN lock_version INT NOT NULL DEFAULT 0
    COMMENT 'optimistic-lock counter; bumped on every update, echoed to the client' AFTER id,
  ADD COLUMN approved_at TIMESTAMP NULL
    COMMENT 'when the current revision was approved (auto-set on approve/release)' AFTER approved_by,
  ADD COLUMN concept TEXT NULL COMMENT 'design concept / intent (designer)' AFTER description;

-- A non-empty POM code is the stable cross-revision handle for a point. NULLs are
-- distinct in MySQL, so the unique key only bites once a code is set.
ALTER TABLE tech_card_pom_point
  ADD UNIQUE KEY uniq_tech_card_pom_code (tech_card_id, code);

-- An actual measured against a specific size can be compared to that size's grade
-- (falling back to base_value when no size is given).
ALTER TABLE tech_card_pom_actual
  ADD COLUMN size_id INT NULL COMMENT 'FK size(id); the size this piece was measured at' AFTER pom_point_id,
  ADD INDEX idx_tech_card_pom_actual_size (size_id),
  ADD CONSTRAINT fk_tech_card_pom_actual_size FOREIGN KEY (size_id) REFERENCES size(id);

-- +migrate Down
ALTER TABLE tech_card_pom_actual
  DROP FOREIGN KEY fk_tech_card_pom_actual_size,
  DROP INDEX idx_tech_card_pom_actual_size,
  DROP COLUMN size_id;
ALTER TABLE tech_card_pom_point DROP INDEX uniq_tech_card_pom_code;
ALTER TABLE tech_card
  DROP COLUMN concept,
  DROP COLUMN approved_at,
  DROP COLUMN lock_version;
