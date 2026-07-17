-- +migrate Up
-- PLM-rework Q1 (tmp/plm-rework/01-DOMAIN-MODEL.md §2.1): Style Number identity.
--   * style_number_source (generated|manual) records provenance so the API can enforce the strict
--     manual-override path only when the owner deliberately overrides the generated proposal.
--   * The composite UNIQUE(style_number, season) is replaced by a GLOBAL UNIQUE(style_number): an
--     article is unique across all seasons, not per-season. NULL stays allowed (idea drafts are
--     pre-article) — MySQL treats NULLs as distinct, so many idea drafts coexist.
--   * tech_card gains created_by/updated_by (audit norm §2.11 — 0% coverage before this).
--
-- Data safety (dedupe BEFORE the UNIQUE, precedent 0059 + CLAUDE.md): a live prod row set has
-- style_number='pack' twice with NULL season, legal under the composite key but a collision under
-- the global one. Empty-string numbers are normalised to NULL, then each duplicate group keeps its
-- lowest id and the rest are moved into a persisted quarantine report and NULLed (source→generated,
-- so the app re-proposes one). The report survives retries (INSERT IGNORE, no rebuild) so the audit
-- of what was cleared is not lost if a later step trips and the file re-runs.
--
-- Idempotent: guarded ADD COLUMN / ADD CONSTRAINT / DROP INDEX / ADD INDEX via information_schema
-- (multi-line PREPARE/EXECUTE/DEALLOCATE — a single line trips 1064 on the managed DSN, see 0154);
-- the report table is created with an IF-NOT-EXISTS guard; backfills touch only offending rows.

-- ============================================================================
-- 1) Provenance column + named CHECK
-- ============================================================================
SET @need_sns := (SELECT COUNT(*) = 0 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card' AND COLUMN_NAME = 'style_number_source');
SET @sql := IF(@need_sns,
    'ALTER TABLE tech_card ADD COLUMN style_number_source VARCHAR(16) NOT NULL DEFAULT ''generated''',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

SET @need_sns_chk := (SELECT COUNT(*) = 0 FROM information_schema.TABLE_CONSTRAINTS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card' AND CONSTRAINT_NAME = 'chk_tech_card_style_number_source');
SET @sql := IF(@need_sns_chk,
    'ALTER TABLE tech_card ADD CONSTRAINT chk_tech_card_style_number_source CHECK (style_number_source IN (''generated'',''manual''))',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

-- ============================================================================
-- 2) Audit columns (norm §2.11)
-- ============================================================================
SET @need_cb := (SELECT COUNT(*) = 0 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card' AND COLUMN_NAME = 'created_by');
SET @sql := IF(@need_cb,
    'ALTER TABLE tech_card ADD COLUMN created_by VARCHAR(255) NOT NULL DEFAULT ''''',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

SET @need_ub := (SELECT COUNT(*) = 0 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card' AND COLUMN_NAME = 'updated_by');
SET @sql := IF(@need_ub,
    'ALTER TABLE tech_card ADD COLUMN updated_by VARCHAR(255) NOT NULL DEFAULT ''''',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

-- ============================================================================
-- 3) Dedupe style_number BEFORE the global UNIQUE (precedent 0059)
-- ============================================================================
-- Empty string is semantically "unset" and would collide under UNIQUE (only NULLs are distinct).
UPDATE tech_card SET style_number = NULL WHERE style_number = '';

CREATE TABLE IF NOT EXISTS tech_card_style_number_dedup_report (
    tech_card_id      INT          NOT NULL,
    style_number      VARCHAR(255) NOT NULL,
    kept_tech_card_id INT          NOT NULL,
    reason            VARCHAR(64)  NOT NULL DEFAULT 'duplicate_style_number',
    reported_at       TIMESTAMP    NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (tech_card_id)
) COMMENT 'WS1/Q1 dedupe audit: cards whose duplicate style_number was cleared before the global UNIQUE';

-- Record the losers (every row past the lowest id in a duplicate group) before clearing them.
-- INSERT IGNORE + no rebuild keeps the audit across a crash-retry that has already NULLed them.
INSERT IGNORE INTO tech_card_style_number_dedup_report (tech_card_id, style_number, kept_tech_card_id)
SELECT d.id, d.style_number, d.keep_id
FROM (
    SELECT id, style_number,
           MIN(id) OVER (PARTITION BY style_number) AS keep_id,
           ROW_NUMBER() OVER (PARTITION BY style_number ORDER BY id) AS rn
    FROM tech_card
    WHERE style_number IS NOT NULL AND style_number <> ''
) d
WHERE d.rn > 1;

-- Clear the losers: NULL the article and reset provenance so the app re-proposes one.
UPDATE tech_card tc
JOIN tech_card_style_number_dedup_report r ON r.tech_card_id = tc.id
SET tc.style_number = NULL, tc.style_number_source = 'generated'
WHERE tc.style_number IS NOT NULL;

-- ============================================================================
-- 4) Composite UNIQUE(style_number, season) -> global UNIQUE(style_number)
-- ============================================================================
SET @have_composite := (SELECT COUNT(*) FROM information_schema.STATISTICS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card' AND INDEX_NAME = 'uniq_tech_card_style_number_season');
SET @sql := IF(@have_composite > 0,
    'ALTER TABLE tech_card DROP INDEX uniq_tech_card_style_number_season',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

SET @need_global := (SELECT COUNT(*) = 0 FROM information_schema.STATISTICS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card' AND INDEX_NAME = 'uniq_tech_card_style_number');
SET @sql := IF(@need_global,
    'ALTER TABLE tech_card ADD UNIQUE INDEX uniq_tech_card_style_number (style_number)',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

-- +migrate Down
-- Best-effort reverse (forward-only auto-migrate in prod; the composite key is not restored because
-- the deduped data may no longer satisfy it).
SET @sql := IF((SELECT COUNT(*) FROM information_schema.STATISTICS
        WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card' AND INDEX_NAME = 'uniq_tech_card_style_number') > 0,
    'ALTER TABLE tech_card DROP INDEX uniq_tech_card_style_number',
    'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;
