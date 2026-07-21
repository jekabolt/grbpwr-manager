-- +migrate Up

-- Links a construction operation to the BOM lines it itself consumes -- the off-part materials
-- (thread, fusing) it joins with. Until now the operation held a single bom_item_id, but one
-- operation can join several materials, so the single ref forced an operator to either split the
-- operation or drop the extras on the floor.
--
-- Same shape and reasoning as tech_card_operation_piece (0199), and deliberately separate from it:
-- piece links are the parts being joined, bom links are what joins them. Both are many-to-many.
--
-- ON DELETE CASCADE on the operation side (the link is part of the operation, and the tech-card
-- write replaces operations wholesale); ON DELETE RESTRICT on the BOM side, matching fk_op_bom
-- (0159) -- a material a spec references must not vanish underneath it.
--
-- The legacy single tech_card_operation.bom_item_id column is NOT dropped: it is still written and
-- read as the first entry so an older client keeps working through the transition.
--
-- Backfill IS possible here, unlike 0199's free-text placement: the existing bom_item_id is a real
-- FK, so every operation that already links a material gets that link carried over verbatim.
-- Guarded by NOT EXISTS so a rerun inserts nothing.
--
-- Numbering: 0189-0197 are claimed by unmerged accounting branches, 0198/0199 by this branch.

CREATE TABLE IF NOT EXISTS tech_card_operation_bom (
    id            INT PRIMARY KEY AUTO_INCREMENT,
    operation_id  INT NOT NULL COMMENT 'FK tech_card_operation(id)',
    bom_item_id   INT NOT NULL COMMENT 'FK tech_card_bom_item(id)',
    display_order INT NOT NULL DEFAULT 0 COMMENT 'order the client sent the materials in',
    created_at    TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT fk_op_bom_link_operation FOREIGN KEY (operation_id) REFERENCES tech_card_operation(id) ON DELETE CASCADE,
    CONSTRAINT fk_op_bom_link_bom       FOREIGN KEY (bom_item_id)  REFERENCES tech_card_bom_item(id)  ON DELETE RESTRICT,
    CONSTRAINT uniq_op_bom_link UNIQUE (operation_id, bom_item_id),
    INDEX idx_op_bom_link_bom (bom_item_id)
) ENGINE = InnoDB COMMENT 'WS3/WS4: BOM lines a construction operation consumes (many-to-many)';

INSERT INTO tech_card_operation_bom (operation_id, bom_item_id, display_order)
SELECT o.id, o.bom_item_id, 0
FROM tech_card_operation o
WHERE o.bom_item_id IS NOT NULL
  AND NOT EXISTS (
    SELECT 1 FROM tech_card_operation_bom l
    WHERE l.operation_id = o.id AND l.bom_item_id = o.bom_item_id
  );

-- +migrate Down
DROP TABLE IF EXISTS tech_card_operation_bom;
