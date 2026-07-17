-- +migrate Up
-- PLM-rework Q5 (tmp/plm-rework/01-DOMAIN-MODEL.md §2.1): roles as admin accounts, multi per role.
-- The free-text designer/constructor/technologist/approved_by strings are replaced by a real
-- assignment table with a FK to admins(id) — the same shape as admin_permission (0087), the existing
-- precedent for "this account IS X of this row". RESTRICT (not CASCADE): a role assignment is a
-- compliance record ("who signed off the spec", Q5), so removing an account must not silently erase
-- it — the account is taken out of its roles explicitly first.
--
-- Backfill: each free-text role value is matched (case-insensitive, trimmed) against admins.username.
-- A match becomes an assignment; an unmatched non-empty value is preserved in a quarantine report so
-- nothing is lost (INSERT IGNORE keeps it idempotent). The free-text columns are NOT dropped here —
-- that destructive step is M3, gated on the read/write path no longer depending on them.
--
-- Idempotent: tables use an IF-NOT-EXISTS guard; the backfill inserts are INSERT IGNORE against
-- unique keys, so a retried partial apply neither fails nor double-inserts.

CREATE TABLE IF NOT EXISTS tech_card_role_assignment (
    id           INT          PRIMARY KEY AUTO_INCREMENT,
    tech_card_id INT          NOT NULL,
    role         VARCHAR(24)  NOT NULL,
    admin_id     INT          NOT NULL,
    assigned_by  VARCHAR(255) NOT NULL DEFAULT '',
    assigned_at  TIMESTAMP    NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT uniq_tech_card_role_assignment UNIQUE (tech_card_id, role, admin_id),
    CONSTRAINT chk_tech_card_role CHECK (role IN ('designer','constructor','technologist','pattern_maker','grader','approver','other')),
    CONSTRAINT fk_tcra_tech_card FOREIGN KEY (tech_card_id) REFERENCES tech_card(id) ON DELETE CASCADE,
    CONSTRAINT fk_tcra_admin FOREIGN KEY (admin_id) REFERENCES admins(id) ON DELETE RESTRICT
) COMMENT 'WS1/Q5 role assignments: which admin account is designer/constructor/... of a tech card (multi)';

CREATE TABLE IF NOT EXISTS tech_card_role_backfill_report (
    id           INT          PRIMARY KEY AUTO_INCREMENT,
    tech_card_id INT          NOT NULL,
    role         VARCHAR(24)  NOT NULL,
    raw_value    VARCHAR(255) NOT NULL,
    reason       VARCHAR(64)  NOT NULL DEFAULT 'no_admin_match',
    reported_at  TIMESTAMP    NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT uniq_tcra_backfill UNIQUE (tech_card_id, role)
) COMMENT 'WS1/Q5 backfill audit: free-text role values that matched no admin username';

-- Backfill matched assignments (designer/constructor/technologist -> same role; approved_by -> approver).
INSERT IGNORE INTO tech_card_role_assignment (tech_card_id, role, admin_id, assigned_by)
SELECT tc.id, 'designer', a.id, 'migration_0161'
FROM tech_card tc JOIN admins a ON LOWER(TRIM(a.username)) = LOWER(TRIM(tc.designer))
WHERE tc.designer IS NOT NULL AND TRIM(tc.designer) <> '';

INSERT IGNORE INTO tech_card_role_assignment (tech_card_id, role, admin_id, assigned_by)
SELECT tc.id, 'constructor', a.id, 'migration_0161'
FROM tech_card tc JOIN admins a ON LOWER(TRIM(a.username)) = LOWER(TRIM(tc.constructor))
WHERE tc.constructor IS NOT NULL AND TRIM(tc.constructor) <> '';

INSERT IGNORE INTO tech_card_role_assignment (tech_card_id, role, admin_id, assigned_by)
SELECT tc.id, 'technologist', a.id, 'migration_0161'
FROM tech_card tc JOIN admins a ON LOWER(TRIM(a.username)) = LOWER(TRIM(tc.technologist))
WHERE tc.technologist IS NOT NULL AND TRIM(tc.technologist) <> '';

INSERT IGNORE INTO tech_card_role_assignment (tech_card_id, role, admin_id, assigned_by)
SELECT tc.id, 'approver', a.id, 'migration_0161'
FROM tech_card tc JOIN admins a ON LOWER(TRIM(a.username)) = LOWER(TRIM(tc.approved_by))
WHERE tc.approved_by IS NOT NULL AND TRIM(tc.approved_by) <> '';

-- Quarantine the unmatched non-empty values (one row per card+role; INSERT IGNORE for retries).
INSERT IGNORE INTO tech_card_role_backfill_report (tech_card_id, role, raw_value)
SELECT tc.id, 'designer', TRIM(tc.designer) FROM tech_card tc
WHERE tc.designer IS NOT NULL AND TRIM(tc.designer) <> ''
  AND NOT EXISTS (SELECT 1 FROM admins a WHERE LOWER(TRIM(a.username)) = LOWER(TRIM(tc.designer)));

INSERT IGNORE INTO tech_card_role_backfill_report (tech_card_id, role, raw_value)
SELECT tc.id, 'constructor', TRIM(tc.constructor) FROM tech_card tc
WHERE tc.constructor IS NOT NULL AND TRIM(tc.constructor) <> ''
  AND NOT EXISTS (SELECT 1 FROM admins a WHERE LOWER(TRIM(a.username)) = LOWER(TRIM(tc.constructor)));

INSERT IGNORE INTO tech_card_role_backfill_report (tech_card_id, role, raw_value)
SELECT tc.id, 'technologist', TRIM(tc.technologist) FROM tech_card tc
WHERE tc.technologist IS NOT NULL AND TRIM(tc.technologist) <> ''
  AND NOT EXISTS (SELECT 1 FROM admins a WHERE LOWER(TRIM(a.username)) = LOWER(TRIM(tc.technologist)));

INSERT IGNORE INTO tech_card_role_backfill_report (tech_card_id, role, raw_value)
SELECT tc.id, 'approver', TRIM(tc.approved_by) FROM tech_card tc
WHERE tc.approved_by IS NOT NULL AND TRIM(tc.approved_by) <> ''
  AND NOT EXISTS (SELECT 1 FROM admins a WHERE LOWER(TRIM(a.username)) = LOWER(TRIM(tc.approved_by)));

-- +migrate Down
DROP TABLE IF EXISTS tech_card_role_backfill_report;
DROP TABLE IF EXISTS tech_card_role_assignment;
