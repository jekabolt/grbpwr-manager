-- +migrate Up
-- PLM-rework Q1 (tmp/plm-rework/01-DOMAIN-MODEL.md §2.1): the "version" of a tech card becomes a
-- sequence of named immutable releases (Rev.N) plus a server-stamped auto-journal — the free-text
-- version string is retired (dropped in M3; the write path stops setting it in the follow-up).
--
--   * tech_card_release.release_number: the user-facing "Rev.N" the factory reads, auto MAX+1 per
--     card. Backfilled sequentially over existing snapshots by created order, then UNIQUE(card, N).
--   * tech_card_revision gains `action` (created|updated|approved|released|reverted|role_assigned|
--     other) and `created_at` so the reprofiled table can be a server-stamped auto-journal (who/what/
--     when) instead of a client free-text log. `change_note` is reused as the human summary; the
--     legacy version/revision_date/section-free-text columns stay until M3. `action` is a NEW column
--     (DEFAULT 'other') so its CHECK cannot fail existing rows; the section CHECK is deferred to M3
--     (the journal writes enum-valid sections, and enforcing it on the legacy free-text column would
--     require sanitising old rows first).
--
-- Idempotent: guarded ADD COLUMN / ADD CONSTRAINT / ADD INDEX via information_schema (multi-line
-- PREPARE/EXECUTE/DEALLOCATE, see 0154); the backfill touches only still-NULL release_number rows.

-- ============================================================================
-- 1) tech_card_release.release_number (Rev.N) + backfill + UNIQUE
-- ============================================================================
SET @need_rn := (SELECT COUNT(*) = 0 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_release' AND COLUMN_NAME = 'release_number');
SET @sql := IF(@need_rn,
    'ALTER TABLE tech_card_release ADD COLUMN release_number INT NULL',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

-- Number existing snapshots per card by creation order (stable: created_at then id).
UPDATE tech_card_release r
JOIN (
    SELECT id, ROW_NUMBER() OVER (PARTITION BY tech_card_id ORDER BY created_at, id) AS rn
    FROM tech_card_release
) seq ON seq.id = r.id
SET r.release_number = seq.rn
WHERE r.release_number IS NULL;

SET @need_rn_uniq := (SELECT COUNT(*) = 0 FROM information_schema.STATISTICS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_release' AND INDEX_NAME = 'uniq_tech_card_release_number');
SET @sql := IF(@need_rn_uniq,
    'ALTER TABLE tech_card_release ADD UNIQUE INDEX uniq_tech_card_release_number (tech_card_id, release_number)',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

-- ============================================================================
-- 2) tech_card_revision -> auto-journal columns (action + created_at)
-- ============================================================================
SET @need_action := (SELECT COUNT(*) = 0 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_revision' AND COLUMN_NAME = 'action');
SET @sql := IF(@need_action,
    "ALTER TABLE tech_card_revision ADD COLUMN action VARCHAR(24) NOT NULL DEFAULT 'other'",
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

SET @need_action_chk := (SELECT COUNT(*) = 0 FROM information_schema.TABLE_CONSTRAINTS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_revision' AND CONSTRAINT_NAME = 'chk_tech_card_revision_action');
SET @sql := IF(@need_action_chk,
    "ALTER TABLE tech_card_revision ADD CONSTRAINT chk_tech_card_revision_action CHECK (action IN ('created','updated','approved','released','reverted','role_assigned','other'))",
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

SET @need_rev_ca := (SELECT COUNT(*) = 0 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_revision' AND COLUMN_NAME = 'created_at');
SET @sql := IF(@need_rev_ca,
    'ALTER TABLE tech_card_revision ADD COLUMN created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

-- +migrate Down
SET @sql := IF((SELECT COUNT(*) FROM information_schema.STATISTICS
        WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_release' AND INDEX_NAME = 'uniq_tech_card_release_number') > 0,
    'ALTER TABLE tech_card_release DROP INDEX uniq_tech_card_release_number',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;
