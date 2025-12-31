-- +migrate Up
-- Migration: Add waist sizes for pants, shorts, and skirts
-- Description: Adds waist measurement sizes for bottoms (pants, shorts, skirts) for both genders
-- Affected tables: size
-- Date: 2025-12-31

-- Women's bottoms sizes (pants, shorts, skirts) - waist measurements
-- Format: size_waist_bo_f (e.g., xxs_23bo_f means XXS, 23" waist, bottoms, female)
INSERT INTO
    size (name)
VALUES
    ('xxs_23bo_f'),
    ('xs_25bo_f'),
    ('s_27bo_f'),
    ('m_29bo_f'),
    ('l_31bo_f'),
    ('xl_33bo_f');

-- Men's bottoms sizes (pants, shorts) - waist measurements
-- Format: size_waist_bo_m (e.g., xxs_26bo_m means XXS, 26" waist, bottoms, male)
INSERT INTO
    size (name)
VALUES
    ('xxs_26bo_m'),
    ('xs_28bo_m'),
    ('s_30bo_m'),
    ('m_32bo_m'),
    ('l_34bo_m'),
    ('xl_36bo_m'),
    ('xxl_38bo_m');

-- +migrate Down
-- Remove women's bottoms sizes
DELETE FROM size WHERE name IN (
    'xxs_23bo_f',
    'xs_25bo_f',
    's_27bo_f',
    'm_29bo_f',
    'l_31bo_f',
    'xl_33bo_f'
);

-- Remove men's bottoms sizes
DELETE FROM size WHERE name IN (
    'xxs_26bo_m',
    'xs_28bo_m',
    's_30bo_m',
    'm_32bo_m',
    'l_34bo_m',
    'xl_36bo_m',
    'xxl_38bo_m'
);

