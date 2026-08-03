-- +migrate Up

-- production-costing Phase 2 dead-schema drop (plan 13): the POM (points-of-measure) trio was
-- superseded by tech_card_size_measurement (the live table the size-spec UI writes) — the sign-off
-- section 'pom' was already dropped back in 0079, and no Go code references any of the three.
-- Verified by grep before this drop. _actual and _grade FK _point, so children drop first.
DROP TABLE IF EXISTS tech_card_pom_actual;
DROP TABLE IF EXISTS tech_card_pom_grade;
DROP TABLE IF EXISTS tech_card_pom_point;

-- +migrate Down

SELECT 1;
