-- +migrate Up
-- Economics audit — Wave 3 / task 09 (phase 3): production-run stock + cost integration.
-- Closes the chain техкарта → партия → склад → cost_price → маржа. Receiving a run increments
-- product_size stock and writes product_stock_change_history rows with the new `production_received`
-- source (the run id goes in the generic reference_id — source is free-text VARCHAR, no schema
-- change there). Optionally the run's actual unit cost sets product.cost_price with provenance
-- source='production_run' — this adds the run link column next to the existing cost_price_tech_card_id
-- (task 02 / migration 0093). Production kanban cards gain a typed production_run_id, mirroring the
-- fitting_id link, so a card can point at the batch it is about.

ALTER TABLE task
  ADD COLUMN production_run_id INT NULL
    COMMENT 'production run this card is about (typed link, like fitting_id)',
  ADD CONSTRAINT fk_task_production_run FOREIGN KEY (production_run_id) REFERENCES production_run(id) ON DELETE SET NULL;

ALTER TABLE product
  ADD COLUMN cost_price_production_run_id INT NULL
    COMMENT 'run whose actual unit cost set cost_price (when cost_price_source = production_run)',
  ADD CONSTRAINT fk_product_cost_production_run FOREIGN KEY (cost_price_production_run_id) REFERENCES production_run(id) ON DELETE SET NULL;

-- +migrate Down
ALTER TABLE product
  DROP FOREIGN KEY fk_product_cost_production_run,
  DROP COLUMN cost_price_production_run_id;

ALTER TABLE task
  DROP FOREIGN KEY fk_task_production_run,
  DROP COLUMN production_run_id;
