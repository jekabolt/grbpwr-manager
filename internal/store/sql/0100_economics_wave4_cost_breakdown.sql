-- +migrate Up
-- Economics audit task 15 (Part B): store the COGS DECOMPOSITION alongside the flat
-- product.cost_price. When a product's cost is seeded from its primary tech card, the seed
-- also snapshots the per-unit cost articles in base currency (EUR) —
-- {materials, cmt, hardware, packaging, logistics, overhead, defect_pct} — so the
-- "structure of COGS sold" report can attribute a period's cost of goods to its components
-- (materials vs CMT vs …) and a margin bridge can say WHICH component moved.
--
-- NULL = no decomposition known: a manually-set cost_price (no card), or a cost seeded before
-- this column existed. The structure report buckets those units as "unattributed" so coverage
-- stays honest. Written next to cost_price by the same seed path and only for the same
-- (primary, non-manual) products, so the two never drift. JSON on the product (rather than a
-- join to the release snapshot) keeps the aggregation a single JSON_EXTRACT in SQL.
ALTER TABLE product
  ADD COLUMN cost_breakdown JSON NULL
    COMMENT 'per-unit COGS decomposition in base currency (EUR) snapshotted at cost seed; NULL = unknown (manual cost / pre-feature)';

-- +migrate Down
ALTER TABLE product
  DROP COLUMN cost_breakdown;
