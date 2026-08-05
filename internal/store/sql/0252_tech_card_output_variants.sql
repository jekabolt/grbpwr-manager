-- +migrate Up
-- Colour variants of an AUXILIARY card's warehouse output. One aux card (кофр, dust bag, shopper)
-- is sewn from several fabrics, and each fabric is a different physical thing on the shelf — a
-- different stock bucket, a different moving average, a different low-stock answer. Until now an
-- aux card had exactly ONE output material (tech_card.output_material_id, migration 0111), so the
-- only way to model two colours was two tech cards with a duplicated BOM and construction.
--
-- This table is the colour dimension of that single output. ZERO rows for a card means legacy
-- single-output mode and byte-identical behaviour; the first row switches the card into variant
-- mode. No backfill on purpose — cards with no colour dimension (care labels, boxes) never opt in.
--
-- NOT to be confused with the other two things this codebase calls a "variant". product_size rows
-- are size variants of a sellable colourway, and 0249 added A/B grade variants of received goods.
-- Everything here carries the output_ prefix precisely because "variant" alone is ambiguous.
--
-- NO table charset clause on purpose. prod/beta run the utf8mb3 server default while local
-- container tests connect utf8mb4, so an explicit utf8mb4 declaration here would make color_code
-- CHAR(3) a different charset from color.code on prod and the FK would fail with Error 3780 —
-- invisibly, because the container test would have passed. color (0130) declares none either.
CREATE TABLE IF NOT EXISTS tech_card_output_variant (
    id           INT PRIMARY KEY AUTO_INCREMENT,
    tech_card_id INT     NOT NULL COMMENT 'auxiliary card; purpose=auxiliary is enforced in the service layer like output_material_id (0111)',
    color_code   CHAR(3) NOT NULL COMMENT 'FK color dictionary, the same colour identity as product.color_code',
    material_id  INT     NOT NULL COMMENT 'warehouse bucket this colour PRODUCES into (opposite direction of tech_card_colorway_usage.material_id from 0221, which is what a colourway CONSUMES)',
    active       BOOLEAN NOT NULL DEFAULT TRUE,
    created_by   VARCHAR(255) NOT NULL DEFAULT '',
    updated_by   VARCHAR(255) NOT NULL DEFAULT '',
    created_at   TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at   TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    -- One row per colour on a card. Retirement is `active = FALSE`, not a second row.
    CONSTRAINT uniq_tcov_card_color UNIQUE (tech_card_id, color_code),
    -- One stock bucket belongs to exactly one colour of one card, or the moving average of that
    -- bucket blends two different physical articles and costing stops being true. Note that
    -- tech_card.output_material_id has no such UNIQUE, so N legacy cards may still share one
    -- material — the service layer must report the collision as "already claimed", not as a 500.
    CONSTRAINT uniq_tcov_material UNIQUE (material_id),
    -- CASCADE is safe. A card with production runs cannot be deleted at all (the delete guard in
    -- the tech-card store refuses it), and phase 3's production_run_line FK will RESTRICT against
    -- the variant itself, so nothing that references a variant can be orphaned by a card delete.
    CONSTRAINT fk_tcov_card FOREIGN KEY (tech_card_id) REFERENCES tech_card(id) ON DELETE CASCADE,
    -- Colour codes are renamed in place occasionally and never hard-deleted, mirroring 0130.
    CONSTRAINT fk_tcov_color FOREIGN KEY (color_code) REFERENCES color(code) ON DELETE RESTRICT ON UPDATE CASCADE,
    -- RESTRICT, mirroring the 0221 pin. A hard delete of a claimed article must be refused loudly.
    CONSTRAINT fk_tcov_material FOREIGN KEY (material_id) REFERENCES material(id) ON DELETE RESTRICT
) ENGINE=InnoDB COMMENT 'Colour variants of an auxiliary card''s warehouse output (extends 0111 output_material_id; NOT product_size variants)';

-- +migrate Down
DROP TABLE IF EXISTS tech_card_output_variant;
