-- Prices belong to the first tenant, not to the carrier dictionary.
DELETE FROM shipment_carrier_price;
DELETE FROM complimentary_shipping_price;

-- The seeded file taxonomy mirrors grbpwr's own vault and workflow.
DELETE FROM file_role;
DELETE FROM file_topic;

-- Keep the fixed loyalty keys, but remove grbpwr from their labels.
UPDATE tier_config
SET display_name = CASE tier_key
    WHEN 'member' THEN 'Member'
    WHEN 'plus' THEN 'Plus'
    WHEN 'plus_plus' THEN 'Plus Plus'
    WHEN 'hacker' THEN 'Hacker'
    ELSE display_name
END
WHERE tier_key IN ('member', 'plus', 'plus_plus', 'hacker');

-- A new team's day boundary must not inherit grbpwr's timezone.
UPDATE design_settings
SET budget_timezone = 'UTC', updated_by = ''
WHERE id = 1;
