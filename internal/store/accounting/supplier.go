package accounting

import (
	"context"
	"fmt"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
)

// AP/AR subledgers (phase 2, wave 4 — docs/plan-accounting-phase2/04-wave4-money.md §4.4). supplier is
// the purchase-side catalog; GetPayables nets open Accounts-Payable (2010) per supplier from entries the
// material-receipt posting tagged with supplier_id (plus manual payments tagged the same way);
// GetReceivables nets open Accounts-Receivable (1040) per bank-invoice order from the ledger.

// CreateSupplier inserts a supplier and returns its id. A duplicate name is a unique-violation the caller
// maps to InvalidArgument.
func (s *Store) CreateSupplier(ctx context.Context, in entity.SupplierInsert) (int, error) {
	id, err := storeutil.ExecNamedLastId(ctx, s.DB,
		`INSERT INTO supplier (name, vat_id, notes) VALUES (:name, :vat_id, :notes)`,
		map[string]any{"name": in.Name, "vat_id": in.VatId, "notes": in.Notes})
	if err != nil {
		return 0, fmt.Errorf("accounting: create supplier %q: %w", in.Name, err)
	}
	return id, nil
}

// ListSuppliers returns the supplier catalog, name-ordered.
func (s *Store) ListSuppliers(ctx context.Context) ([]entity.Supplier, error) {
	suppliers, err := storeutil.QueryListNamed[entity.Supplier](ctx, s.DB,
		`SELECT id, name, vat_id, notes, created_at FROM supplier ORDER BY name`, nil)
	if err != nil {
		return nil, fmt.Errorf("accounting: list suppliers: %w", err)
	}
	return suppliers, nil
}

// GetPayables returns the open Accounts-Payable (2010) position per supplier: Accrued is the Σ 2010
// credits of the supplier's tagged entries (material receipts owed), Paid the Σ 2010 debits (manual
// payments tagged with the supplier), Balance = Accrued − Paid. Rows are grouped by acct_journal_entry.
// supplier_id (0 = entries with a 2010 movement but no supplier tag). Only positions with a non-zero
// balance are returned (the open AP), largest first. Reversals are counted as posted (a reversed receipt
// nets to zero within its supplier group); a supplier-less reversal lands in the untagged group.
func (s *Store) GetPayables(ctx context.Context) ([]entity.AcctPayableRow, error) {
	rows, err := storeutil.QueryListNamed[entity.AcctPayableRow](ctx, s.DB, `
		SELECT COALESCE(e.supplier_id, 0)        AS supplier_id,
		       COALESCE(sup.name, '(untagged)')  AS supplier_name,
		       COALESCE(SUM(CASE WHEN l.side = 'credit' THEN l.amount ELSE 0 END), 0) AS accrued,
		       COALESCE(SUM(CASE WHEN l.side = 'debit'  THEN l.amount ELSE 0 END), 0) AS paid
		FROM acct_journal_line l
		JOIN acct_journal_entry e ON e.id = l.entry_id
		JOIN acct_account a       ON a.id = l.account_id
		LEFT JOIN supplier sup    ON sup.id = e.supplier_id
		WHERE a.code = '2010'
		GROUP BY COALESCE(e.supplier_id, 0), sup.name
		HAVING (accrued - paid) <> 0
		ORDER BY (accrued - paid) DESC`, nil)
	if err != nil {
		return nil, fmt.Errorf("accounting: get payables: %w", err)
	}
	for i := range rows {
		rows[i].Balance = rows[i].Accrued.Sub(rows[i].Paid)
	}
	return rows, nil
}

// GetReceivables returns the open Accounts-Receivable (1040) position per bank-invoice order: Invoiced is
// the Σ 1040 debits (revenue recognised against a receivable), Received the Σ 1040 credits (a payment or
// refund), Balance = Invoiced − Received. Lines are grouped by the order uuid (SUBSTRING_INDEX of
// source_key, so an order_refund 'uuid:seq' nets with its 'uuid' invoice, and a payment keyed to the order
// nets too). Requiring Invoiced > 0 keeps only real invoices (a stray untagged 1040 credit forms no row).
// Only non-zero balances are returned, largest first.
func (s *Store) GetReceivables(ctx context.Context) ([]entity.AcctReceivableRow, error) {
	rows, err := storeutil.QueryListNamed[entity.AcctReceivableRow](ctx, s.DB, `
		SELECT SUBSTRING_INDEX(e.source_key, ':', 1) AS ref,
		       COALESCE(SUM(CASE WHEN l.side = 'debit'  THEN l.amount ELSE 0 END), 0) AS invoiced,
		       COALESCE(SUM(CASE WHEN l.side = 'credit' THEN l.amount ELSE 0 END), 0) AS received
		FROM acct_journal_line l
		JOIN acct_journal_entry e ON e.id = l.entry_id
		JOIN acct_account a       ON a.id = l.account_id
		WHERE a.code = '1040'
		  -- Only order-keyed 1040 lines (source_key = order uuid) form real receivable rows. Bank-posted
		  -- entries (source_key 'bank:<id>') and manual payments ('manual:<uuid>') both have source_type
		  -- 'manual' and would collapse into a phantom ref='bank'/'manual' group (LOW-2), so exclude them.
		  AND e.source_type <> 'manual'
		GROUP BY SUBSTRING_INDEX(e.source_key, ':', 1)
		HAVING invoiced > 0 AND (invoiced - received) <> 0
		ORDER BY (invoiced - received) DESC`, nil)
	if err != nil {
		return nil, fmt.Errorf("accounting: get receivables: %w", err)
	}
	for i := range rows {
		rows[i].Balance = rows[i].Invoiced.Sub(rows[i].Received)
	}
	return rows, nil
}
