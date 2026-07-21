-- +migrate Up
-- NUMBERING - the gap from 0189 to 0197 is intentional. Those numbers are already taken by the
-- in-flight accounting branches (accounting_core through depreciation_corptax_accrual) which are not
-- merged to master yet. Renumbering this file into the gap would collide on their merge, and
-- sql-migrate tracks migrations by full filename, so a later rename reapplies them. Leave it at 0198.
--
-- Backfill tech_card's (top_category_id, sub_category_id, type_id) from the single category_id that
-- Create/UpdateTechCard writes. category_id is now the source of truth for a style's taxonomy and the
-- triple is derived from it on every tech-card write; this fills in the styles created BEFORE that
-- derivation existed, whose triple is NULL and which therefore break size-system resolution
-- (a NULL top_category_id reads as "no category" and disables size validation entirely), storefront
-- category filters, and the category breakdowns in metrics.
--
-- Classification is by category.level_id, never by position in the parent chain. Dresses is the
-- exception that forces this - its level-3 types hang DIRECTLY off the level-1 top category with no
-- sub-category in between (0001_initial_setup.sql, "Dresses types (no sub-category)"), so a dress
-- must derive as top=dresses, sub=NULL, type=mini. A positional walk would file it as sub=mini,
-- inventing a sub-category that does not exist. Levels 1/2/3 are top_category/sub_category/type per
-- the category_level seed in 0001. Mirrors entity.DeriveStyleCategoryPath, which owns the same
-- classification for all writes from here on.
--
-- Conservative by design - only rows where ALL THREE columns are NULL are touched, so a triple
-- already written by the legacy UpdateStyle path is never overwritten. This matches the runtime
-- rule that a tech-card write leaves the triple alone when category_id is unset - the wire cannot
-- distinguish "field omitted" from "field cleared", so neither may destroy a category it did not set.
--
-- Rows whose chain reaches no level-1 ancestor are skipped rather than given a headless partial path,
-- for the same reason the Go derivation refuses them.
--
-- Idempotent - the WHERE requires all three to be NULL, so every row this touches stops matching and a
-- rerun updates nothing. Pure DML with no DDL, so there is no half-applied schema to recover from.

UPDATE tech_card tc
JOIN      category c  ON c.id  = tc.category_id
LEFT JOIN category p1 ON p1.id = c.parent_id
LEFT JOIN category p2 ON p2.id = p1.parent_id
SET
    tc.top_category_id = COALESCE(
        CASE WHEN c.level_id  = 1 THEN c.id  END,
        CASE WHEN p1.level_id = 1 THEN p1.id END,
        CASE WHEN p2.level_id = 1 THEN p2.id END),
    tc.sub_category_id = COALESCE(
        CASE WHEN c.level_id  = 2 THEN c.id  END,
        CASE WHEN p1.level_id = 2 THEN p1.id END,
        CASE WHEN p2.level_id = 2 THEN p2.id END),
    tc.type_id = COALESCE(
        CASE WHEN c.level_id  = 3 THEN c.id  END,
        CASE WHEN p1.level_id = 3 THEN p1.id END,
        CASE WHEN p2.level_id = 3 THEN p2.id END)
WHERE tc.category_id     IS NOT NULL
  AND tc.top_category_id IS NULL
  AND tc.sub_category_id IS NULL
  AND tc.type_id         IS NULL
  AND (c.level_id = 1 OR p1.level_id = 1 OR p2.level_id = 1);

-- +migrate Down
-- No inverse. A derived triple is indistinguishable from one the legacy UpdateStyle path wrote, so
-- clearing it on rollback would destroy category assignments this migration never made. The Up is
-- additive (it only fills NULLs) and harmless to leave in place if the code is rolled back.
SELECT 1;
