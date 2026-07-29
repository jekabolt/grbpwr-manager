-- +migrate Up

-- Makes "this section changed after it was signed off" survive a page reload (PLM UI gap 9).
--
-- tech_card_signoff records that a section was approved, but nothing about WHAT was approved. So the
-- admin could only compare against what it had watched change in the current browser session: approve
-- the construction sheet, edit an operation, and the warning appeared -- reload, and it was gone,
-- because the page had nothing left to compare against. A sign-off that silently stops meaning
-- anything is worse than no sign-off.
--
-- signed_digest is the fingerprint of that section's content at the moment it was approved. The read
-- path recomputes the same fingerprint from the current card (TechCard.section_digests); equal means
-- the sheet still says what was approved, different means it was edited afterwards.
--
-- WHY PER SECTION, not the card's lock_version: lock_version bumps on every save, so comparing
-- against it would invalidate the design sign-off because someone touched the costing sheet. The
-- point of per-section sign-off is that the sections are independent.
--
-- NULL for a pending/rejected section and for every pre-existing row: a sign-off taken before this
-- shipped has no recorded content, and the read side treats "no digest" as "cannot tell", not as
-- "changed" -- inventing a mismatch would flag every historical approval as stale on deploy day.
--
-- 64 chars: hex SHA-256. Idempotent via an information_schema guard.

SET @col_exists := (
    SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'tech_card_signoff'
      AND COLUMN_NAME = 'signed_digest'
);
SET @ddl := IF(@col_exists = 0,
    'ALTER TABLE tech_card_signoff ADD COLUMN signed_digest CHAR(64) NULL COMMENT ''hex SHA-256 of the section content when it was approved; NULL = not recorded''',
    'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- +migrate Down
SET @col_exists := (
    SELECT COUNT(*) FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'tech_card_signoff'
      AND COLUMN_NAME = 'signed_digest'
);
SET @ddl := IF(@col_exists = 1, 'ALTER TABLE tech_card_signoff DROP COLUMN signed_digest', 'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;
