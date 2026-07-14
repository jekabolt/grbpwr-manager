-- +migrate Up

-- new-flow NF-03: add an `idea` stage before `proto` so a style can be captured as a draft
-- (moodboard, callouts, concept, category) before it has a style number, and make style_number
-- nullable so those drafts can be saved. UNIQUE(style_number, season) still holds — MySQL allows
-- multiple NULLs in a unique key, so many idea drafts can coexist in one season.
--
-- The stage CHECK is auto-named (tech_card_chk_N) and must not be dropped by positional name, so it
-- is realigned dynamically via information_schema (idempotent — a mid-file failure re-runs cleanly).

-- style_number nullable (MODIFY is idempotent).
ALTER TABLE tech_card MODIFY style_number VARCHAR(255) NULL;

-- widen the stage CHECK to include `idea`.
SET @cname := (
    SELECT tc.CONSTRAINT_NAME
    FROM information_schema.TABLE_CONSTRAINTS tc
    JOIN information_schema.CHECK_CONSTRAINTS cc
        ON cc.CONSTRAINT_SCHEMA = tc.CONSTRAINT_SCHEMA AND cc.CONSTRAINT_NAME = tc.CONSTRAINT_NAME
    WHERE tc.TABLE_SCHEMA = DATABASE() AND tc.TABLE_NAME = 'tech_card'
        AND tc.CONSTRAINT_TYPE = 'CHECK'
        AND cc.CHECK_CLAUSE LIKE '%stage%'
        AND tc.CONSTRAINT_NAME <> 'chk_tech_card_stage'
    LIMIT 1);
SET @sql := IF(@cname IS NULL, 'SELECT 1', CONCAT('ALTER TABLE tech_card DROP CHECK ', @cname));
PREPARE s FROM @sql; EXECUTE s; DEALLOCATE PREPARE s;

SET @have := (SELECT COUNT(*) FROM information_schema.TABLE_CONSTRAINTS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card' AND CONSTRAINT_NAME = 'chk_tech_card_stage');
SET @sql := IF(@have > 0, 'SELECT 1',
    'ALTER TABLE tech_card ADD CONSTRAINT chk_tech_card_stage CHECK (stage REGEXP ''^(idea|proto|fit|sms|pp|prod)$'')');
PREPARE s FROM @sql; EXECUTE s; DEALLOCATE PREPARE s;

-- +migrate Down

-- (leaves style_number nullable and the widened CHECK; a Down is not exercised in prod automigrate)
SELECT 1;
