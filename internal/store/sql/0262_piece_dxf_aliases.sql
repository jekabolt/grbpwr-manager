-- +migrate Up

-- DXF block-name aliases for cut-pieces (PIECES-WASTAGE-DESIGN §2.2, fabric-scope amendment).
-- A DXF file's blocks carry whatever names the exporter minted («PERED_S», «front-38», «деталь_1»);
-- the canonical piece name lives ONLY in tech_card_piece.name. This table records, per fabric slot
-- (bom_line_key, the same binding pattern rows carry since 0260), which block name means which
-- cut-piece, so re-uploads and other size files of the SAME fabric match automatically while a
-- generic block name in ANOTHER fabric's file maps independently (the owner's collision case).
--
-- No table charset clause on purpose (0252 precedent) -- prod runs the utf8mb3 server default while
-- container tests connect utf8mb4. The default CI collation makes the UNIQUE case-insensitive, so
-- «ПОЛОЧКА» and «полочка» collapse into one alias within a slot, which is exactly the wanted dedupe.
--
-- piece_id is ON DELETE CASCADE, a deliberate divergence from usage.piece_id RESTRICT (0168) --
-- pieces are diffed INSIDE UpdateTechCard, so RESTRICT would abort the whole card save when a piece
-- is deleted. A deleted piece simply drops its aliases and the block reads as unmatched again.
--
-- Renumber note -- 0260 is taken on this branch and 0261 by the parallel wastage branch; renaming
-- pre-apply is safe if the merge order shifts.
CREATE TABLE IF NOT EXISTS tech_card_piece_dxf_block (
    id            INT PRIMARY KEY AUTO_INCREMENT,
    tech_card_id  INT          NOT NULL,
    bom_line_key  VARCHAR(26)  NOT NULL COMMENT 'fabric BOM line whose DXF files this mapping is scoped to',
    block_name    VARCHAR(255) NOT NULL COMMENT 'normalized DXF block name (trimmed, inner whitespace collapsed)',
    piece_id      INT          NOT NULL,
    created_by    VARCHAR(255) NOT NULL DEFAULT '',
    updated_by    VARCHAR(255) NOT NULL DEFAULT '',
    created_at    TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at    TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    CONSTRAINT uniq_tcpdb_card_slot_block UNIQUE (tech_card_id, bom_line_key, block_name),
    CONSTRAINT fk_tcpdb_card  FOREIGN KEY (tech_card_id) REFERENCES tech_card(id) ON DELETE CASCADE,
    CONSTRAINT fk_tcpdb_piece FOREIGN KEY (piece_id) REFERENCES tech_card_piece(id) ON DELETE CASCADE,
    INDEX idx_tcpdb_piece (piece_id)
) ENGINE=InnoDB COMMENT 'DXF block name to cut-piece aliases, scoped per (tech card, fabric slot)';

-- +migrate Down
DROP TABLE IF EXISTS tech_card_piece_dxf_block;
