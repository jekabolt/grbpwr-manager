-- +migrate Up

-- production-costing Phase 4 (receipt v1, plan file 05): the receipt EVENT tables. Receiving stops
-- being a mutable pair of columns on the plan grid and becomes an immutable record: WHO received
-- WHAT, WHEN, at what frozen valuation — the unit the accounting worker posts by (source_key
-- 'receipt:<id>') and the unit Phase 6 will reverse. v1 is final-only: one receipt closes the run;
-- partial receipts (Phase 5) and reversals (Phase 6) land on these same tables, which is why
-- reversal_of / reversed_by exist now — adding FK columns later costs an ALTER on a hot table,
-- carrying them empty costs nothing.
--
-- run_id has NO ON DELETE CASCADE deliberately: a receipt is a financial record. DeleteProductionRun
-- refuses received runs at the app layer; this FK makes the DB refuse too (defense in depth).
CREATE TABLE IF NOT EXISTS production_run_receipt (
    id INT AUTO_INCREMENT PRIMARY KEY,
    run_id INT NOT NULL,
    received_at DATETIME NOT NULL,
    admin_username VARCHAR(255) NULL,
    note VARCHAR(512) NULL,
    -- client-minted command identity (26 chars [0-9A-Z], same shape as line_key); the UNIQUE makes a
    -- raced duplicate command die on insert even if it slipped past the command_idempotency check.
    idempotency_key VARCHAR(64) NOT NULL,
    -- frozen valuation at receipt time: the run's actual unit cost in base currency the moment the
    -- goods were booked. NULL = not computable then (uncosted issues / unfolded articles) — later
    -- costing edits do NOT retroactively change what this receipt was valued at.
    unit_cost_base DECIMAL(12, 2) NULL,
    base_currency VARCHAR(4) NULL,
    has_base TINYINT(1) NOT NULL DEFAULT 0,
    -- Phase 6 linkage (unused in v1): a reversal receipt points at what it reverses via reversal_of;
    -- the reversed receipt gets reversed_by stamped. Both NULL for a normal receipt.
    reversal_of INT NULL,
    reversed_by INT NULL,
    -- posting observability (plan 05 amendment 8): the receipt is the accounting outbox. pending →
    -- posted by the worker; dead_letter after repeated failures (alerted, excluded from the scan,
    -- still blocks period close). Flip back to pending manually to retry a dead-lettered receipt.
    posting_status VARCHAR(16) NOT NULL DEFAULT 'pending',
    posting_attempts INT NOT NULL DEFAULT 0,
    last_posting_error VARCHAR(512) NULL,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT fk_prr_run FOREIGN KEY (run_id) REFERENCES production_run(id),
    CONSTRAINT fk_prr_reversal_of FOREIGN KEY (reversal_of) REFERENCES production_run_receipt(id),
    CONSTRAINT fk_prr_reversed_by FOREIGN KEY (reversed_by) REFERENCES production_run_receipt(id),
    CONSTRAINT uq_prr_idempotency UNIQUE (idempotency_key),
    CONSTRAINT chk_prr_posting_status CHECK (posting_status REGEXP '^(pending|posted|dead_letter)$'),
    INDEX idx_prr_run (run_id),
    INDEX idx_prr_posting (posting_status, received_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

-- One receipt line per counted plan line. run_line_id is the WHOLE POINT of the line_key pre-PR
-- (0230): plan lines keep their ids across edits, so this FK stays valid. product_id/size_id are
-- SNAPSHOTS of what was booked (the plan line is mutable in the Phase 5 partial world; the receipt
-- is not). No ON DELETE CASCADE from run_line: deleting a counted plan line must fail at the DB.
CREATE TABLE IF NOT EXISTS production_run_receipt_line (
    id INT AUTO_INCREMENT PRIMARY KEY,
    receipt_id INT NOT NULL,
    run_line_id INT NOT NULL,
    product_id INT NULL,
    size_id INT NULL,
    good_qty INT NOT NULL DEFAULT 0,
    defect_qty INT NOT NULL DEFAULT 0,
    CONSTRAINT fk_prrl_receipt FOREIGN KEY (receipt_id) REFERENCES production_run_receipt(id) ON DELETE CASCADE,
    CONSTRAINT fk_prrl_line FOREIGN KEY (run_line_id) REFERENCES production_run_line(id),
    CONSTRAINT chk_prrl_good CHECK (good_qty >= 0),
    CONSTRAINT chk_prrl_defect CHECK (defect_qty >= 0),
    CONSTRAINT uq_prrl_receipt_line UNIQUE (receipt_id, run_line_id),
    INDEX idx_prrl_receipt (receipt_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

-- Backfill: one synthetic receipt per already-received run, so "every received run has >= 1
-- receipt" is an invariant the scanner, ClosePeriod and Phase 6 can rely on. received_at is the
-- run's stamp; the valuation is NOT retro-frozen (NULL — inventing a figure the receipt was never
-- booked at would be worse than admitting it is unknown). idempotency_key mirrors 0230's LEGACY
-- padding: unique because run id is, never colliding with client-minted Crockford ULIDs ('L').
INSERT INTO production_run_receipt (run_id, received_at, admin_username, note, idempotency_key, posting_status)
SELECT r.id, r.received_at, NULL, 'backfilled from pre-receipt receive flow', CONCAT('LEGACY', LPAD(r.id, 20, '0')), 'pending'
FROM production_run r
WHERE r.status IN ('received', 'closed') AND r.received_at IS NOT NULL
  AND NOT EXISTS (SELECT 1 FROM production_run_receipt x WHERE x.run_id = r.id);

-- Backfill lines from the plan grid's counted rows (received_qty/defect_qty were the only receive
-- record until now). Uncounted rows carry no receipt fact and are skipped.
INSERT INTO production_run_receipt_line (receipt_id, run_line_id, product_id, size_id, good_qty, defect_qty)
SELECT pr.id, l.id, l.product_id, l.size_id, COALESCE(l.received_qty, 0), COALESCE(l.defect_qty, 0)
FROM production_run_receipt pr
JOIN production_run_line l ON l.run_id = pr.run_id
WHERE pr.idempotency_key LIKE 'LEGACY%'
  AND (COALESCE(l.received_qty, 0) > 0 OR COALESCE(l.defect_qty, 0) > 0)
  AND NOT EXISTS (SELECT 1 FROM production_run_receipt_line e WHERE e.receipt_id = pr.id AND e.run_line_id = l.id);

-- +migrate Down

DROP TABLE IF EXISTS production_run_receipt_line;
DROP TABLE IF EXISTS production_run_receipt;
