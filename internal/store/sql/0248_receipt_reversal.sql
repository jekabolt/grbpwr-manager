-- +migrate Up

-- production-costing Phase 6 (plan 05): receipt reversal.
--
-- 1. production_run_event — the run's append-only audit trail. Phase 6 lands the MINIMAL shape
--    (Phase 8 extends it with more event types and a read surface): who did what to the run, why,
--    with a JSON payload describing the effects (stock deltas, compensated FG, cost_price actions).
--    ON DELETE CASCADE mirrors the run's other children — DeleteProductionRun already refuses runs
--    with applied facts, so a cascading delete only ever drops events of a plan-stage run.
CREATE TABLE IF NOT EXISTS production_run_event (
    id INT AUTO_INCREMENT PRIMARY KEY,
    run_id INT NOT NULL,
    event_type VARCHAR(32) NOT NULL,
    actor VARCHAR(255) NULL,
    reason VARCHAR(512) NULL,
    payload JSON NULL,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT fk_pre_run FOREIGN KEY (run_id) REFERENCES production_run (id) ON DELETE CASCADE,
    INDEX idx_pre_run_created (run_id, created_at)
);

-- 2. Extend chk_acct_entry_source_type (+production_receive_reversal). This migration sorts LAST,
--    so its list MUST be the UNION of every source type ever added (0189/0195/0196/0197/0201 —
--    mirrors entity.ValidAcctSourceTypes). The scoped compensation entry a reversal books
--    (Dr 1120 WIP / Cr 1130 FG) deliberately does NOT reuse the generic 'reversal' source: it
--    reverses only the FG transfer of a production_receive entry — the manual/AP capitalisation
--    stays payable — and reconciliation must be able to tell the two apart.
SET @sql := IF((SELECT COUNT(*) FROM information_schema.TABLE_CONSTRAINTS
    WHERE CONSTRAINT_SCHEMA = DATABASE() AND TABLE_NAME = 'acct_journal_entry'
      AND CONSTRAINT_NAME = 'chk_acct_entry_source_type') > 0,
    'ALTER TABLE acct_journal_entry DROP CONSTRAINT chk_acct_entry_source_type', 'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

SET @sql := IF((SELECT COUNT(*) FROM information_schema.TABLE_CONSTRAINTS
    WHERE CONSTRAINT_SCHEMA = DATABASE() AND TABLE_NAME = 'acct_journal_entry'
      AND CONSTRAINT_NAME = 'chk_acct_entry_source_type') = 0,
    'ALTER TABLE acct_journal_entry ADD CONSTRAINT chk_acct_entry_source_type CHECK (source_type IN (
        ''order_sale'',''order_refund'',
        ''order_prepayment'',''order_transit'',''order_delivered_sale'',
        ''material_receipt'',''material_issue'',''material_return'',
        ''material_writeoff'',''material_adjustment'',
        ''production_receive'',''production_receive_reversal'',''opex_month'',
        ''shipping_actual'',''dev_expense'',
        ''depreciation'',''corp_tax'',
        ''order_dispute'',
        ''manual'',''reversal''))', 'SELECT 1');
PREPARE s FROM @sql;
EXECUTE s;
DEALLOCATE PREPARE s;

-- +migrate Down

-- The CHECK narrowing is deliberately not reversed (posted compensation entries would violate it).
DROP TABLE IF EXISTS production_run_event;
