-- +migrate Up
-- P4-flyover M1 (tmp/plm-rework/04-MAZE-FLYOVER.md): seed the controlled `fiber` dictionary (table
-- added empty by 0167_composition.sql) with the ten base fibres the PLM composition model needs day
-- one. Structural composition (material_composition / bom_item_composition / style_composition, each
-- FK fiber_code -> fiber(code) ON DELETE RESTRICT) landed as dead schema — with zero fiber rows, every
-- insert into those tables would have been rejected by the FK regardless of any store-layer wiring.
-- This unblocks the derive-from-BOM path (entity.DeriveStyleComposition / ReconcileStyleComposition,
-- now called from UpdateStyle / UpdateColorwayRecipe).
--
-- There is no dictionary CRUD (Create/Update/Archive RPC) for fiber yet, unlike collection/tag/colour
-- (internal/apisrv/admin/dictionary_crud.go, R9). FOLLOW-UP: add ListFibers/CreateFiber/ArchiveFiber
-- mirroring that pattern once composition authoring needs more than this seed. Until then, a new fibre
-- needs its own follow-up migration (same INSERT IGNORE pattern as this one).
--
-- Idempotent: INSERT IGNORE on the table's existing PRIMARY KEY (code); a rerun or a mid-file crash is
-- a no-op.

INSERT IGNORE INTO fiber (code, name) VALUES
    ('COT', 'Cotton'),
    ('WOL', 'Wool'),
    ('POL', 'Polyester'),
    ('ELS', 'Elastane'),
    ('VIS', 'Viscose'),
    ('SLK', 'Silk'),
    ('LIN', 'Linen'),
    ('NYL', 'Nylon'),
    ('CSH', 'Cashmere'),
    ('LEA', 'Leather');

-- +migrate Down
DELETE FROM fiber WHERE code IN ('COT', 'WOL', 'POL', 'ELS', 'VIS', 'SLK', 'LIN', 'NYL', 'CSH', 'LEA');
