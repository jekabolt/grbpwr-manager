-- +migrate Up

-- PR6 phase 3 (POM / size chart to the style), step 2 of 2: drop the per-colourway size_measurement
-- table now that the catalogue size chart lives on the style (tech_card_size_measurement, 0141) and
-- the product read/write have been repointed to it. No table references size_measurement by FK, so a
-- plain drop is safe. Idempotent: DROP TABLE IF EXISTS.

DROP TABLE IF EXISTS size_measurement;
