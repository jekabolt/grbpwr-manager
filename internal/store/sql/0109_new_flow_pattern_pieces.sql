-- +migrate Up

-- new-flow NF-05: a structured list of cut-pieces (детали кроя) for a tech card, plus a typed
-- matrix «piece × colourway → BOM fabric». Until now the only "piece" notions were free text
-- (tech_card_callout.part, tech_card_detail free aspects) or per-size PDF patterns
-- (tech_card_size_pattern) — none of them a structural list of what the garment is cut from, and
-- no way to say "for THIS colourway, this piece is cut from THAT fabric".
--
-- tech_card_piece_material references the colourway by real FK (colorway_id) — the store resolves
-- the positional colorway_index from the payload to the freshly-inserted colorway id inside the
-- same Create/Update transaction (colourways are full-replace, so ids are recreated). BOM refs stay
-- POSITIONAL (bom_item_index) to match tech_card_colorway_usage / tech_card_operation.
--
-- All CREATE TABLE IF NOT EXISTS with inline indexes/CHECKs (named), and the ALTER ... ADD COLUMN
-- is guarded via information_schema (MySQL 8 has no ADD COLUMN IF NOT EXISTS) so the file is
-- idempotent — a mid-file DDL failure re-runs cleanly from the top.

CREATE TABLE IF NOT EXISTS tech_card_piece (
    id INT AUTO_INCREMENT PRIMARY KEY,
    tech_card_id INT NOT NULL,
    name VARCHAR(255) NOT NULL,                            -- «полочка», «спинка», «обтачка горловины»…
    pieces_per_garment INT NOT NULL DEFAULT 1,             -- how many times cut per garment
    mirrored BOOLEAN NOT NULL DEFAULT FALSE,               -- paired / mirror piece
    grainline VARCHAR(16) NOT NULL DEFAULT 'lengthwise',   -- долевая: lengthwise|crosswise|bias|any
    fused BOOLEAN NOT NULL DEFAULT FALSE,                  -- клеевая (fused with interlining)
    callout_number INT NULL,                               -- link to a sketch callout pin (like operations)
    note VARCHAR(255) NULL,
    display_order INT NOT NULL DEFAULT 0,
    CONSTRAINT fk_tcp_tech_card FOREIGN KEY (tech_card_id) REFERENCES tech_card(id) ON DELETE CASCADE,
    CONSTRAINT chk_tcp_grainline CHECK (grainline REGEXP '^(lengthwise|crosswise|bias|any)$'),
    INDEX idx_tcp_tech_card (tech_card_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

-- piece × colourway → fabric (BOM line). For each colourway a piece may be cut from a different
-- fabric; fusing_bom_item_index is the interlining/клеевая for that colourway, if any.
CREATE TABLE IF NOT EXISTS tech_card_piece_material (
    id INT AUTO_INCREMENT PRIMARY KEY,
    piece_id INT NOT NULL,
    colorway_id INT NOT NULL,
    bom_item_index INT NULL,             -- positional ref into bom_items (the fabric), NULL = unset
    fusing_bom_item_index INT NULL,      -- positional ref into bom_items (the fusing), NULL = none
    note VARCHAR(255) NULL,
    display_order INT NOT NULL DEFAULT 0,
    CONSTRAINT fk_tcpm_piece FOREIGN KEY (piece_id) REFERENCES tech_card_piece(id) ON DELETE CASCADE,
    CONSTRAINT fk_tcpm_colorway FOREIGN KEY (colorway_id) REFERENCES tech_card_colorway(id) ON DELETE CASCADE,
    UNIQUE KEY uniq_tcpm (piece_id, colorway_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

-- optional arrow on an existing consumption norm: which cut-piece it is about (informational in v1;
-- consumption stays on the usage — piece geometry does not drive it, the nesting marker does).
SET @need_col := (SELECT COUNT(*) = 0 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_colorway_usage' AND COLUMN_NAME = 'piece_index');
SET @sql := IF(@need_col,
    'ALTER TABLE tech_card_colorway_usage ADD COLUMN piece_index INT NULL',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

-- +migrate Down

DROP TABLE IF EXISTS tech_card_piece_material;
DROP TABLE IF EXISTS tech_card_piece;
-- (leaves tech_card_colorway_usage.piece_index; a Down is not exercised in prod automigrate)
SELECT 1;
