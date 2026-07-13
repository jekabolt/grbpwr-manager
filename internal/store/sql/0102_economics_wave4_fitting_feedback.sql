-- +migrate Up
-- Economics audit task 13: restore (in light form) the fitting → spec feedback loop that the
-- 0079 overhaul removed with the POM tables. A fitting already records status, verdict, per-size
-- fit notes, photo callouts and PDF patterns; what was missing is the ROUND (which try-on is this
-- in the style's sequence) and a STRUCTURED outcome + change list (what to change: pattern /
-- construction / material / grading). This makes "how many iterations to approval, and why the
-- spec changed" readable, and lets development analytics count rounds-to-approval.
--
-- round_number is unique per tech card. MySQL treats NULLs as distinct in a UNIQUE index, so
-- product-only fittings (tech_card_id NULL) and unnumbered rounds (round_number NULL) are left
-- unconstrained, while (card 5, round 1) can occur at most once. outcome carries no DB CHECK
-- (validated in dto) so the DOWN migration needs no DROP-CHECK dance before dropping the column.
ALTER TABLE fitting
  ADD COLUMN round_number INT NULL
    COMMENT 'this fitting''s number in the tech card''s try-on sequence; unique per card',
  ADD COLUMN outcome VARCHAR(16) NULL
    COMMENT 'structured round outcome: approved | new_round | dropped (verdict stays as-is)';

ALTER TABLE fitting
  ADD CONSTRAINT uniq_fitting_round UNIQUE (tech_card_id, round_number);

-- The structured "what to change" work list produced by a fitting. Resolved when the change has
-- been carried into the tech card (a manual toggle). callout_number optionally ties a request to
-- a numbered photo pin (fitting_callout), so a marked fit problem maps to a concrete change.
CREATE TABLE fitting_change_request (
  id INT PRIMARY KEY AUTO_INCREMENT,
  fitting_id INT NOT NULL,
  target VARCHAR(16) NOT NULL
    CHECK (target REGEXP '^(pattern|construction|material|grading|other)$'),
  note TEXT NOT NULL,
  callout_number INT NULL COMMENT 'optional link to a fitting_callout pin on the photo',
  resolved BOOLEAN NOT NULL DEFAULT FALSE COMMENT 'carried into the tech card',
  display_order INT NOT NULL DEFAULT 0,
  created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  FOREIGN KEY (fitting_id) REFERENCES fitting(id) ON DELETE CASCADE,
  INDEX idx_fcr_fitting (fitting_id)
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4 COMMENT 'Structured change requests produced by a fitting';

-- +migrate Down
DROP TABLE IF EXISTS fitting_change_request;
ALTER TABLE fitting DROP INDEX uniq_fitting_round;
ALTER TABLE fitting
  DROP COLUMN outcome,
  DROP COLUMN round_number;
