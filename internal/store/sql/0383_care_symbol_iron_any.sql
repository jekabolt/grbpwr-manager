-- +migrate Up
-- Care vocabulary: IA, "iron at any temperature" (the plain iron, no dots).
--
-- 0217 seeded 39 ISO 3758 symbols but left out the unmarked iron, so a garment that tolerates any
-- iron temperature had no symbol to carry. The brand's care iconset (2026-09) draws it, so it joins
-- the dictionary here.
--
-- Placement: IA is the first Ironing symbol, ahead of IL (25). sort_order is UNIQUE, so everything
-- from 25 up moves one step first; the ORDER BY DESC walks from the top so no row ever lands on an
-- occupied value mid-update. The relative order of the existing 39 does not change, so every stored
-- care string (canonicalised to sort_order by CareIndex.Normalize) stays canonical as written.
--
-- Ironing is one slot (careSlotKey), so IA conflicts with IL/IM/IH/DNS/DNI on write exactly like
-- those already conflict with each other.
--
-- The inserts are INSERT IGNORE on the natural keys, joining language by code as 0217 does. The
-- shift is not guarded: sql-migrate runs a migration once, and a self-referencing guard on
-- care_symbol would trip MySQL 1093. One statement per line-ending semicolon — prod and beta run
-- without multiStatements.

UPDATE care_symbol SET sort_order = sort_order + 1 WHERE sort_order >= 25 ORDER BY sort_order DESC;

INSERT IGNORE INTO care_symbol (code, category, sub_category, name, short_prose, sort_order) VALUES
    ('IA', 'Ironing', NULL, 'Iron at Any Temperature', 'iron at any temperature', 25);

INSERT IGNORE INTO care_symbol_translation (care_code, language_id, short_prose)
SELECT t.code, l.id, t.prose
  FROM (
            SELECT 'IA' AS code, 'fr' AS lang, 'repassage à toute température' AS prose
    UNION ALL SELECT 'IA' AS code, 'de' AS lang, 'bügeln bei jeder Temperatur' AS prose
    UNION ALL SELECT 'IA' AS code, 'it' AS lang, 'stirare a qualsiasi temperatura' AS prose
    UNION ALL SELECT 'IA' AS code, 'ja' AS lang, '温度を問わずアイロン可' AS prose
    UNION ALL SELECT 'IA' AS code, 'cn' AS lang, '可任意温度熨烫' AS prose
    UNION ALL SELECT 'IA' AS code, 'kr' AS lang, '온도 제한 없이 다림질' AS prose
  ) t
  JOIN language l ON l.code = t.lang
  JOIN care_symbol c ON c.code = t.code;

-- +migrate Down
DELETE FROM care_symbol_translation WHERE care_code = 'IA';
DELETE FROM care_symbol WHERE code = 'IA';
UPDATE care_symbol SET sort_order = sort_order - 1 WHERE sort_order >= 26 ORDER BY sort_order ASC;
