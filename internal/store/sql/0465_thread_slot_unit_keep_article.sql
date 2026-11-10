-- Thread units: a THREAD slot authored in metres now reads in metres even when its article is
-- bought in another unit (cone, pcs, spool…); the price is converted per metre. Before this, the
-- article unit won on read, so an old slot stored as `m` on a non-metre article was really priced
-- and normed in the article unit. Keep that meaning: such old rows take the article unit, exactly
-- what they displayed before. A thread line is put into metres again by re-linking its article.
--
-- Data only, idempotent: after the update the condition no longer matches. Prod: 4 rows, none with
-- a norm or a topstitch line. Safe under ANSI_QUOTES (single-quoted literals only).

-- +migrate Up

UPDATE tech_card_bom_item bi
JOIN material m ON m.id = bi.material_id
SET bi.unit = m.unit
WHERE bi.section = 'thread'
  AND LOWER(TRIM(bi.unit)) IN ('m', 'м', 'meter', 'meters', 'metre', 'metres')
  AND TRIM(m.unit) <> ''
  AND LOWER(TRIM(m.unit)) NOT IN ('m', 'м', 'meter', 'meters', 'metre', 'metres');

-- +migrate Down
