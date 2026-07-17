-- +migrate Up
-- tech_card.composition is a native JSON column (0139). The old frontend stored the free-text
-- composition as a JSON ARRAY of strings (e.g. ["100% cotton"], ["70% cotton","30% polyester"]) rather
-- than a scalar. The M1 read path JSON_UNQUOTEs the value, which unwraps a scalar ("100% cotton" ->
-- 100% cotton) but leaves an array as its literal text — so the storefront/admin showed the raw
-- ["100% COTTON"]. Flatten every array value to a scalar plain-text JSON string once, joining string
-- elements with ", "; after this JSON_UNQUOTE (styleCompositionSelect) and entity.UnquoteLegacyComposition
-- surface plain text everywhere.
--
-- Idempotent / crash-safe: the flattened result is a JSON scalar, so a re-run's WHERE (JSON_TYPE = 'ARRAY')
-- matches nothing. Non-string array elements (defensive — no known data) are dropped; an all-non-string
-- array collapses to '' rather than leaking object text onto the storefront. Only ARRAY-typed rows are
-- touched — existing scalar and NULL compositions are left exactly as they are.
UPDATE tech_card
SET composition = JSON_QUOTE(COALESCE((
        SELECT GROUP_CONCAT(JSON_UNQUOTE(jt.val) ORDER BY jt.idx SEPARATOR ', ')
        FROM JSON_TABLE(composition, '$[*]' COLUMNS (idx FOR ORDINALITY, val JSON PATH '$')) AS jt
        WHERE JSON_TYPE(jt.val) = 'STRING'
    ), ''))
WHERE composition IS NOT NULL AND JSON_TYPE(composition) = 'ARRAY';

-- +migrate Down
-- No down: the original per-row array encoding is not recoverable (and was never a contract), and the
-- flattened scalar is the intended plain-text form going forward.
SELECT 1;
