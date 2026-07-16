-- +migrate Up

-- SKU redesign task 03 (contract decision R10): the 5-digit MODEL segment of the SKU (SS26-00021-BLK)
-- is owned by the STYLE (tech_card) alone. There is NO standalone product.model_no — in the North Star
-- model every product IS a colourway of a style (PR6 P1) and all of a style's colourways share the one
-- style model number. A product therefore never carries its own model number; the SKU resolver reads it
-- from the linked style (product.style_id -> tech_card.model_no).
--
-- Numbers are minted from style_model_no_allocation, an AUTO_INCREMENT allocation table keyed UNIQUE by
-- style_id. That UNIQUE is the crash-idempotency guarantee (fixes problem 037): if a boot dies between
-- allocating a number and persisting it onto the style, the retry re-selects the SAME allocation row via
-- INSERT ... ON DUPLICATE KEY UPDATE instead of burning a second number and leaving a gap.
--
-- Runtime allocation (storeutil.AllocateStyleModelNo) locks the style FOR UPDATE, then
--   INSERT INTO style_model_no_allocation (style_id) VALUES (?)
--     ON DUPLICATE KEY UPDATE model_no = LAST_INSERT_ID(model_no)
-- and persists the number onto tech_card.model_no only while it is still NULL, then re-reads the winner.
--
-- Idempotent: table create guarded IF NOT EXISTS; guarded ADD COLUMN / ADD UNIQUE via information_schema
-- (multi-line PREPARE/EXECUTE/DEALLOCATE — a single line trips 1064 on the managed DSN, see 0124); the
-- backfill is gated on model_no IS NULL plus a NOT EXISTS provenance guard, so a re-run allocates nothing.

-- Allocation table. The AUTO_INCREMENT PK IS the minted model number; UNIQUE(style_id) makes a second
-- allocation for the same style impossible, which is what the ON DUPLICATE KEY retry relies on.
CREATE TABLE IF NOT EXISTS style_model_no_allocation (
    model_no   INT PRIMARY KEY AUTO_INCREMENT COMMENT 'the minted 5-digit model number for the style',
    style_id   INT NOT NULL,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE KEY uniq_style_model_no_allocation_style (style_id),
    CONSTRAINT fk_style_model_no_allocation_style FOREIGN KEY (style_id) REFERENCES tech_card(id) ON DELETE CASCADE
);

-- tech_card.model_no (UNIQUE; NULL until assigned).
SET @need_tc := (SELECT COUNT(*) = 0 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card' AND COLUMN_NAME = 'model_no');
SET @sql := IF(@need_tc,
    'ALTER TABLE tech_card ADD COLUMN model_no INT NULL',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

SET @need_tc_uq := (SELECT COUNT(*) = 0 FROM information_schema.STATISTICS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card' AND INDEX_NAME = 'uniq_tech_card_model_no');
SET @sql := IF(@need_tc_uq,
    'ALTER TABLE tech_card ADD CONSTRAINT uniq_tech_card_model_no UNIQUE (model_no)',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

-- Backfill: allocate a number for every existing tech_card lacking one, in created_at order so the
-- earliest style gets the lowest number. INSERT..SELECT assigns the AUTO_INCREMENT model_no sequentially
-- in the ORDER BY order (single-threaded insert). The NOT EXISTS guard is redundant with UNIQUE(style_id)
-- but makes the intent explicit and the re-run a no-op. Synthetic styles created later (0138) get their
-- number lazily from the runtime allocator (or a later hardening step) — they do not exist yet here.
INSERT INTO style_model_no_allocation (style_id)
    SELECT tc.id FROM tech_card tc
    WHERE tc.model_no IS NULL
      AND NOT EXISTS (SELECT 1 FROM style_model_no_allocation a WHERE a.style_id = tc.id)
    ORDER BY tc.created_at, tc.id;
UPDATE tech_card tc
    JOIN style_model_no_allocation a ON a.style_id = tc.id
    SET tc.model_no = a.model_no
    WHERE tc.model_no IS NULL;
