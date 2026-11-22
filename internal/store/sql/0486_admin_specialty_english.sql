-- +migrate Up

-- Seeded specialties in English lowercase (owner rule: the UI is English). 0315 seeded them in
-- Russian; this renames each seeded row IN PLACE, so its id — what admin_specialty_link stores —
-- stays the same and every account keeps its specialty.
--
-- A team that already typed the English name by hand has both rows. There the Russian row's links
-- move to the English row, then the Russian row goes (its links are gone, so the RESTRICT FK lets
-- it). Then the rename covers the rest. Names a team added itself are left alone.
--
-- Idempotent: after one run no Russian seed name is left, so every statement matches nothing.
-- Strings in single quotes only (ANSI_QUOTES). Rows only — no DDL.
-- compat: lazy seeded display names; the code reads whatever name a row carries

INSERT IGNORE INTO admin_specialty_link (admin_id, specialty_id)
SELECT l.admin_id, e.id
FROM admin_specialty_link l
JOIN admin_specialty s ON s.id = l.specialty_id
JOIN (
  SELECT 'дизайнер' AS ru, 'designer' AS en UNION ALL
  SELECT 'графический дизайнер', 'graphic designer' UNION ALL
  SELECT 'конструктор', 'pattern maker' UNION ALL
  SELECT 'технолог', 'technologist' UNION ALL
  SELECT 'фотограф', 'photographer' UNION ALL
  SELECT 'видеограф', 'videographer' UNION ALL
  SELECT 'стилист', 'stylist' UNION ALL
  SELECT 'продюсер съёмки', 'shoot producer' UNION ALL
  SELECT 'копирайтер', 'copywriter' UNION ALL
  SELECT 'маркетинг', 'marketing' UNION ALL
  SELECT 'биздев', 'business' UNION ALL
  SELECT 'разработчик', 'developer' UNION ALL
  SELECT 'производство', 'production'
) m ON s.name = m.ru
JOIN admin_specialty e ON e.name = m.en;

DELETE l
FROM admin_specialty_link l
JOIN admin_specialty s ON s.id = l.specialty_id
JOIN (
  SELECT 'дизайнер' AS ru, 'designer' AS en UNION ALL
  SELECT 'графический дизайнер', 'graphic designer' UNION ALL
  SELECT 'конструктор', 'pattern maker' UNION ALL
  SELECT 'технолог', 'technologist' UNION ALL
  SELECT 'фотограф', 'photographer' UNION ALL
  SELECT 'видеограф', 'videographer' UNION ALL
  SELECT 'стилист', 'stylist' UNION ALL
  SELECT 'продюсер съёмки', 'shoot producer' UNION ALL
  SELECT 'копирайтер', 'copywriter' UNION ALL
  SELECT 'маркетинг', 'marketing' UNION ALL
  SELECT 'биздев', 'business' UNION ALL
  SELECT 'разработчик', 'developer' UNION ALL
  SELECT 'производство', 'production'
) m ON s.name = m.ru
JOIN admin_specialty e ON e.name = m.en;

DELETE s
FROM admin_specialty s
JOIN (
  SELECT 'дизайнер' AS ru, 'designer' AS en UNION ALL
  SELECT 'графический дизайнер', 'graphic designer' UNION ALL
  SELECT 'конструктор', 'pattern maker' UNION ALL
  SELECT 'технолог', 'technologist' UNION ALL
  SELECT 'фотограф', 'photographer' UNION ALL
  SELECT 'видеограф', 'videographer' UNION ALL
  SELECT 'стилист', 'stylist' UNION ALL
  SELECT 'продюсер съёмки', 'shoot producer' UNION ALL
  SELECT 'копирайтер', 'copywriter' UNION ALL
  SELECT 'маркетинг', 'marketing' UNION ALL
  SELECT 'биздев', 'business' UNION ALL
  SELECT 'разработчик', 'developer' UNION ALL
  SELECT 'производство', 'production'
) m ON s.name = m.ru
JOIN admin_specialty e ON e.name = m.en;

UPDATE admin_specialty s
JOIN (
  SELECT 'дизайнер' AS ru, 'designer' AS en UNION ALL
  SELECT 'графический дизайнер', 'graphic designer' UNION ALL
  SELECT 'конструктор', 'pattern maker' UNION ALL
  SELECT 'технолог', 'technologist' UNION ALL
  SELECT 'фотограф', 'photographer' UNION ALL
  SELECT 'видеограф', 'videographer' UNION ALL
  SELECT 'стилист', 'stylist' UNION ALL
  SELECT 'продюсер съёмки', 'shoot producer' UNION ALL
  SELECT 'копирайтер', 'copywriter' UNION ALL
  SELECT 'маркетинг', 'marketing' UNION ALL
  SELECT 'биздев', 'business' UNION ALL
  SELECT 'разработчик', 'developer' UNION ALL
  SELECT 'производство', 'production'
) m ON s.name = m.ru
SET s.name = m.en;

-- +migrate Down
-- No-op: the English names are the ones kept; renaming back would put Russian into the UI again.
SELECT 1;
