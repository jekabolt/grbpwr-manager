package accounting

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
)

// Revolut bank inbox store (phase 2, wave 4 — docs/plan-accounting-phase2/04-wave4-money.md §4.1). The
// parser (internal/accounting) produces AcctBankTxnInsert rows; this layer deduplicates them into
// acct_bank_txn, applies the acct_bank_rule substring suggestions, and drives the inbox lifecycle
// (post / ignore). PostBankTxn's journal entry is built + FX-folded + posted by the admin handler, which
// then calls SetBankTxnPosted here.

// bankTxnColumns is the acct_bank_txn read projection.
const bankTxnColumns = `id, source, external_id, booked_at, amount, currency, fee, description,
	counterparty, state, matched_entry_id, suggested_account, created_at`

// ImportBankTxns inserts parsed inbox lines, deduplicating on external_id (a re-imported statement is a
// no-op for lines already present). Before insert it applies the acct_bank_rule substring suggestions to
// each still-unmatched line (a rule whose pattern is a case-insensitive substring of the counterparty or
// description sets suggested_account). Returns how many lines were parsed / newly imported / skipped.
func (s *Store) ImportBankTxns(ctx context.Context, txns []entity.AcctBankTxnInsert) (entity.AcctBankImportResult, error) {
	res := entity.AcctBankImportResult{Parsed: len(txns)}
	if len(txns) == 0 {
		return res, nil
	}

	rules, err := s.ListBankRules(ctx)
	if err != nil {
		return res, err
	}

	for i := range txns {
		t := txns[i]
		if t.State == "" {
			t.State = entity.AcctBankTxnUnmatched
		}
		// Apply rule suggestions only to a line still awaiting a decision.
		if t.State == entity.AcctBankTxnUnmatched && !t.SuggestedAccount.Valid {
			if code := matchBankRule(rules, t.Counterparty, t.Description); code != "" {
				t.SuggestedAccount = sql.NullString{String: code, Valid: true}
			}
		}
		affected, err := storeutil.ExecNamedRows(ctx, s.DB, `
			INSERT INTO acct_bank_txn
				(source, external_id, booked_at, amount, currency, fee, description,
				 counterparty, state, suggested_account, raw)
			VALUES (:source, :external_id, :booked_at, :amount, :currency, :fee, :description,
				:counterparty, :state, :suggested_account, :raw)
			ON DUPLICATE KEY UPDATE id = id`,
			map[string]any{
				"source":            defaultBankSource(t.Source),
				"external_id":       t.ExternalId,
				"booked_at":         t.BookedAt.UTC(),
				"amount":            t.Amount,
				"currency":          t.Currency,
				"fee":               t.Fee,
				"description":       t.Description,
				"counterparty":      t.Counterparty,
				"state":             string(t.State),
				"suggested_account": t.SuggestedAccount,
				"raw":               t.Raw,
			})
		if err != nil {
			return res, fmt.Errorf("accounting: import bank txn %s: %w", t.ExternalId, err)
		}
		// ON DUPLICATE KEY UPDATE id=id reports 0 rows affected for an existing row, 1 for a fresh insert.
		if affected > 0 {
			res.Imported++
		} else {
			res.Skipped++
		}
	}
	return res, nil
}

// defaultBankSource defaults an empty source to 'revolut' (the only parser today).
func defaultBankSource(s string) string {
	if strings.TrimSpace(s) == "" {
		return "revolut"
	}
	return s
}

// matchBankRule returns the account code of the first rule whose pattern is a case-insensitive substring
// of the line's counterparty or description, or "" when none match.
func matchBankRule(rules []entity.AcctBankRule, counterparty sql.NullString, description string) string {
	hay := strings.ToLower(description)
	if counterparty.Valid {
		hay = strings.ToLower(counterparty.String) + " " + hay
	}
	for _, r := range rules {
		p := strings.ToLower(strings.TrimSpace(r.Pattern))
		if p != "" && strings.Contains(hay, p) {
			return r.AccountCode
		}
	}
	return ""
}

// ListBankTxns returns inbox lines filtered by state ("" = all), newest booked first, bounded to a page.
func (s *Store) ListBankTxns(ctx context.Context, state string, limit int) ([]entity.AcctBankTxn, error) {
	if limit <= 0 {
		limit = defaultListLimit
	}
	if limit > maxListLimit {
		limit = maxListLimit
	}
	conds := []string{"1 = 1"}
	params := map[string]any{"limit": limit}
	if strings.TrimSpace(state) != "" {
		conds = append(conds, "state = :state")
		params["state"] = strings.TrimSpace(state)
	}
	txns, err := storeutil.QueryListNamed[entity.AcctBankTxn](ctx, s.DB, `
		SELECT `+bankTxnColumns+`
		FROM acct_bank_txn
		WHERE `+strings.Join(conds, " AND ")+`
		ORDER BY booked_at DESC, id DESC
		LIMIT :limit`, params)
	if err != nil {
		return nil, fmt.Errorf("accounting: list bank txns: %w", err)
	}
	return txns, nil
}

// GetBankTxn loads one inbox line; a missing row surfaces as wrapped sql.ErrNoRows.
func (s *Store) GetBankTxn(ctx context.Context, id int) (*entity.AcctBankTxn, error) {
	txn, err := storeutil.QueryNamedOne[entity.AcctBankTxn](ctx, s.DB,
		`SELECT `+bankTxnColumns+` FROM acct_bank_txn WHERE id = :id`, map[string]any{"id": id})
	if err != nil {
		return nil, fmt.Errorf("accounting: get bank txn %d: %w", id, err)
	}
	return &txn, nil
}

// SetBankTxnPosted marks a line posted and links it to the journal entry PostBankTxn created. It is a
// no-op guard against double-posting: only a non-posted line transitions.
func (s *Store) SetBankTxnPosted(ctx context.Context, id, entryID int) error {
	if err := storeutil.ExecNamed(ctx, s.DB, `
		UPDATE acct_bank_txn
		SET state = 'posted', matched_entry_id = :entry_id
		WHERE id = :id AND state <> 'posted'`,
		map[string]any{"id": id, "entry_id": entryID}); err != nil {
		return fmt.Errorf("accounting: set bank txn %d posted: %w", id, err)
	}
	return nil
}

// SetBankTxnIgnored marks a not-yet-posted line ignored (deliberately not booked). A posted line is left
// untouched (its entry stands). The reason is advisory (logged by the caller); acct_bank_txn has no
// reason column, so it is not persisted.
func (s *Store) SetBankTxnIgnored(ctx context.Context, id int) error {
	if err := storeutil.ExecNamed(ctx, s.DB, `
		UPDATE acct_bank_txn SET state = 'ignored' WHERE id = :id AND state <> 'posted'`,
		map[string]any{"id": id}); err != nil {
		return fmt.Errorf("accounting: set bank txn %d ignored: %w", id, err)
	}
	return nil
}

// ListBankRules returns the substring→account suggestion rules, oldest first.
func (s *Store) ListBankRules(ctx context.Context) ([]entity.AcctBankRule, error) {
	rules, err := storeutil.QueryListNamed[entity.AcctBankRule](ctx, s.DB,
		`SELECT id, pattern, account_code FROM acct_bank_rule ORDER BY id`, nil)
	if err != nil {
		return nil, fmt.Errorf("accounting: list bank rules: %w", err)
	}
	return rules, nil
}

// CreateBankRule inserts a substring→account suggestion rule and returns its id.
func (s *Store) CreateBankRule(ctx context.Context, pattern, accountCode string) (int, error) {
	id, err := storeutil.ExecNamedLastId(ctx, s.DB,
		`INSERT INTO acct_bank_rule (pattern, account_code) VALUES (:pattern, :account_code)`,
		map[string]any{"pattern": pattern, "account_code": accountCode})
	if err != nil {
		return 0, fmt.Errorf("accounting: create bank rule: %w", err)
	}
	return id, nil
}

// DeleteBankRule removes a suggestion rule; a missing id is sql.ErrNoRows.
func (s *Store) DeleteBankRule(ctx context.Context, id int) error {
	affected, err := storeutil.ExecNamedRows(ctx, s.DB,
		`DELETE FROM acct_bank_rule WHERE id = :id`, map[string]any{"id": id})
	if err != nil {
		return fmt.Errorf("accounting: delete bank rule %d: %w", id, err)
	}
	if affected == 0 {
		return fmt.Errorf("accounting: delete bank rule %d: %w", id, sql.ErrNoRows)
	}
	return nil
}

// GetEntryBySource returns the journal-entry header for a (source_type, source_key), or wrapped
// sql.ErrNoRows if none — used by the dispute worker to find the open dispute entry it must reverse when
// the chargeback is won (phase 2, wave 4).
func (s *Store) GetEntryBySource(ctx context.Context, sourceType entity.AcctSourceType, sourceKey string) (*entity.AcctJournalEntry, error) {
	entry, err := storeutil.QueryNamedOne[entity.AcctJournalEntry](ctx, s.DB,
		`SELECT id, occurred_at, description, source_type, source_key,
		        reversal_of, reversed_by, created_by, has_caveat, caveat, created_at
		 FROM acct_journal_entry WHERE source_type = :st AND source_key = :sk`,
		map[string]any{"st": string(sourceType), "sk": sourceKey})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		return nil, fmt.Errorf("accounting: get entry by source %s/%s: %w", sourceType, sourceKey, err)
	}
	return &entry, nil
}
