-- CATEGORY TAXONOMY GAP — wave techcard-ux-0925 (T03): 18 sub-categories and 35 types the catalogue
-- was missing (polos, leggings, jumpsuits, eyewear, crossbody bags, henleys, sweatpants, midi
-- dresses, overcoats, ...), so a style no longer has to be filed under the nearest wrong node.
--
-- PURELY ADDITIVE. No existing row is renamed, moved or deleted, so every stored category_id and
-- every style's top/sub/type triple means exactly what it meant before. Size systems need no rows:
-- a new sub-category or type inherits its top category's policy through
-- entity.ResolveSizeSystemPolicy (category_size_system is keyed on the top level, 0175).
-- category_translation is not touched — it was dropped in 0008 and nothing reads it.
--
-- IDEMPOTENT ROW BY ROW. Each INSERT adds its row only when no row with the same (name, parent)
-- exists — the very pair UNIQUE KEY unique_name_parent (name, parent_id) guards — so a re-run after
-- a partial apply inserts exactly what is still missing and never trips 1062.
--
-- PARENTS ARE LOOKED UP BY (name, level_id), NEVER BY NAME ALONE. Names repeat across the tree
-- (leather, mesh, cargo and cropped hang under several parents; slippers_loafers is both a
-- sub-category and a type), so a name-only lookup can bind to the wrong parent, or to two at once.
-- Top categories are unique by name within level 1 and sub-categories within level 2, so the pair is
-- exact. `dresses` is a TOP category whose types hang directly under it (0001) — hence level_id = 1
-- for that one parent.
--
-- ORDER MATTERS: the four sub-categories that receive types in this same file (jumpsuits, eyewear,
-- wallets, dress_shoes) are inserted above their types. The bags sub-category is `duffle_bags`, not
-- `duffle`: `duffle` already names a coat type, and its storefront translations say "duffle coat".
--
-- Plain INSERT ... SELECT, one statement per line — no PREPARE needed, and nothing here depends on
-- multiStatements (prod runs without it).
--
-- DOWN removes exactly these 53 rows, types before sub-categories. A row a style already points at
-- is refused by the tech_card category foreign keys (1451) — the right outcome: a node in use cannot
-- be un-seeded silently.

-- +migrate Up

-- Sub-categories (level 2) under their top categories (level 1).
-- tops › polos, bodysuits, blouses
INSERT INTO category (name, level_id, parent_id) SELECT 'polos', 2, c.id FROM category c WHERE c.name = 'tops' AND c.level_id = 1 AND NOT EXISTS (SELECT 1 FROM category x WHERE x.name = 'polos' AND x.parent_id = c.id);
INSERT INTO category (name, level_id, parent_id) SELECT 'bodysuits', 2, c.id FROM category c WHERE c.name = 'tops' AND c.level_id = 1 AND NOT EXISTS (SELECT 1 FROM category x WHERE x.name = 'bodysuits' AND x.parent_id = c.id);
INSERT INTO category (name, level_id, parent_id) SELECT 'blouses', 2, c.id FROM category c WHERE c.name = 'tops' AND c.level_id = 1 AND NOT EXISTS (SELECT 1 FROM category x WHERE x.name = 'blouses' AND x.parent_id = c.id);
-- bottoms › leggings, jumpsuits
INSERT INTO category (name, level_id, parent_id) SELECT 'leggings', 2, c.id FROM category c WHERE c.name = 'bottoms' AND c.level_id = 1 AND NOT EXISTS (SELECT 1 FROM category x WHERE x.name = 'leggings' AND x.parent_id = c.id);
INSERT INTO category (name, level_id, parent_id) SELECT 'jumpsuits', 2, c.id FROM category c WHERE c.name = 'bottoms' AND c.level_id = 1 AND NOT EXISTS (SELECT 1 FROM category x WHERE x.name = 'jumpsuits' AND x.parent_id = c.id);
-- loungewear_sleepwear › pyjamas, sets
INSERT INTO category (name, level_id, parent_id) SELECT 'pyjamas', 2, c.id FROM category c WHERE c.name = 'loungewear_sleepwear' AND c.level_id = 1 AND NOT EXISTS (SELECT 1 FROM category x WHERE x.name = 'pyjamas' AND x.parent_id = c.id);
INSERT INTO category (name, level_id, parent_id) SELECT 'sets', 2, c.id FROM category c WHERE c.name = 'loungewear_sleepwear' AND c.level_id = 1 AND NOT EXISTS (SELECT 1 FROM category x WHERE x.name = 'sets' AND x.parent_id = c.id);
-- accessories › eyewear, wallets, keychains, ties
INSERT INTO category (name, level_id, parent_id) SELECT 'eyewear', 2, c.id FROM category c WHERE c.name = 'accessories' AND c.level_id = 1 AND NOT EXISTS (SELECT 1 FROM category x WHERE x.name = 'eyewear' AND x.parent_id = c.id);
INSERT INTO category (name, level_id, parent_id) SELECT 'wallets', 2, c.id FROM category c WHERE c.name = 'accessories' AND c.level_id = 1 AND NOT EXISTS (SELECT 1 FROM category x WHERE x.name = 'wallets' AND x.parent_id = c.id);
INSERT INTO category (name, level_id, parent_id) SELECT 'keychains', 2, c.id FROM category c WHERE c.name = 'accessories' AND c.level_id = 1 AND NOT EXISTS (SELECT 1 FROM category x WHERE x.name = 'keychains' AND x.parent_id = c.id);
INSERT INTO category (name, level_id, parent_id) SELECT 'ties', 2, c.id FROM category c WHERE c.name = 'accessories' AND c.level_id = 1 AND NOT EXISTS (SELECT 1 FROM category x WHERE x.name = 'ties' AND x.parent_id = c.id);
-- shoes › mules_clogs, dress_shoes
INSERT INTO category (name, level_id, parent_id) SELECT 'mules_clogs', 2, c.id FROM category c WHERE c.name = 'shoes' AND c.level_id = 1 AND NOT EXISTS (SELECT 1 FROM category x WHERE x.name = 'mules_clogs' AND x.parent_id = c.id);
INSERT INTO category (name, level_id, parent_id) SELECT 'dress_shoes', 2, c.id FROM category c WHERE c.name = 'shoes' AND c.level_id = 1 AND NOT EXISTS (SELECT 1 FROM category x WHERE x.name = 'dress_shoes' AND x.parent_id = c.id);
-- bags › crossbody, belt_bags, clutches, duffle_bags, pouches
INSERT INTO category (name, level_id, parent_id) SELECT 'crossbody', 2, c.id FROM category c WHERE c.name = 'bags' AND c.level_id = 1 AND NOT EXISTS (SELECT 1 FROM category x WHERE x.name = 'crossbody' AND x.parent_id = c.id);
INSERT INTO category (name, level_id, parent_id) SELECT 'belt_bags', 2, c.id FROM category c WHERE c.name = 'bags' AND c.level_id = 1 AND NOT EXISTS (SELECT 1 FROM category x WHERE x.name = 'belt_bags' AND x.parent_id = c.id);
INSERT INTO category (name, level_id, parent_id) SELECT 'clutches', 2, c.id FROM category c WHERE c.name = 'bags' AND c.level_id = 1 AND NOT EXISTS (SELECT 1 FROM category x WHERE x.name = 'clutches' AND x.parent_id = c.id);
INSERT INTO category (name, level_id, parent_id) SELECT 'duffle_bags', 2, c.id FROM category c WHERE c.name = 'bags' AND c.level_id = 1 AND NOT EXISTS (SELECT 1 FROM category x WHERE x.name = 'duffle_bags' AND x.parent_id = c.id);
INSERT INTO category (name, level_id, parent_id) SELECT 'pouches', 2, c.id FROM category c WHERE c.name = 'bags' AND c.level_id = 1 AND NOT EXISTS (SELECT 1 FROM category x WHERE x.name = 'pouches' AND x.parent_id = c.id);

-- Types (level 3) under their sub-categories (level 2) — and under `dresses`, a top category (level 1).
-- tshirts › henley
INSERT INTO category (name, level_id, parent_id) SELECT 'henley', 3, c.id FROM category c WHERE c.name = 'tshirts' AND c.level_id = 2 AND NOT EXISTS (SELECT 1 FROM category x WHERE x.name = 'henley' AND x.parent_id = c.id);
-- shirts › flannel, denim, oxford
INSERT INTO category (name, level_id, parent_id) SELECT 'flannel', 3, c.id FROM category c WHERE c.name = 'shirts' AND c.level_id = 2 AND NOT EXISTS (SELECT 1 FROM category x WHERE x.name = 'flannel' AND x.parent_id = c.id);
INSERT INTO category (name, level_id, parent_id) SELECT 'denim', 3, c.id FROM category c WHERE c.name = 'shirts' AND c.level_id = 2 AND NOT EXISTS (SELECT 1 FROM category x WHERE x.name = 'denim' AND x.parent_id = c.id);
INSERT INTO category (name, level_id, parent_id) SELECT 'oxford', 3, c.id FROM category c WHERE c.name = 'shirts' AND c.level_id = 2 AND NOT EXISTS (SELECT 1 FROM category x WHERE x.name = 'oxford' AND x.parent_id = c.id);
-- sweaters_knits › sweater_vests
INSERT INTO category (name, level_id, parent_id) SELECT 'sweater_vests', 3, c.id FROM category c WHERE c.name = 'sweaters_knits' AND c.level_id = 2 AND NOT EXISTS (SELECT 1 FROM category x WHERE x.name = 'sweater_vests' AND x.parent_id = c.id);
-- hoodies_sweatshirts › half_zip
INSERT INTO category (name, level_id, parent_id) SELECT 'half_zip', 3, c.id FROM category c WHERE c.name = 'hoodies_sweatshirts' AND c.level_id = 2 AND NOT EXISTS (SELECT 1 FROM category x WHERE x.name = 'half_zip' AND x.parent_id = c.id);
-- pants › sweatpants, wide_leg, pleated, track
INSERT INTO category (name, level_id, parent_id) SELECT 'sweatpants', 3, c.id FROM category c WHERE c.name = 'pants' AND c.level_id = 2 AND NOT EXISTS (SELECT 1 FROM category x WHERE x.name = 'sweatpants' AND x.parent_id = c.id);
INSERT INTO category (name, level_id, parent_id) SELECT 'wide_leg', 3, c.id FROM category c WHERE c.name = 'pants' AND c.level_id = 2 AND NOT EXISTS (SELECT 1 FROM category x WHERE x.name = 'wide_leg' AND x.parent_id = c.id);
INSERT INTO category (name, level_id, parent_id) SELECT 'pleated', 3, c.id FROM category c WHERE c.name = 'pants' AND c.level_id = 2 AND NOT EXISTS (SELECT 1 FROM category x WHERE x.name = 'pleated' AND x.parent_id = c.id);
INSERT INTO category (name, level_id, parent_id) SELECT 'track', 3, c.id FROM category c WHERE c.name = 'pants' AND c.level_id = 2 AND NOT EXISTS (SELECT 1 FROM category x WHERE x.name = 'track' AND x.parent_id = c.id);
-- shorts › sweat, bermuda
INSERT INTO category (name, level_id, parent_id) SELECT 'sweat', 3, c.id FROM category c WHERE c.name = 'shorts' AND c.level_id = 2 AND NOT EXISTS (SELECT 1 FROM category x WHERE x.name = 'sweat' AND x.parent_id = c.id);
INSERT INTO category (name, level_id, parent_id) SELECT 'bermuda', 3, c.id FROM category c WHERE c.name = 'shorts' AND c.level_id = 2 AND NOT EXISTS (SELECT 1 FROM category x WHERE x.name = 'bermuda' AND x.parent_id = c.id);
-- jumpsuits › boiler_suit, overalls
INSERT INTO category (name, level_id, parent_id) SELECT 'boiler_suit', 3, c.id FROM category c WHERE c.name = 'jumpsuits' AND c.level_id = 2 AND NOT EXISTS (SELECT 1 FROM category x WHERE x.name = 'boiler_suit' AND x.parent_id = c.id);
INSERT INTO category (name, level_id, parent_id) SELECT 'overalls', 3, c.id FROM category c WHERE c.name = 'jumpsuits' AND c.level_id = 2 AND NOT EXISTS (SELECT 1 FROM category x WHERE x.name = 'overalls' AND x.parent_id = c.id);
-- dresses › midi, slip, knit, wrap
INSERT INTO category (name, level_id, parent_id) SELECT 'midi', 3, c.id FROM category c WHERE c.name = 'dresses' AND c.level_id = 1 AND NOT EXISTS (SELECT 1 FROM category x WHERE x.name = 'midi' AND x.parent_id = c.id);
INSERT INTO category (name, level_id, parent_id) SELECT 'slip', 3, c.id FROM category c WHERE c.name = 'dresses' AND c.level_id = 1 AND NOT EXISTS (SELECT 1 FROM category x WHERE x.name = 'slip' AND x.parent_id = c.id);
INSERT INTO category (name, level_id, parent_id) SELECT 'knit', 3, c.id FROM category c WHERE c.name = 'dresses' AND c.level_id = 1 AND NOT EXISTS (SELECT 1 FROM category x WHERE x.name = 'knit' AND x.parent_id = c.id);
INSERT INTO category (name, level_id, parent_id) SELECT 'wrap', 3, c.id FROM category c WHERE c.name = 'dresses' AND c.level_id = 1 AND NOT EXISTS (SELECT 1 FROM category x WHERE x.name = 'wrap' AND x.parent_id = c.id);
-- jackets › denim, track, windbreaker, shearling, fleece, varsity
INSERT INTO category (name, level_id, parent_id) SELECT 'denim', 3, c.id FROM category c WHERE c.name = 'jackets' AND c.level_id = 2 AND NOT EXISTS (SELECT 1 FROM category x WHERE x.name = 'denim' AND x.parent_id = c.id);
INSERT INTO category (name, level_id, parent_id) SELECT 'track', 3, c.id FROM category c WHERE c.name = 'jackets' AND c.level_id = 2 AND NOT EXISTS (SELECT 1 FROM category x WHERE x.name = 'track' AND x.parent_id = c.id);
INSERT INTO category (name, level_id, parent_id) SELECT 'windbreaker', 3, c.id FROM category c WHERE c.name = 'jackets' AND c.level_id = 2 AND NOT EXISTS (SELECT 1 FROM category x WHERE x.name = 'windbreaker' AND x.parent_id = c.id);
INSERT INTO category (name, level_id, parent_id) SELECT 'shearling', 3, c.id FROM category c WHERE c.name = 'jackets' AND c.level_id = 2 AND NOT EXISTS (SELECT 1 FROM category x WHERE x.name = 'shearling' AND x.parent_id = c.id);
INSERT INTO category (name, level_id, parent_id) SELECT 'fleece', 3, c.id FROM category c WHERE c.name = 'jackets' AND c.level_id = 2 AND NOT EXISTS (SELECT 1 FROM category x WHERE x.name = 'fleece' AND x.parent_id = c.id);
INSERT INTO category (name, level_id, parent_id) SELECT 'varsity', 3, c.id FROM category c WHERE c.name = 'jackets' AND c.level_id = 2 AND NOT EXISTS (SELECT 1 FROM category x WHERE x.name = 'varsity' AND x.parent_id = c.id);
-- coats › overcoat, wool, car_coat, shearling
INSERT INTO category (name, level_id, parent_id) SELECT 'overcoat', 3, c.id FROM category c WHERE c.name = 'coats' AND c.level_id = 2 AND NOT EXISTS (SELECT 1 FROM category x WHERE x.name = 'overcoat' AND x.parent_id = c.id);
INSERT INTO category (name, level_id, parent_id) SELECT 'wool', 3, c.id FROM category c WHERE c.name = 'coats' AND c.level_id = 2 AND NOT EXISTS (SELECT 1 FROM category x WHERE x.name = 'wool' AND x.parent_id = c.id);
INSERT INTO category (name, level_id, parent_id) SELECT 'car_coat', 3, c.id FROM category c WHERE c.name = 'coats' AND c.level_id = 2 AND NOT EXISTS (SELECT 1 FROM category x WHERE x.name = 'car_coat' AND x.parent_id = c.id);
INSERT INTO category (name, level_id, parent_id) SELECT 'shearling', 3, c.id FROM category c WHERE c.name = 'coats' AND c.level_id = 2 AND NOT EXISTS (SELECT 1 FROM category x WHERE x.name = 'shearling' AND x.parent_id = c.id);
-- eyewear › sunglasses, optical
INSERT INTO category (name, level_id, parent_id) SELECT 'sunglasses', 3, c.id FROM category c WHERE c.name = 'eyewear' AND c.level_id = 2 AND NOT EXISTS (SELECT 1 FROM category x WHERE x.name = 'sunglasses' AND x.parent_id = c.id);
INSERT INTO category (name, level_id, parent_id) SELECT 'optical', 3, c.id FROM category c WHERE c.name = 'eyewear' AND c.level_id = 2 AND NOT EXISTS (SELECT 1 FROM category x WHERE x.name = 'optical' AND x.parent_id = c.id);
-- wallets › cardholder, bifold
INSERT INTO category (name, level_id, parent_id) SELECT 'cardholder', 3, c.id FROM category c WHERE c.name = 'wallets' AND c.level_id = 2 AND NOT EXISTS (SELECT 1 FROM category x WHERE x.name = 'cardholder' AND x.parent_id = c.id);
INSERT INTO category (name, level_id, parent_id) SELECT 'bifold', 3, c.id FROM category c WHERE c.name = 'wallets' AND c.level_id = 2 AND NOT EXISTS (SELECT 1 FROM category x WHERE x.name = 'bifold' AND x.parent_id = c.id);
-- dress_shoes › derby, oxford, monk
INSERT INTO category (name, level_id, parent_id) SELECT 'derby', 3, c.id FROM category c WHERE c.name = 'dress_shoes' AND c.level_id = 2 AND NOT EXISTS (SELECT 1 FROM category x WHERE x.name = 'derby' AND x.parent_id = c.id);
INSERT INTO category (name, level_id, parent_id) SELECT 'oxford', 3, c.id FROM category c WHERE c.name = 'dress_shoes' AND c.level_id = 2 AND NOT EXISTS (SELECT 1 FROM category x WHERE x.name = 'oxford' AND x.parent_id = c.id);
INSERT INTO category (name, level_id, parent_id) SELECT 'monk', 3, c.id FROM category c WHERE c.name = 'dress_shoes' AND c.level_id = 2 AND NOT EXISTS (SELECT 1 FROM category x WHERE x.name = 'monk' AND x.parent_id = c.id);

-- +migrate Down

-- Types first, then the sub-categories (whose own new types are gone by then).
DELETE t FROM category t JOIN category p ON p.id = t.parent_id WHERE t.name = 'henley' AND t.level_id = 3 AND p.name = 'tshirts' AND p.level_id = 2;
DELETE t FROM category t JOIN category p ON p.id = t.parent_id WHERE t.name = 'flannel' AND t.level_id = 3 AND p.name = 'shirts' AND p.level_id = 2;
DELETE t FROM category t JOIN category p ON p.id = t.parent_id WHERE t.name = 'denim' AND t.level_id = 3 AND p.name = 'shirts' AND p.level_id = 2;
DELETE t FROM category t JOIN category p ON p.id = t.parent_id WHERE t.name = 'oxford' AND t.level_id = 3 AND p.name = 'shirts' AND p.level_id = 2;
DELETE t FROM category t JOIN category p ON p.id = t.parent_id WHERE t.name = 'sweater_vests' AND t.level_id = 3 AND p.name = 'sweaters_knits' AND p.level_id = 2;
DELETE t FROM category t JOIN category p ON p.id = t.parent_id WHERE t.name = 'half_zip' AND t.level_id = 3 AND p.name = 'hoodies_sweatshirts' AND p.level_id = 2;
DELETE t FROM category t JOIN category p ON p.id = t.parent_id WHERE t.name = 'sweatpants' AND t.level_id = 3 AND p.name = 'pants' AND p.level_id = 2;
DELETE t FROM category t JOIN category p ON p.id = t.parent_id WHERE t.name = 'wide_leg' AND t.level_id = 3 AND p.name = 'pants' AND p.level_id = 2;
DELETE t FROM category t JOIN category p ON p.id = t.parent_id WHERE t.name = 'pleated' AND t.level_id = 3 AND p.name = 'pants' AND p.level_id = 2;
DELETE t FROM category t JOIN category p ON p.id = t.parent_id WHERE t.name = 'track' AND t.level_id = 3 AND p.name = 'pants' AND p.level_id = 2;
DELETE t FROM category t JOIN category p ON p.id = t.parent_id WHERE t.name = 'sweat' AND t.level_id = 3 AND p.name = 'shorts' AND p.level_id = 2;
DELETE t FROM category t JOIN category p ON p.id = t.parent_id WHERE t.name = 'bermuda' AND t.level_id = 3 AND p.name = 'shorts' AND p.level_id = 2;
DELETE t FROM category t JOIN category p ON p.id = t.parent_id WHERE t.name = 'boiler_suit' AND t.level_id = 3 AND p.name = 'jumpsuits' AND p.level_id = 2;
DELETE t FROM category t JOIN category p ON p.id = t.parent_id WHERE t.name = 'overalls' AND t.level_id = 3 AND p.name = 'jumpsuits' AND p.level_id = 2;
DELETE t FROM category t JOIN category p ON p.id = t.parent_id WHERE t.name = 'midi' AND t.level_id = 3 AND p.name = 'dresses' AND p.level_id = 1;
DELETE t FROM category t JOIN category p ON p.id = t.parent_id WHERE t.name = 'slip' AND t.level_id = 3 AND p.name = 'dresses' AND p.level_id = 1;
DELETE t FROM category t JOIN category p ON p.id = t.parent_id WHERE t.name = 'knit' AND t.level_id = 3 AND p.name = 'dresses' AND p.level_id = 1;
DELETE t FROM category t JOIN category p ON p.id = t.parent_id WHERE t.name = 'wrap' AND t.level_id = 3 AND p.name = 'dresses' AND p.level_id = 1;
DELETE t FROM category t JOIN category p ON p.id = t.parent_id WHERE t.name = 'denim' AND t.level_id = 3 AND p.name = 'jackets' AND p.level_id = 2;
DELETE t FROM category t JOIN category p ON p.id = t.parent_id WHERE t.name = 'track' AND t.level_id = 3 AND p.name = 'jackets' AND p.level_id = 2;
DELETE t FROM category t JOIN category p ON p.id = t.parent_id WHERE t.name = 'windbreaker' AND t.level_id = 3 AND p.name = 'jackets' AND p.level_id = 2;
DELETE t FROM category t JOIN category p ON p.id = t.parent_id WHERE t.name = 'shearling' AND t.level_id = 3 AND p.name = 'jackets' AND p.level_id = 2;
DELETE t FROM category t JOIN category p ON p.id = t.parent_id WHERE t.name = 'fleece' AND t.level_id = 3 AND p.name = 'jackets' AND p.level_id = 2;
DELETE t FROM category t JOIN category p ON p.id = t.parent_id WHERE t.name = 'varsity' AND t.level_id = 3 AND p.name = 'jackets' AND p.level_id = 2;
DELETE t FROM category t JOIN category p ON p.id = t.parent_id WHERE t.name = 'overcoat' AND t.level_id = 3 AND p.name = 'coats' AND p.level_id = 2;
DELETE t FROM category t JOIN category p ON p.id = t.parent_id WHERE t.name = 'wool' AND t.level_id = 3 AND p.name = 'coats' AND p.level_id = 2;
DELETE t FROM category t JOIN category p ON p.id = t.parent_id WHERE t.name = 'car_coat' AND t.level_id = 3 AND p.name = 'coats' AND p.level_id = 2;
DELETE t FROM category t JOIN category p ON p.id = t.parent_id WHERE t.name = 'shearling' AND t.level_id = 3 AND p.name = 'coats' AND p.level_id = 2;
DELETE t FROM category t JOIN category p ON p.id = t.parent_id WHERE t.name = 'sunglasses' AND t.level_id = 3 AND p.name = 'eyewear' AND p.level_id = 2;
DELETE t FROM category t JOIN category p ON p.id = t.parent_id WHERE t.name = 'optical' AND t.level_id = 3 AND p.name = 'eyewear' AND p.level_id = 2;
DELETE t FROM category t JOIN category p ON p.id = t.parent_id WHERE t.name = 'cardholder' AND t.level_id = 3 AND p.name = 'wallets' AND p.level_id = 2;
DELETE t FROM category t JOIN category p ON p.id = t.parent_id WHERE t.name = 'bifold' AND t.level_id = 3 AND p.name = 'wallets' AND p.level_id = 2;
DELETE t FROM category t JOIN category p ON p.id = t.parent_id WHERE t.name = 'derby' AND t.level_id = 3 AND p.name = 'dress_shoes' AND p.level_id = 2;
DELETE t FROM category t JOIN category p ON p.id = t.parent_id WHERE t.name = 'oxford' AND t.level_id = 3 AND p.name = 'dress_shoes' AND p.level_id = 2;
DELETE t FROM category t JOIN category p ON p.id = t.parent_id WHERE t.name = 'monk' AND t.level_id = 3 AND p.name = 'dress_shoes' AND p.level_id = 2;
DELETE t FROM category t JOIN category p ON p.id = t.parent_id WHERE t.name = 'polos' AND t.level_id = 2 AND p.name = 'tops' AND p.level_id = 1;
DELETE t FROM category t JOIN category p ON p.id = t.parent_id WHERE t.name = 'bodysuits' AND t.level_id = 2 AND p.name = 'tops' AND p.level_id = 1;
DELETE t FROM category t JOIN category p ON p.id = t.parent_id WHERE t.name = 'blouses' AND t.level_id = 2 AND p.name = 'tops' AND p.level_id = 1;
DELETE t FROM category t JOIN category p ON p.id = t.parent_id WHERE t.name = 'leggings' AND t.level_id = 2 AND p.name = 'bottoms' AND p.level_id = 1;
DELETE t FROM category t JOIN category p ON p.id = t.parent_id WHERE t.name = 'jumpsuits' AND t.level_id = 2 AND p.name = 'bottoms' AND p.level_id = 1;
DELETE t FROM category t JOIN category p ON p.id = t.parent_id WHERE t.name = 'pyjamas' AND t.level_id = 2 AND p.name = 'loungewear_sleepwear' AND p.level_id = 1;
DELETE t FROM category t JOIN category p ON p.id = t.parent_id WHERE t.name = 'sets' AND t.level_id = 2 AND p.name = 'loungewear_sleepwear' AND p.level_id = 1;
DELETE t FROM category t JOIN category p ON p.id = t.parent_id WHERE t.name = 'eyewear' AND t.level_id = 2 AND p.name = 'accessories' AND p.level_id = 1;
DELETE t FROM category t JOIN category p ON p.id = t.parent_id WHERE t.name = 'wallets' AND t.level_id = 2 AND p.name = 'accessories' AND p.level_id = 1;
DELETE t FROM category t JOIN category p ON p.id = t.parent_id WHERE t.name = 'keychains' AND t.level_id = 2 AND p.name = 'accessories' AND p.level_id = 1;
DELETE t FROM category t JOIN category p ON p.id = t.parent_id WHERE t.name = 'ties' AND t.level_id = 2 AND p.name = 'accessories' AND p.level_id = 1;
DELETE t FROM category t JOIN category p ON p.id = t.parent_id WHERE t.name = 'mules_clogs' AND t.level_id = 2 AND p.name = 'shoes' AND p.level_id = 1;
DELETE t FROM category t JOIN category p ON p.id = t.parent_id WHERE t.name = 'dress_shoes' AND t.level_id = 2 AND p.name = 'shoes' AND p.level_id = 1;
DELETE t FROM category t JOIN category p ON p.id = t.parent_id WHERE t.name = 'crossbody' AND t.level_id = 2 AND p.name = 'bags' AND p.level_id = 1;
DELETE t FROM category t JOIN category p ON p.id = t.parent_id WHERE t.name = 'belt_bags' AND t.level_id = 2 AND p.name = 'bags' AND p.level_id = 1;
DELETE t FROM category t JOIN category p ON p.id = t.parent_id WHERE t.name = 'clutches' AND t.level_id = 2 AND p.name = 'bags' AND p.level_id = 1;
DELETE t FROM category t JOIN category p ON p.id = t.parent_id WHERE t.name = 'duffle_bags' AND t.level_id = 2 AND p.name = 'bags' AND p.level_id = 1;
DELETE t FROM category t JOIN category p ON p.id = t.parent_id WHERE t.name = 'pouches' AND t.level_id = 2 AND p.name = 'bags' AND p.level_id = 1;
