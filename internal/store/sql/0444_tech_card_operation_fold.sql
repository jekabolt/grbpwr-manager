-- A tech-card step stores the folds that close on it (TechCardOperation.fold_keys, proto field 74).
--
-- A piece fold (tech_card_piece_fold, 0443) said HOW a piece folds and nothing about WHEN: the
-- assembly picture had no way to tell a flat FLAP before its fold step from a folded one after it,
-- and no rule can derive that. So the step names it: an ordered list of
-- tech_card_piece_fold.fold_key per operation. A fold no step names leaves its piece flat.
--
-- SAME SHAPE AS tech_card_operation_seam (0434), the sibling op child table: FK to the operation
-- with ON DELETE CASCADE (operations are full-replaced on every card save, so the links go with
-- them and are rewritten in the same transaction), a per-step display_order, UNIQUE on the position
-- and on the key.
--
-- NO FK TO tech_card_piece_fold. A fold is unique per (tech_card_id, fold_key, size_key), not per
-- fold_key, so there is no single parent row to point at. The save checks in Go: the key names a
-- fold of this card, one fold closes on at most one step, and on one piece a larger seq never stands
-- on an earlier step than a smaller seq. The fold delete removes the key from every step in its own
-- transaction.
--
-- INERT FOR THE OLD BINARY: a new empty table nobody reads. No backfill: every existing card stays
-- without fold steps, which reads as flat. Safe under ANSI_QUOTES (single-quoted literals only).

-- +migrate Up

CREATE TABLE IF NOT EXISTS tech_card_operation_fold (
    id            INT PRIMARY KEY AUTO_INCREMENT,
    operation_id  INT NOT NULL COMMENT 'FK tech_card_operation(id)',
    fold_key      CHAR(26) NOT NULL COMMENT 'tech_card_piece_fold.fold_key of the same card (ULID, upper case)',
    display_order INT NOT NULL COMMENT 'order within the step',
    created_at    TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT fk_op_fold_operation FOREIGN KEY (operation_id) REFERENCES tech_card_operation(id) ON DELETE CASCADE,
    CONSTRAINT uniq_op_fold_order UNIQUE (operation_id, display_order),
    CONSTRAINT uniq_op_fold_key UNIQUE (operation_id, fold_key),
    INDEX idx_op_fold_key (fold_key)
) ENGINE = InnoDB COMMENT 'ordered piece folds that close on a tech-card operation';

-- +migrate Down

DROP TABLE IF EXISTS tech_card_operation_fold;
