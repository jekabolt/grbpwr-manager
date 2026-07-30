-- +migrate Up
-- Aligns email_campaign_recipient.email with storefront_account.email so the campaign dispatcher
-- stops dying on MySQL Error 1267 (illegal mix of collations).
--
-- 0208 created email_campaign_recipient with an explicit CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci.
-- storefront_account (0047) has no charset clause at all, so its email column inherits whatever the
-- schema default was when that table was created, and every later ALTER only added columns.
-- internal/store/campaign/recipient.go then compares the two columns directly, twice - in
-- skipComplianceChanged (UPDATE ... LEFT JOIN storefront_account sa ON sa.id = ecr.account_id AND
-- sa.email = ecr.email) and in the FOR UPDATE claim query. When two IMPLICIT column collations of the
-- same charset meet in one comparison MySQL raises an error instead of coercing, so every dispatcher
-- tick fails, the worker enters permanent backoff and the campaign stays in status 'sending' forever.
--
-- Exactly which of the two flavours of mismatch each environment has cannot be read from the repo,
-- and the two behave differently (verified on MySQL 8.0.46 against simulated schemas).
--   * SAME charset, different collation - for example beta, where storefront_account resolves to
--     utf8mb4_0900_ai_ci - is a hard Error 1267. Reproduced.
--   * DIFFERENT charset, for example a utf8mb3 storefront_account against this utf8mb4 column, does
--     NOT error. MySQL widens the narrower operand and borrows the wider side's collation.
-- So beta is provably broken today, and prod is broken only if its legacy schema default landed on a
-- utf8mb4 collation other than utf8mb4_unicode_ci. Rather than betting on which case prod is in, this
-- migration reads the target charset and collation from information_schema and aligns unconditionally,
-- applying the ALTER through PREPARE the way 0208 and 0209 do. That also removes the trap that bit
-- the accounting store twice - once an explicit COLLATE utf8mb4_unicode_ci is written into a query
-- against a utf8mb3 operand, the widening rule no longer saves it and it fails with Error 1253.
--
-- Only the email column is touched, and the table default stays utf8mb4_unicode_ci because no other
-- column here is ever compared against another table. The target is also constrained to the utf8
-- family, so a hypothetical 8-bit legacy default such as latin1 can never narrow this column - that
-- case is one of the widening cases above and needs no fix.
--
-- Idempotent and self-skipping. If the two columns already agree, either table is missing, or the
-- target is outside the utf8 family, the statement degrades to SELECT 1. PREPARE / EXECUTE /
-- DEALLOCATE stay one per line because the managed beta and prod DSNs do not enable multiStatements.
--
-- Data safety. On prod email_campaign_recipient is created empty by 0208 in this same boot, so there
-- is nothing to convert. On beta the target charset is utf8mb4 as well, so the rewrite is a pure
-- recollation with no character conversion. Values in this column always originate from
-- storefront_account.email (fanout copies them), which is itself constrained to ASCII by its CHECK.

SET @target_charset := (SELECT CHARACTER_SET_NAME FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'storefront_account' AND COLUMN_NAME = 'email');
SET @target_collation := (SELECT COLLATION_NAME FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'storefront_account' AND COLUMN_NAME = 'email');
SET @current_collation := (SELECT COLLATION_NAME FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'email_campaign_recipient' AND COLUMN_NAME = 'email');
SET @sql := IF(
    @target_charset IS NULL
        OR @target_collation IS NULL
        OR @current_collation IS NULL
        OR @current_collation = @target_collation
        OR @target_charset NOT IN ('utf8mb3', 'utf8mb4'),
    'SELECT 1',
    CONCAT('ALTER TABLE email_campaign_recipient MODIFY COLUMN email VARCHAR(254) CHARACTER SET ',
        @target_charset, ' COLLATE ', @target_collation, ' NOT NULL'));
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

-- +migrate Down
-- Restores the 0208 declaration. Non-destructive in both directions - utf8mb4 is a strict superset of
-- the utf8mb3 family, all three collations involved are case and accent insensitive, and no row is
-- deleted or truncated. Guarded so a Down on a database where Up was a no-op is also a no-op.

SET @current_collation := (SELECT COLLATION_NAME FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'email_campaign_recipient' AND COLUMN_NAME = 'email');
SET @sql := IF(@current_collation IS NULL OR @current_collation = 'utf8mb4_unicode_ci',
    'SELECT 1',
    'ALTER TABLE email_campaign_recipient MODIFY COLUMN email VARCHAR(254) CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci NOT NULL');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;
