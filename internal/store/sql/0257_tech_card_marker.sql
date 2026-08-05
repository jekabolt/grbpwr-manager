-- +migrate Up

-- Saved раскладки (markers). The client-side nesting (раскладка) lays one size's pattern pieces
-- onto a strip of fabric and measures the used length; until now the result died with the modal.
-- A marker persists that measurement together with SELF-CONTAINED geometry, so costing can read
-- fabric consumption per garment (used_length_cm / sets) and the layout can be reopened, adjusted
-- and re-exported for the plotter without re-parsing any DXF.
--
-- Geometry lives in the `layout` JSON blob (proto-JSON of common.TechCardMarkerLayout, idiom of
-- tech_card_release.snapshot) and NOT as references to pattern files, for three load-bearing
-- reasons. Pattern object URLS are not stable (0260 later gave rows a stable line_key, but the
-- FILE behind a row still changes on sheet replacement). The pattern garbage collector deletes
-- the CDN object as soon as no pattern row references its url, so a marker-by-reference would go
-- dark on the next sheet replacement. And a DXF re-parse is not reproducible identity-wise
-- (piece ids are parse-local, tessellation depends on tolerances).
--
-- bom_item_id is ON DELETE SET NULL, a deliberate divergence from the RESTRICT used by
-- tech_card_colorway_usage/piece_material pins. The BOM is upsert-diffed INSIDE UpdateTechCard,
-- so a RESTRICT here would fail the whole card save the moment an operator deletes a fabric slot
-- a marker once measured. A marker is a measurement, not a structural reference -- orphaned, it
-- stays valid geometry and merely drops out of costing suggestions.
--
-- No table charset clause on purpose (0252 precedent) -- prod/beta run the utf8mb3 server default
-- while local container tests connect utf8mb4, and an explicit clause would diverge them invisibly.
CREATE TABLE IF NOT EXISTS tech_card_marker (
    id              INT PRIMARY KEY AUTO_INCREMENT,
    tech_card_id    INT          NOT NULL,
    size_id         INT          NOT NULL COMMENT 'the size whose pieces this marker lays out; must be in the card range at save time, may outlive it like pattern rows',
    bom_item_id     INT          NULL COMMENT 'the BOM fabric line this marker measures; NULL = never linked or the slot was deleted (SET NULL)',
    name            VARCHAR(191) NOT NULL,
    source          VARCHAR(24)  NOT NULL DEFAULT 'auto' COMMENT 'layout provenance, auto (nesting engine) / manual (operator-adjusted) / imported (external CAD)',
    fabric_width_cm DECIMAL(10,2) NOT NULL,
    gap_cm          DECIMAL(6,2)  NOT NULL DEFAULT 0,
    edge_margin_cm  DECIMAL(6,2)  NOT NULL DEFAULT 0,
    allow_cross_grain BOOLEAN     NOT NULL DEFAULT FALSE,
    sets            INT           NOT NULL DEFAULT 1 COMMENT 'комплектов, garments cut at once; consumption per unit derives as used_length_cm / sets',
    used_length_cm  DECIMAL(10,2) NOT NULL,
    efficiency_pct  DECIMAL(5,2)  NULL,
    placed_count    INT           NOT NULL,
    total_count     INT           NOT NULL,
    layout          JSON          NOT NULL COMMENT 'proto-JSON of common.TechCardMarkerLayout, self-contained contours + placements',
    created_by      VARCHAR(255)  NOT NULL DEFAULT '',
    updated_by      VARCHAR(255)  NOT NULL DEFAULT '',
    created_at      TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at      TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    CONSTRAINT chk_tcm_source CHECK (source IN ('auto', 'manual', 'imported')),
    CONSTRAINT chk_tcm_width_pos CHECK (fabric_width_cm > 0),
    CONSTRAINT chk_tcm_gap_nonneg CHECK (gap_cm >= 0),
    CONSTRAINT chk_tcm_margin_nonneg CHECK (edge_margin_cm >= 0),
    CONSTRAINT chk_tcm_sets_pos CHECK (sets >= 1),
    CONSTRAINT chk_tcm_length_nonneg CHECK (used_length_cm >= 0),
    CONSTRAINT chk_tcm_efficiency CHECK (efficiency_pct IS NULL OR (efficiency_pct >= 0 AND efficiency_pct <= 100)),
    CONSTRAINT chk_tcm_counts CHECK (placed_count >= 0 AND total_count >= 1),
    -- One name per (card, size). Two markers MAY measure the same size (different widths, different
    -- BOM slots) -- the name is how the operator tells them apart, so it has to actually do that.
    CONSTRAINT uniq_tcm_card_size_name UNIQUE (tech_card_id, size_id, name),
    CONSTRAINT fk_tcm_card FOREIGN KEY (tech_card_id) REFERENCES tech_card(id) ON DELETE CASCADE,
    -- RESTRICT mirrors the size FK tightening of 0149 -- sizes are dictionary rows, never deleted
    -- in practice, and a marker silently losing its size would make its consumption unattributable.
    CONSTRAINT fk_tcm_size FOREIGN KEY (size_id) REFERENCES size(id) ON DELETE RESTRICT,
    CONSTRAINT fk_tcm_bom FOREIGN KEY (bom_item_id) REFERENCES tech_card_bom_item(id) ON DELETE SET NULL,
    INDEX idx_tcm_card_size (tech_card_id, size_id)
) ENGINE=InnoDB COMMENT 'Saved fabric-layout markers (раскладки) per tech card size, measured consumption + self-contained geometry';

-- +migrate Down
DROP TABLE IF EXISTS tech_card_marker;
