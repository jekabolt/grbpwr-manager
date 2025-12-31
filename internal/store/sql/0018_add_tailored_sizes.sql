-- +migrate Up
-- Migration: Add tailored sizes for men and women
-- Description: Adds tailored garment sizes with specific measurements for both genders
-- Affected tables: size
-- Date: 2025-12-31

-- Tailored sizes for women (XXS to XXL)
-- Format: size_measurement_ta_f (e.g., xxs_32ta_f means XXS, 32 measurement, tailored, female)
INSERT INTO
    size (name)
VALUES
    ('xxs_32ta_f'),
    ('xs_34ta_f'),
    ('s_36ta_f'),
    ('m_38ta_f'),
    ('l_40ta_f'),
    ('xl_42ta_f'),
    ('xxl_44ta_f');

-- Tailored sizes for men (XS to XXL)
-- Format: size_measurement_ta_m (e.g., xs_44ta_m means XS, 44 measurement, tailored, male)
INSERT INTO
    size (name)
VALUES
    ('xs_44ta_m'),
    ('s_46ta_m'),
    ('m_48ta_m'),
    ('l_50ta_m'),
    ('xl_52ta_m'),
    ('xxl_54ta_m');

-- +migrate Down
-- Remove tailored sizes for women
DELETE FROM size WHERE name IN (
    'xxs_32ta_f',
    'xs_34ta_f',
    's_36ta_f',
    'm_38ta_f',
    'l_40ta_f',
    'xl_42ta_f',
    'xxl_44ta_f'
);

-- Remove tailored sizes for men
DELETE FROM size WHERE name IN (
    'xs_44ta_m',
    's_46ta_m',
    'm_48ta_m',
    'l_50ta_m',
    'xl_52ta_m',
    'xxl_54ta_m'
);

