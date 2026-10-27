-- A tech-card step stores the seams it sews (TechCardOperation.seam_keys, proto field 71).
--
-- Until now a step did not know its seams and the admin client guessed them (bindSeams). The step
-- card gets an explicit seam picker, so the choice is stored here: an ordered list of
-- tech_card_seam.seam_key per operation, order = the seam number on the step picture.
--
-- SAME SHAPE AS tech_card_operation_input (0307), the sibling op child table: FK to the operation
-- with ON DELETE CASCADE (operations are full-replaced on every card save, so the links go with
-- them and are rewritten in the same transaction), a per-step display_order, UNIQUE on the position
-- and on the key.
--
-- NO FK TO tech_card_seam. A seam is unique per (tech_card_id, seam_key, size_key), not per
-- seam_key, so there is no single parent row to point at. Existence is checked in Go inside the
-- card save (unknown key = InvalidArgument), and the seam delete removes the key from every step in
-- its own transaction.
--
-- Hand placement of a part piece on its host (TechCardSeamAnchor.placement) needs no DDL: anchors
-- live inside tech_card_seam.side_a / side_b JSON, and the new key is omitted when absent.
--
-- INERT FOR THE OLD BINARY: a new empty table nobody reads. Safe under ANSI_QUOTES (single-quoted
-- literals only).

-- +migrate Up

CREATE TABLE IF NOT EXISTS tech_card_operation_seam (
    id            INT PRIMARY KEY AUTO_INCREMENT,
    operation_id  INT NOT NULL COMMENT 'FK tech_card_operation(id)',
    seam_key      CHAR(26) NOT NULL COMMENT 'tech_card_seam.seam_key of the same card (ULID, upper case)',
    display_order INT NOT NULL COMMENT 'sewing order within the step',
    created_at    TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT fk_op_seam_operation FOREIGN KEY (operation_id) REFERENCES tech_card_operation(id) ON DELETE CASCADE,
    CONSTRAINT uniq_op_seam_order UNIQUE (operation_id, display_order),
    CONSTRAINT uniq_op_seam_key UNIQUE (operation_id, seam_key),
    INDEX idx_op_seam_key (seam_key)
) ENGINE = InnoDB COMMENT 'ordered seams a tech-card operation sews';

-- +migrate Down

DROP TABLE IF EXISTS tech_card_operation_seam;
