-- +migrate Up

-- Ф5а.3. Единица измерения becomes a closed vocabulary (entity.MaterialUnit / the proto MaterialUnit
-- enum). This migration collapses the CATALOGUE's legacy free-text spellings onto the canonical one.
--
-- Why it matters: every consumer compared units as raw strings. The production material plan carried
-- its own private metre synonym set (dto.planLengthUnits — m, м, meter, meters, metre, metres) and
-- degraded into a caveat on ANY other mismatch, so a slot spelled «м» against an article spelled "m"
-- counted as two different units. Precisely what that cost, and what it did NOT cost:
--
--   * The plan's ARITHMETIC on that pair was already correct. Both sides were metres, and the
--     fall-through branch left the quantity untouched (stockAdd was initialised to the computed
--     figure and only a real conversion ever replaced it), so the number netted against stock was
--     the right number. What was wrong was the REPORT around it — the row was labelled with the
--     slot's spelling instead of the catalogue's, and a caveat announced a unit conflict that did
--     not exist, which teaches an operator to distrust the caveats that DO matter.
--   * The pinned-article costing path (pinShadowBom) did lose a number outright — its EqualFold said
--     «м» and "m" disagreed, and a disagreement there leaves the line UNPRICED, which blocks the
--     cost seed. A silently missing figure, not a wrong one.
--   * The genuinely wrong arithmetic is the case this phase's kg conversion fixes, and it is a
--     different one: a slot in "m" against an article stocked in "kg". There the quantity really did
--     stay in metres while being subtracted from a balance kept in kilograms. That pair is now
--     converted through the roll's full width and density instead of being caveated and mis-netted.
--
-- The metre row below IS that pre-existing synonym set, lifted verbatim — it is not re-derived and
-- not extended, because it is what legacy metre values were already being matched against.
--
-- DATA SAFETY, three explicit choices:
--
--  1. A value the vocabulary does not know is LEFT ALONE. Nothing is guessed into a canonical unit:
--     a wrong guess makes two genuinely different quantities addable, which is worse than the free
--     text it replaces. Unmapped values keep working exactly as they do today (raw-string compare)
--     and surface on the wire as MATERIAL_UNIT_UNKNOWN — that IS the report of what needs a
--     human. Count them with:
--        SELECT unit, COUNT(*) FROM material WHERE unit IS NOT NULL AND TRIM(unit) <> ''
--          AND LOWER(TRIM(unit)) NOT IN ('m','cm','mm','m2','g','kg','pcs','pair','set','cone','roll')
--        GROUP BY unit ORDER BY 2 DESC;
--
--  2. `tech_card_bom_item.unit` is deliberately NOT rewritten. That column sits inside the SIGNED
--     MATERIALS digest projection (dto.materialsProjection), so respelling «м» → "m" would mark the
--     MATERIALS approval of every card that spells a unit non-canonically as "changed since
--     sign-off" — a wall of stale sign-offs for a change that alters nothing the card BUYS. The
--     entire functional benefit (canonical comparison, no phantom caveat, kg conversion) comes from
--     normalising in CODE, which costs no sign-off. If the owner wants BOM storage canonicalised
--     too, it is one more UPDATE here plus an accepted re-approval wave — a decision, not a detail.
--
--  3. No lock_version bump. A spelling change must not invalidate an admin's open editor, and the
--     write path re-canonicalises on save anyway. It also intentionally bypasses the
--     ErrMaterialUnitLocked guard (which forbids changing the unit of a material that already has
--     stock movements): that guard exists so historical quantities keep their meaning, and «м» → "m"
--     is the same unit — no quantity changes meaning.
--
-- Idempotent by construction: a second run matches the same rows and writes the identical value, and
-- MySQL does not touch a row whose columns are unchanged.

UPDATE material
SET unit = CASE LOWER(TRIM(unit))
        WHEN 'm' THEN 'm'
        WHEN 'м' THEN 'm'
        WHEN 'meter' THEN 'm'
        WHEN 'meters' THEN 'm'
        WHEN 'metre' THEN 'm'
        WHEN 'metres' THEN 'm'
        WHEN 'cm' THEN 'cm'
        WHEN 'см' THEN 'cm'
        WHEN 'mm' THEN 'mm'
        WHEN 'мм' THEN 'mm'
        WHEN 'm2' THEN 'm2'
        WHEN 'm²' THEN 'm2'
        WHEN 'sqm' THEN 'm2'
        WHEN 'g' THEN 'g'
        WHEN 'г' THEN 'g'
        WHEN 'kg' THEN 'kg'
        WHEN 'кг' THEN 'kg'
        WHEN 'pcs' THEN 'pcs'
        WHEN 'pc' THEN 'pcs'
        WHEN 'шт' THEN 'pcs'
        WHEN 'шт.' THEN 'pcs'
        WHEN 'pair' THEN 'pair'
        WHEN 'пара' THEN 'pair'
        WHEN 'set' THEN 'set'
        WHEN 'cone' THEN 'cone'
        WHEN 'бобина' THEN 'cone'
        WHEN 'roll' THEN 'roll'
        WHEN 'рулон' THEN 'roll'
        ELSE unit
    END
WHERE unit IS NOT NULL
  AND LOWER(TRIM(unit)) IN (
    'm', 'м', 'meter', 'meters', 'metre', 'metres',
    'cm', 'см', 'mm', 'мм', 'm2', 'm²', 'sqm',
    'g', 'г', 'kg', 'кг',
    'pcs', 'pc', 'шт', 'шт.', 'pair', 'пара', 'set',
    'cone', 'бобина', 'roll', 'рулон'
  );

-- +migrate Down

-- No-op: the pre-migration spellings are not recoverable (and are not information — «м» and "m" name
-- the same unit). Down exists so sql-migrate has a section, not because a rollback restores text.
SELECT 1;
