-- +migrate Up
-- The care vocabulary, server-side.
--
-- Until now a style's care round-tripped as ONE opaque string ("MW30,DNB,DNTD,IL"): the backend
-- could store it and hand it back, but had no idea what any of it meant. The taxonomy lived only in
-- the admin client's TypeScript, in three separate copies (symbol artwork, picker names, storefront
-- prose + print order), so the storefront had nothing to resolve the codes with and no read path
-- could be symbol-accurate.
--
-- This is the shape the composition model already uses (0167 / 0177): a controlled dictionary
-- (fiber -> care_symbol) that a typed wire projection resolves against, sitting ALONGSIDE the
-- legacy free-text column rather than replacing it. tech_card.care_instructions keeps holding the
-- canonical comma-joined code string -- it is what prints on the sewn tag and what the label
-- generator already consumes -- and a client renders the resolved entries when present, falling
-- back to the raw string for rows that still hold pre-ISO free text (the beta seed has some:
-- "Machine wash cold at 30, do not tumble dry").
--
-- Translations follow the house side-table pattern (category_translation, 0002) rather than a JSON
-- column: one row per (code, language), FK to language, so a language that is dropped takes its
-- wording with it. Only short_prose is translated -- that is the customer-facing string. name is
-- the admin picker's long label and the admin is English-only, so the translation's name column is
-- NULLABLE and the resolver falls back to the base English name.
--
-- Idempotent: CREATE TABLE IF NOT EXISTS plus INSERT IGNORE on the natural keys. The translation
-- seed joins language by code instead of hardcoding ids, and joins care_symbol so a partially
-- applied run cannot leave an orphan.

CREATE TABLE IF NOT EXISTS care_symbol (
    code VARCHAR(8) PRIMARY KEY,
    category VARCHAR(32) NOT NULL,
    sub_category VARCHAR(32) NULL COMMENT 'only Professional Care nests (dry / wet)',
    name VARCHAR(96) NOT NULL COMMENT 'admin picker label',
    short_prose VARCHAR(96) NOT NULL COMMENT 'storefront wording',
    sort_order INT NOT NULL COMMENT 'canonical order: wash, bleach, dry, iron, professional',
    archived_at TIMESTAMP NULL,
    UNIQUE KEY uniq_care_symbol_sort (sort_order)
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4 COMMENT 'Controlled ISO 3758 care vocabulary';

CREATE TABLE IF NOT EXISTS care_symbol_translation (
    id INT PRIMARY KEY AUTO_INCREMENT,
    care_code VARCHAR(8) NOT NULL,
    language_id INT NOT NULL,
    name VARCHAR(96) NULL COMMENT 'NULL falls back to care_symbol.name',
    short_prose VARCHAR(96) NOT NULL,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP NOT NULL,
    updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP NOT NULL,
    UNIQUE KEY uniq_care_language (care_code, language_id),
    FOREIGN KEY (care_code) REFERENCES care_symbol(code) ON DELETE CASCADE,
    FOREIGN KEY (language_id) REFERENCES language(id) ON DELETE CASCADE
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4 COMMENT 'Customer-facing care wording per language';

INSERT IGNORE INTO care_symbol (code, category, sub_category, name, short_prose, sort_order) VALUES
    ('MWN', 'Washing', NULL, 'Machine Wash Normal', 'machine wash', 1),
    ('MW30', 'Washing', NULL, 'Machine Wash Cold (30°C)', 'machine wash 30°', 2),
    ('MW40', 'Washing', NULL, 'Machine Wash Warm (40°C)', 'machine wash 40°', 3),
    ('MW50', 'Washing', NULL, 'Machine Wash Hot (50°C)', 'machine wash 50°', 4),
    ('MW60', 'Washing', NULL, 'Machine Wash Very Hot (60°C)', 'machine wash 60°', 5),
    ('GW', 'Washing', NULL, 'Gentle Wash', 'gentle wash', 6),
    ('VGW', 'Washing', NULL, 'Very Gentle Wash', 'very gentle wash', 7),
    ('HW', 'Washing', NULL, 'Hand Wash Only', 'hand wash only', 8),
    ('DNW', 'Washing', NULL, 'Do Not Wash', 'do not wash', 9),
    ('BA', 'Bleaching', NULL, 'Bleach Allowed', 'bleach allowed', 10),
    ('NCB', 'Bleaching', NULL, 'Non-Chlorine Bleach Only', 'non-chlorine bleach only', 11),
    ('DNB', 'Bleaching', NULL, 'Do Not Bleach', 'do not bleach', 12),
    ('TDN', 'Drying', NULL, 'Tumble Dry Normal', 'tumble dry normal', 13),
    ('TDL', 'Drying', NULL, 'Tumble Dry Low Heat', 'tumble dry low', 14),
    ('TDM', 'Drying', NULL, 'Tumble Dry Medium Heat', 'tumble dry medium', 15),
    ('TDH', 'Drying', NULL, 'Tumble Dry High Heat', 'tumble dry high', 16),
    ('DNTD', 'Drying', NULL, 'Do Not Tumble Dry', 'do not tumble dry', 17),
    ('LD', 'Drying', NULL, 'Line Dry', 'line dry', 18),
    ('DF', 'Drying', NULL, 'Dry Flat', 'dry flat', 19),
    ('DD', 'Drying', NULL, 'Drip Dry', 'drip dry', 20),
    ('DIS', 'Drying', NULL, 'Dry in Shade', 'dry in shade', 21),
    ('LDS', 'Drying', NULL, 'Line Dry in Shade', 'line dry in shade', 22),
    ('DFS', 'Drying', NULL, 'Dry Flat in Shade', 'dry flat in shade', 23),
    ('DDS', 'Drying', NULL, 'Drip Dry in Shade', 'drip dry in shade', 24),
    ('IL', 'Ironing', NULL, 'Iron at Low Temperature (110°C)', 'iron low', 25),
    ('IM', 'Ironing', NULL, 'Iron at Medium Temperature (150°C)', 'iron medium', 26),
    ('IH', 'Ironing', NULL, 'Iron at High Temperature (200°C)', 'iron high', 27),
    ('DNS', 'Ironing', NULL, 'Do Not Steam', 'do not steam', 28),
    ('DNI', 'Ironing', NULL, 'Do Not Iron', 'do not iron', 29),
    ('DCAS', 'Professional Care', 'Dry Cleaning', 'Dry Clean with Any Solvent', 'dry clean, any solvent', 30),
    ('DCPS', 'Professional Care', 'Dry Cleaning', 'Dry Clean with Petroleum Solvent Only', 'dry clean, petroleum solvent only', 31),
    ('DCASE', 'Professional Care', 'Dry Cleaning', 'Dry Clean with Any Solvent Except Trichloroethylene', 'dry clean, any solvent except trichloroethylene', 32),
    ('GDC', 'Professional Care', 'Dry Cleaning', 'Gentle Dry Clean with Any Solvent Except Trichloroethylene', 'gentle dry clean', 33),
    ('VGDC', 'Professional Care', 'Dry Cleaning', 'Very Gentle Dry Clean with Any Solvent Except Trichloroethylene', 'very gentle dry clean', 34),
    ('DNDC', 'Professional Care', 'Dry Cleaning', 'Do Not Dry Clean', 'do not dry clean', 35),
    ('PWC', 'Professional Care', 'Wet Cleaning', 'Professional Wet Clean', 'professional wet clean', 36),
    ('GPWC', 'Professional Care', 'Wet Cleaning', 'Gentle Professional Wet Clean', 'gentle professional wet clean', 37),
    ('VGPWC', 'Professional Care', 'Wet Cleaning', 'Very Gentle Professional Wet Clean', 'very gentle professional wet clean', 38),
    ('DNWC', 'Professional Care', 'Wet Cleaning', 'Do Not Wet Clean', 'do not wet clean', 39);

INSERT IGNORE INTO care_symbol_translation (care_code, language_id, short_prose)
SELECT t.code, l.id, t.prose
  FROM (
            SELECT 'MWN' AS code, 'lavage en machine' AS prose
    UNION ALL SELECT 'MW30' AS code, 'lavage en machine 30°' AS prose
    UNION ALL SELECT 'MW40' AS code, 'lavage en machine 40°' AS prose
    UNION ALL SELECT 'MW50' AS code, 'lavage en machine 50°' AS prose
    UNION ALL SELECT 'MW60' AS code, 'lavage en machine 60°' AS prose
    UNION ALL SELECT 'GW' AS code, 'lavage délicat' AS prose
    UNION ALL SELECT 'VGW' AS code, 'lavage très délicat' AS prose
    UNION ALL SELECT 'HW' AS code, 'lavage à la main uniquement' AS prose
    UNION ALL SELECT 'DNW' AS code, 'ne pas laver' AS prose
    UNION ALL SELECT 'BA' AS code, 'javel autorisée' AS prose
    UNION ALL SELECT 'NCB' AS code, 'javel sans chlore uniquement' AS prose
    UNION ALL SELECT 'DNB' AS code, 'ne pas javelliser' AS prose
    UNION ALL SELECT 'TDN' AS code, 'séchage en tambour normal' AS prose
    UNION ALL SELECT 'TDL' AS code, 'séchage en tambour doux' AS prose
    UNION ALL SELECT 'TDM' AS code, 'séchage en tambour moyen' AS prose
    UNION ALL SELECT 'TDH' AS code, 'séchage en tambour fort' AS prose
    UNION ALL SELECT 'DNTD' AS code, 'ne pas sécher en tambour' AS prose
    UNION ALL SELECT 'LD' AS code, 'séchage sur fil' AS prose
    UNION ALL SELECT 'DF' AS code, 'séchage à plat' AS prose
    UNION ALL SELECT 'DD' AS code, 'séchage égoutté' AS prose
    UNION ALL SELECT 'DIS' AS code, 'séchage à l''ombre' AS prose
    UNION ALL SELECT 'LDS' AS code, 'séchage sur fil à l''ombre' AS prose
    UNION ALL SELECT 'DFS' AS code, 'séchage à plat à l''ombre' AS prose
    UNION ALL SELECT 'DDS' AS code, 'séchage égoutté à l''ombre' AS prose
    UNION ALL SELECT 'IL' AS code, 'repassage doux' AS prose
    UNION ALL SELECT 'IM' AS code, 'repassage moyen' AS prose
    UNION ALL SELECT 'IH' AS code, 'repassage fort' AS prose
    UNION ALL SELECT 'DNS' AS code, 'ne pas repasser à la vapeur' AS prose
    UNION ALL SELECT 'DNI' AS code, 'ne pas repasser' AS prose
    UNION ALL SELECT 'DCAS' AS code, 'nettoyage à sec, tout solvant' AS prose
    UNION ALL SELECT 'DCPS' AS code, 'nettoyage à sec, solvant pétrolier uniquement' AS prose
    UNION ALL SELECT 'DCASE' AS code, 'nettoyage à sec, tout solvant sauf trichloréthylène' AS prose
    UNION ALL SELECT 'GDC' AS code, 'nettoyage à sec doux' AS prose
    UNION ALL SELECT 'VGDC' AS code, 'nettoyage à sec très doux' AS prose
    UNION ALL SELECT 'DNDC' AS code, 'ne pas nettoyer à sec' AS prose
    UNION ALL SELECT 'PWC' AS code, 'nettoyage professionnel à l''eau' AS prose
    UNION ALL SELECT 'GPWC' AS code, 'nettoyage professionnel à l''eau doux' AS prose
    UNION ALL SELECT 'VGPWC' AS code, 'nettoyage professionnel à l''eau très doux' AS prose
    UNION ALL SELECT 'DNWC' AS code, 'ne pas nettoyer à l''eau' AS prose
  ) t
  JOIN language l ON l.code = 'fr'
  JOIN care_symbol c ON c.code = t.code;

INSERT IGNORE INTO care_symbol_translation (care_code, language_id, short_prose)
SELECT t.code, l.id, t.prose
  FROM (
            SELECT 'MWN' AS code, 'Maschinenwäsche' AS prose
    UNION ALL SELECT 'MW30' AS code, 'Maschinenwäsche 30°' AS prose
    UNION ALL SELECT 'MW40' AS code, 'Maschinenwäsche 40°' AS prose
    UNION ALL SELECT 'MW50' AS code, 'Maschinenwäsche 50°' AS prose
    UNION ALL SELECT 'MW60' AS code, 'Maschinenwäsche 60°' AS prose
    UNION ALL SELECT 'GW' AS code, 'Schonwaschgang' AS prose
    UNION ALL SELECT 'VGW' AS code, 'Feinwaschgang' AS prose
    UNION ALL SELECT 'HW' AS code, 'nur Handwäsche' AS prose
    UNION ALL SELECT 'DNW' AS code, 'nicht waschen' AS prose
    UNION ALL SELECT 'BA' AS code, 'Bleichen erlaubt' AS prose
    UNION ALL SELECT 'NCB' AS code, 'nur Sauerstoffbleiche' AS prose
    UNION ALL SELECT 'DNB' AS code, 'nicht bleichen' AS prose
    UNION ALL SELECT 'TDN' AS code, 'Trommeltrocknen normal' AS prose
    UNION ALL SELECT 'TDL' AS code, 'Trommeltrocknen niedrig' AS prose
    UNION ALL SELECT 'TDM' AS code, 'Trommeltrocknen mittel' AS prose
    UNION ALL SELECT 'TDH' AS code, 'Trommeltrocknen hoch' AS prose
    UNION ALL SELECT 'DNTD' AS code, 'nicht im Trommeltrockner trocknen' AS prose
    UNION ALL SELECT 'LD' AS code, 'auf der Leine trocknen' AS prose
    UNION ALL SELECT 'DF' AS code, 'liegend trocknen' AS prose
    UNION ALL SELECT 'DD' AS code, 'tropfnass trocknen' AS prose
    UNION ALL SELECT 'DIS' AS code, 'im Schatten trocknen' AS prose
    UNION ALL SELECT 'LDS' AS code, 'auf der Leine im Schatten trocknen' AS prose
    UNION ALL SELECT 'DFS' AS code, 'liegend im Schatten trocknen' AS prose
    UNION ALL SELECT 'DDS' AS code, 'tropfnass im Schatten trocknen' AS prose
    UNION ALL SELECT 'IL' AS code, 'bügeln niedrig' AS prose
    UNION ALL SELECT 'IM' AS code, 'bügeln mittel' AS prose
    UNION ALL SELECT 'IH' AS code, 'bügeln hoch' AS prose
    UNION ALL SELECT 'DNS' AS code, 'nicht dämpfen' AS prose
    UNION ALL SELECT 'DNI' AS code, 'nicht bügeln' AS prose
    UNION ALL SELECT 'DCAS' AS code, 'chemische Reinigung, alle Lösemittel' AS prose
    UNION ALL SELECT 'DCPS' AS code, 'chemische Reinigung, nur Kohlenwasserstoffe' AS prose
    UNION ALL SELECT 'DCASE' AS code, 'chemische Reinigung, alle Lösemittel außer Trichlorethylen' AS prose
    UNION ALL SELECT 'GDC' AS code, 'schonende chemische Reinigung' AS prose
    UNION ALL SELECT 'VGDC' AS code, 'sehr schonende chemische Reinigung' AS prose
    UNION ALL SELECT 'DNDC' AS code, 'nicht chemisch reinigen' AS prose
    UNION ALL SELECT 'PWC' AS code, 'professionelle Nassreinigung' AS prose
    UNION ALL SELECT 'GPWC' AS code, 'schonende Nassreinigung' AS prose
    UNION ALL SELECT 'VGPWC' AS code, 'sehr schonende Nassreinigung' AS prose
    UNION ALL SELECT 'DNWC' AS code, 'keine Nassreinigung' AS prose
  ) t
  JOIN language l ON l.code = 'de'
  JOIN care_symbol c ON c.code = t.code;

INSERT IGNORE INTO care_symbol_translation (care_code, language_id, short_prose)
SELECT t.code, l.id, t.prose
  FROM (
            SELECT 'MWN' AS code, 'lavaggio in lavatrice' AS prose
    UNION ALL SELECT 'MW30' AS code, 'lavaggio in lavatrice 30°' AS prose
    UNION ALL SELECT 'MW40' AS code, 'lavaggio in lavatrice 40°' AS prose
    UNION ALL SELECT 'MW50' AS code, 'lavaggio in lavatrice 50°' AS prose
    UNION ALL SELECT 'MW60' AS code, 'lavaggio in lavatrice 60°' AS prose
    UNION ALL SELECT 'GW' AS code, 'lavaggio delicato' AS prose
    UNION ALL SELECT 'VGW' AS code, 'lavaggio molto delicato' AS prose
    UNION ALL SELECT 'HW' AS code, 'solo lavaggio a mano' AS prose
    UNION ALL SELECT 'DNW' AS code, 'non lavare' AS prose
    UNION ALL SELECT 'BA' AS code, 'candeggio consentito' AS prose
    UNION ALL SELECT 'NCB' AS code, 'solo candeggio non al cloro' AS prose
    UNION ALL SELECT 'DNB' AS code, 'non candeggiare' AS prose
    UNION ALL SELECT 'TDN' AS code, 'asciugatura in tamburo normale' AS prose
    UNION ALL SELECT 'TDL' AS code, 'asciugatura in tamburo bassa' AS prose
    UNION ALL SELECT 'TDM' AS code, 'asciugatura in tamburo media' AS prose
    UNION ALL SELECT 'TDH' AS code, 'asciugatura in tamburo alta' AS prose
    UNION ALL SELECT 'DNTD' AS code, 'non asciugare in tamburo' AS prose
    UNION ALL SELECT 'LD' AS code, 'stendere ad asciugare' AS prose
    UNION ALL SELECT 'DF' AS code, 'asciugare in piano' AS prose
    UNION ALL SELECT 'DD' AS code, 'asciugare senza strizzare' AS prose
    UNION ALL SELECT 'DIS' AS code, 'asciugare all''ombra' AS prose
    UNION ALL SELECT 'LDS' AS code, 'stendere ad asciugare all''ombra' AS prose
    UNION ALL SELECT 'DFS' AS code, 'asciugare in piano all''ombra' AS prose
    UNION ALL SELECT 'DDS' AS code, 'asciugare senza strizzare all''ombra' AS prose
    UNION ALL SELECT 'IL' AS code, 'stirare a bassa temperatura' AS prose
    UNION ALL SELECT 'IM' AS code, 'stirare a media temperatura' AS prose
    UNION ALL SELECT 'IH' AS code, 'stirare ad alta temperatura' AS prose
    UNION ALL SELECT 'DNS' AS code, 'non stirare a vapore' AS prose
    UNION ALL SELECT 'DNI' AS code, 'non stirare' AS prose
    UNION ALL SELECT 'DCAS' AS code, 'lavaggio a secco, qualsiasi solvente' AS prose
    UNION ALL SELECT 'DCPS' AS code, 'lavaggio a secco, solo solvente petrolifero' AS prose
    UNION ALL SELECT 'DCASE' AS code, 'lavaggio a secco, ogni solvente tranne trielina' AS prose
    UNION ALL SELECT 'GDC' AS code, 'lavaggio a secco delicato' AS prose
    UNION ALL SELECT 'VGDC' AS code, 'lavaggio a secco molto delicato' AS prose
    UNION ALL SELECT 'DNDC' AS code, 'non lavare a secco' AS prose
    UNION ALL SELECT 'PWC' AS code, 'lavaggio professionale ad acqua' AS prose
    UNION ALL SELECT 'GPWC' AS code, 'lavaggio professionale ad acqua delicato' AS prose
    UNION ALL SELECT 'VGPWC' AS code, 'lavaggio professionale ad acqua molto delicato' AS prose
    UNION ALL SELECT 'DNWC' AS code, 'non lavare ad acqua' AS prose
  ) t
  JOIN language l ON l.code = 'it'
  JOIN care_symbol c ON c.code = t.code;

INSERT IGNORE INTO care_symbol_translation (care_code, language_id, short_prose)
SELECT t.code, l.id, t.prose
  FROM (
            SELECT 'MWN' AS code, '洗濯機洗い' AS prose
    UNION ALL SELECT 'MW30' AS code, '洗濯機洗い 30°' AS prose
    UNION ALL SELECT 'MW40' AS code, '洗濯機洗い 40°' AS prose
    UNION ALL SELECT 'MW50' AS code, '洗濯機洗い 50°' AS prose
    UNION ALL SELECT 'MW60' AS code, '洗濯機洗い 60°' AS prose
    UNION ALL SELECT 'GW' AS code, '弱水流で洗濯' AS prose
    UNION ALL SELECT 'VGW' AS code, '非常に弱い水流で洗濯' AS prose
    UNION ALL SELECT 'HW' AS code, '手洗いのみ' AS prose
    UNION ALL SELECT 'DNW' AS code, '洗濯不可' AS prose
    UNION ALL SELECT 'BA' AS code, '漂白可' AS prose
    UNION ALL SELECT 'NCB' AS code, '酸素系漂白剤のみ' AS prose
    UNION ALL SELECT 'DNB' AS code, '漂白不可' AS prose
    UNION ALL SELECT 'TDN' AS code, 'タンブル乾燥可' AS prose
    UNION ALL SELECT 'TDL' AS code, 'タンブル乾燥 低温' AS prose
    UNION ALL SELECT 'TDM' AS code, 'タンブル乾燥 中温' AS prose
    UNION ALL SELECT 'TDH' AS code, 'タンブル乾燥 高温' AS prose
    UNION ALL SELECT 'DNTD' AS code, 'タンブル乾燥不可' AS prose
    UNION ALL SELECT 'LD' AS code, 'つり干し' AS prose
    UNION ALL SELECT 'DF' AS code, '平干し' AS prose
    UNION ALL SELECT 'DD' AS code, 'ぬれつり干し' AS prose
    UNION ALL SELECT 'DIS' AS code, '陰干し' AS prose
    UNION ALL SELECT 'LDS' AS code, '陰干しでつり干し' AS prose
    UNION ALL SELECT 'DFS' AS code, '陰干しで平干し' AS prose
    UNION ALL SELECT 'DDS' AS code, '陰干しでぬれつり干し' AS prose
    UNION ALL SELECT 'IL' AS code, '低温でアイロン' AS prose
    UNION ALL SELECT 'IM' AS code, '中温でアイロン' AS prose
    UNION ALL SELECT 'IH' AS code, '高温でアイロン' AS prose
    UNION ALL SELECT 'DNS' AS code, 'スチーム不可' AS prose
    UNION ALL SELECT 'DNI' AS code, 'アイロン不可' AS prose
    UNION ALL SELECT 'DCAS' AS code, 'ドライクリーニング 溶剤不問' AS prose
    UNION ALL SELECT 'DCPS' AS code, 'ドライクリーニング 石油系溶剤のみ' AS prose
    UNION ALL SELECT 'DCASE' AS code, 'ドライクリーニング トリクロロエチレン以外' AS prose
    UNION ALL SELECT 'GDC' AS code, '弱いドライクリーニング' AS prose
    UNION ALL SELECT 'VGDC' AS code, '非常に弱いドライクリーニング' AS prose
    UNION ALL SELECT 'DNDC' AS code, 'ドライクリーニング不可' AS prose
    UNION ALL SELECT 'PWC' AS code, 'ウエットクリーニング' AS prose
    UNION ALL SELECT 'GPWC' AS code, '弱いウエットクリーニング' AS prose
    UNION ALL SELECT 'VGPWC' AS code, '非常に弱いウエットクリーニング' AS prose
    UNION ALL SELECT 'DNWC' AS code, 'ウエットクリーニング不可' AS prose
  ) t
  JOIN language l ON l.code = 'ja'
  JOIN care_symbol c ON c.code = t.code;

INSERT IGNORE INTO care_symbol_translation (care_code, language_id, short_prose)
SELECT t.code, l.id, t.prose
  FROM (
            SELECT 'MWN' AS code, '机洗' AS prose
    UNION ALL SELECT 'MW30' AS code, '机洗 30°' AS prose
    UNION ALL SELECT 'MW40' AS code, '机洗 40°' AS prose
    UNION ALL SELECT 'MW50' AS code, '机洗 50°' AS prose
    UNION ALL SELECT 'MW60' AS code, '机洗 60°' AS prose
    UNION ALL SELECT 'GW' AS code, '轻柔机洗' AS prose
    UNION ALL SELECT 'VGW' AS code, '特轻柔机洗' AS prose
    UNION ALL SELECT 'HW' AS code, '仅可手洗' AS prose
    UNION ALL SELECT 'DNW' AS code, '不可水洗' AS prose
    UNION ALL SELECT 'BA' AS code, '可漂白' AS prose
    UNION ALL SELECT 'NCB' AS code, '仅可氧漂' AS prose
    UNION ALL SELECT 'DNB' AS code, '不可漂白' AS prose
    UNION ALL SELECT 'TDN' AS code, '可翻转干燥' AS prose
    UNION ALL SELECT 'TDL' AS code, '低温翻转干燥' AS prose
    UNION ALL SELECT 'TDM' AS code, '中温翻转干燥' AS prose
    UNION ALL SELECT 'TDH' AS code, '高温翻转干燥' AS prose
    UNION ALL SELECT 'DNTD' AS code, '不可翻转干燥' AS prose
    UNION ALL SELECT 'LD' AS code, '悬挂晾干' AS prose
    UNION ALL SELECT 'DF' AS code, '平摊晾干' AS prose
    UNION ALL SELECT 'DD' AS code, '悬挂滴干' AS prose
    UNION ALL SELECT 'DIS' AS code, '阴凉处晾干' AS prose
    UNION ALL SELECT 'LDS' AS code, '阴凉处悬挂晾干' AS prose
    UNION ALL SELECT 'DFS' AS code, '阴凉处平摊晾干' AS prose
    UNION ALL SELECT 'DDS' AS code, '阴凉处悬挂滴干' AS prose
    UNION ALL SELECT 'IL' AS code, '低温熨烫' AS prose
    UNION ALL SELECT 'IM' AS code, '中温熨烫' AS prose
    UNION ALL SELECT 'IH' AS code, '高温熨烫' AS prose
    UNION ALL SELECT 'DNS' AS code, '不可蒸汽熨烫' AS prose
    UNION ALL SELECT 'DNI' AS code, '不可熨烫' AS prose
    UNION ALL SELECT 'DCAS' AS code, '干洗，任何溶剂' AS prose
    UNION ALL SELECT 'DCPS' AS code, '干洗，仅石油溶剂' AS prose
    UNION ALL SELECT 'DCASE' AS code, '干洗，除三氯乙烯外任何溶剂' AS prose
    UNION ALL SELECT 'GDC' AS code, '轻柔干洗' AS prose
    UNION ALL SELECT 'VGDC' AS code, '特轻柔干洗' AS prose
    UNION ALL SELECT 'DNDC' AS code, '不可干洗' AS prose
    UNION ALL SELECT 'PWC' AS code, '专业湿洗' AS prose
    UNION ALL SELECT 'GPWC' AS code, '轻柔专业湿洗' AS prose
    UNION ALL SELECT 'VGPWC' AS code, '特轻柔专业湿洗' AS prose
    UNION ALL SELECT 'DNWC' AS code, '不可湿洗' AS prose
  ) t
  JOIN language l ON l.code = 'cn'
  JOIN care_symbol c ON c.code = t.code;

INSERT IGNORE INTO care_symbol_translation (care_code, language_id, short_prose)
SELECT t.code, l.id, t.prose
  FROM (
            SELECT 'MWN' AS code, '기계 세탁' AS prose
    UNION ALL SELECT 'MW30' AS code, '기계 세탁 30°' AS prose
    UNION ALL SELECT 'MW40' AS code, '기계 세탁 40°' AS prose
    UNION ALL SELECT 'MW50' AS code, '기계 세탁 50°' AS prose
    UNION ALL SELECT 'MW60' AS code, '기계 세탁 60°' AS prose
    UNION ALL SELECT 'GW' AS code, '약하게 세탁' AS prose
    UNION ALL SELECT 'VGW' AS code, '매우 약하게 세탁' AS prose
    UNION ALL SELECT 'HW' AS code, '손세탁만 가능' AS prose
    UNION ALL SELECT 'DNW' AS code, '세탁 금지' AS prose
    UNION ALL SELECT 'BA' AS code, '표백 가능' AS prose
    UNION ALL SELECT 'NCB' AS code, '산소계 표백만 가능' AS prose
    UNION ALL SELECT 'DNB' AS code, '표백 금지' AS prose
    UNION ALL SELECT 'TDN' AS code, '건조기 사용 가능' AS prose
    UNION ALL SELECT 'TDL' AS code, '건조기 저온' AS prose
    UNION ALL SELECT 'TDM' AS code, '건조기 중온' AS prose
    UNION ALL SELECT 'TDH' AS code, '건조기 고온' AS prose
    UNION ALL SELECT 'DNTD' AS code, '건조기 사용 금지' AS prose
    UNION ALL SELECT 'LD' AS code, '옷걸이 건조' AS prose
    UNION ALL SELECT 'DF' AS code, '뉘어서 건조' AS prose
    UNION ALL SELECT 'DD' AS code, '물기 뺀 후 걸어 건조' AS prose
    UNION ALL SELECT 'DIS' AS code, '그늘에서 건조' AS prose
    UNION ALL SELECT 'LDS' AS code, '그늘에서 옷걸이 건조' AS prose
    UNION ALL SELECT 'DFS' AS code, '그늘에 뉘어서 건조' AS prose
    UNION ALL SELECT 'DDS' AS code, '그늘에서 물기 뺀 후 걸어 건조' AS prose
    UNION ALL SELECT 'IL' AS code, '저온 다림질' AS prose
    UNION ALL SELECT 'IM' AS code, '중온 다림질' AS prose
    UNION ALL SELECT 'IH' AS code, '고온 다림질' AS prose
    UNION ALL SELECT 'DNS' AS code, '스팀 금지' AS prose
    UNION ALL SELECT 'DNI' AS code, '다림질 금지' AS prose
    UNION ALL SELECT 'DCAS' AS code, '드라이클리닝, 모든 용제' AS prose
    UNION ALL SELECT 'DCPS' AS code, '드라이클리닝, 석유계 용제만' AS prose
    UNION ALL SELECT 'DCASE' AS code, '드라이클리닝, 트리클로로에틸렌 제외' AS prose
    UNION ALL SELECT 'GDC' AS code, '약한 드라이클리닝' AS prose
    UNION ALL SELECT 'VGDC' AS code, '매우 약한 드라이클리닝' AS prose
    UNION ALL SELECT 'DNDC' AS code, '드라이클리닝 금지' AS prose
    UNION ALL SELECT 'PWC' AS code, '전문 웨트클리닝' AS prose
    UNION ALL SELECT 'GPWC' AS code, '약한 웨트클리닝' AS prose
    UNION ALL SELECT 'VGPWC' AS code, '매우 약한 웨트클리닝' AS prose
    UNION ALL SELECT 'DNWC' AS code, '웨트클리닝 금지' AS prose
  ) t
  JOIN language l ON l.code = 'kr'
  JOIN care_symbol c ON c.code = t.code;

-- +migrate Down
DROP TABLE IF EXISTS care_symbol_translation;
DROP TABLE IF EXISTS care_symbol;
