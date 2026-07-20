// Package accounting implements the double-entry general-ledger store (docs/plan-accounting/).
// The ledger is a DERIVED, append-only projection of existing operational facts (orders, material
// movements, production runs, opex) plus manual entries; base currency is EUR. This package owns
// the journal (acct_journal_entry / acct_journal_line), the chart of accounts (acct_account), the
// period gate (acct_period), the order-event outbox (acct_event) and the pull-source checkpoints
// (acct_checkpoint), and it reads other domains' tables directly to assemble posting facts — the
// same cross-domain-read precedent as internal/store/metrics.
//
// Like every sub-store it runs on the caller's connection (storeutil.Base.DB works on both the pool
// and inside a repo.Tx). CreateJournalEntry / ReverseJournalEntry never open a transaction
// themselves: the worker and the manual-entry admin handlers wrap them in repo.Tx so the entry
// header and its lines commit atomically.
package accounting

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
	"github.com/shopspring/decimal"
)

// dateLayout is MySQL's DATE format; occurred_at and acct_period.period are DATE columns compared
// against these strings in UTC (docs/plan-accounting/09 §9.3).
const dateLayout = "2006-01-02"

// defaultListLimit / maxListLimit bound paginated ledger reads.
const (
	defaultListLimit = 50
	maxListLimit     = 500
)

// The accounting sentinel errors (entity.ErrAcctUnbalanced, entity.ErrAcctPeriodClosed, …) live in package entity
// alongside the other domain sentinels, so the API layer can errors.Is against them without importing
// this store package. This package returns them via the entity.ErrAcct* names.

// Store implements dependency.Accounting. repo is used for cross-domain reads at posting time and to
// satisfy ContextStore.Tx (delegated to the repository) — the same shape as metrics.Store.
type Store struct {
	storeutil.Base
	repo dependency.Repository
}

// New creates a new accounting store.
func New(base storeutil.Base, repo dependency.Repository) *Store {
	return &Store{Base: base, repo: repo}
}

// Tx satisfies dependency.ContextStore by delegating to the repository. Accounting itself never
// needs to open a transaction (its writers run on the caller's connection); this exists so the
// interface embeds ContextStore uniformly with the other domains.
func (s *Store) Tx(ctx context.Context, fn func(ctx context.Context, rep dependency.Repository) error) error {
	return s.repo.Tx(ctx, fn)
}

// firstOfMonthUTC normalises t to the 1st of its month at 00:00 UTC (the acct_period key).
func firstOfMonthUTC(t time.Time) time.Time {
	y, m, _ := t.UTC().Date()
	return time.Date(y, m, 1, 0, 0, 0, 0, time.UTC)
}

// createdByOrSystem defaults an empty author to 'system' (matches acct_journal_entry.created_by).
func createdByOrSystem(v string) string {
	if strings.TrimSpace(v) == "" {
		return "system"
	}
	return v
}

// nullableCaveat returns the caveat only when the entry is flagged, so has_caveat and caveat stay
// consistent (a caveat text without the flag, or vice-versa, would be misleading).
func nullableCaveat(in entity.AcctJournalEntryInsert) any {
	if !in.HasCaveat {
		return nil
	}
	if in.Caveat.Valid {
		return in.Caveat.String
	}
	return nil
}

// resolveAccounts maps a set of account codes to their ids in one query, rejecting archived
// (entity.ErrAcctArchivedAccount) and unknown (entity.ErrAcctUnknownAccount) codes. Not cached: ~34 accounts, the
// query is cheap and always fresh (no invalidation to get wrong).
func (s *Store) resolveAccounts(ctx context.Context, codes []string) (map[string]int, error) {
	type row struct {
		Id       int    `db:"id"`
		Code     string `db:"code"`
		Archived bool   `db:"archived"`
	}
	rows, err := storeutil.QueryListNamed[row](ctx, s.DB,
		`SELECT id, code, archived FROM acct_account WHERE code IN (:codes)`,
		map[string]any{"codes": codes})
	if err != nil {
		return nil, fmt.Errorf("accounting: resolve accounts: %w", err)
	}
	byCode := make(map[string]int, len(rows))
	for _, r := range rows {
		if r.Archived {
			return nil, fmt.Errorf("%w: %s", entity.ErrAcctArchivedAccount, r.Code)
		}
		byCode[r.Code] = r.Id
	}
	for _, c := range codes {
		if _, ok := byCode[c]; !ok {
			return nil, fmt.Errorf("%w: %s", entity.ErrAcctUnknownAccount, c)
		}
	}
	return byCode, nil
}

// getAccountByCode loads one account, mapping a missing row to entity.ErrAcctUnknownAccount.
func (s *Store) getAccountByCode(ctx context.Context, code string) (entity.AcctAccount, error) {
	acc, err := storeutil.QueryNamedOne[entity.AcctAccount](ctx, s.DB,
		`SELECT id, code, name, section, statement, is_system, archived, created_at, updated_at
		 FROM acct_account WHERE code = :code`, map[string]any{"code": code})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return entity.AcctAccount{}, fmt.Errorf("%w: %s", entity.ErrAcctUnknownAccount, code)
		}
		return entity.AcctAccount{}, fmt.Errorf("accounting: get account %s: %w", code, err)
	}
	return acc, nil
}

// CreateJournalEntry is the single write path into the journal. See dependency.Accounting for the
// full contract. It does not open a transaction — the caller must wrap it in repo.Tx so the entry
// header and its lines are atomic.
func (s *Store) CreateJournalEntry(ctx context.Context, in entity.AcctJournalEntryInsert) (int, bool, error) {
	// 1) validate shape: >= 2 lines, each amount > 0, Σdebit == Σcredit, non-empty source_key.
	if len(in.Lines) < 2 {
		return 0, false, fmt.Errorf("%w: need >= 2 lines, got %d", entity.ErrAcctUnbalanced, len(in.Lines))
	}
	if strings.TrimSpace(in.SourceKey) == "" {
		return 0, false, fmt.Errorf("accounting: empty source_key")
	}
	var sumDebit, sumCredit decimal.Decimal
	codes := make([]string, 0, len(in.Lines))
	seen := make(map[string]bool, len(in.Lines))
	amounts := make([]decimal.Decimal, len(in.Lines))
	for i, ln := range in.Lines {
		amt := ln.Amount.Round(2)
		amounts[i] = amt
		if amt.LessThanOrEqual(decimal.Zero) {
			return 0, false, fmt.Errorf("%w: line %d amount must be > 0", entity.ErrAcctUnbalanced, i)
		}
		switch ln.Side {
		case entity.AcctSideDebit:
			sumDebit = sumDebit.Add(amt)
		case entity.AcctSideCredit:
			sumCredit = sumCredit.Add(amt)
		default:
			return 0, false, fmt.Errorf("accounting: invalid side %q on line %d", ln.Side, i)
		}
		if !seen[ln.AccountCode] {
			seen[ln.AccountCode] = true
			codes = append(codes, ln.AccountCode)
		}
	}
	if !sumDebit.Equal(sumCredit) {
		return 0, false, fmt.Errorf("%w: debit %s != credit %s", entity.ErrAcctUnbalanced, sumDebit.String(), sumCredit.String())
	}

	// 2) resolve account codes to ids (also guards archived / unknown).
	accByCode, err := s.resolveAccounts(ctx, codes)
	if err != nil {
		return 0, false, err
	}

	// 3) gate on the period (entity.ErrAcctPeriodClosed) — checked BEFORE insert so a posting into a closed
	//    period fails loudly and stays queued rather than sneaking in.
	if err := s.EnsurePeriodOpen(ctx, in.OccurredAt); err != nil {
		return 0, false, err
	}

	// 4) insert the entry header; on a duplicate (source_type, source_key) return the existing id.
	entryID, err := storeutil.ExecNamedLastId(ctx, s.DB, `
		INSERT INTO acct_journal_entry
			(occurred_at, description, source_type, source_key, created_by, has_caveat, caveat)
		VALUES (:occurred_at, :description, :source_type, :source_key, :created_by, :has_caveat, :caveat)`,
		map[string]any{
			"occurred_at": firstOfDayUTC(in.OccurredAt),
			"description": in.Description,
			"source_type": string(in.SourceType),
			"source_key":  in.SourceKey,
			"created_by":  createdByOrSystem(in.CreatedBy),
			"has_caveat":  in.HasCaveat,
			"caveat":      nullableCaveat(in),
		})
	if err != nil {
		if s.repo.IsErrUniqueViolation(err) {
			existing, gErr := storeutil.QueryNamedOne[struct {
				Id int `db:"id"`
			}](ctx, s.DB,
				`SELECT id FROM acct_journal_entry WHERE source_type = :st AND source_key = :sk`,
				map[string]any{"st": string(in.SourceType), "sk": in.SourceKey})
			if gErr != nil {
				return 0, false, fmt.Errorf("accounting: lookup existing entry after duplicate: %w", gErr)
			}
			return existing.Id, true, nil
		}
		return 0, false, fmt.Errorf("accounting: insert journal entry: %w", err)
	}

	// 5) bulk-insert the lines.
	rows := make([]map[string]any, 0, len(in.Lines))
	for i, ln := range in.Lines {
		rows = append(rows, map[string]any{
			"entry_id":     entryID,
			"account_id":   accByCode[ln.AccountCode],
			"side":         string(ln.Side),
			"amount":       amounts[i],
			"amount_src":   ln.AmountSrc,
			"currency_src": ln.CurrencySrc,
			"note":         ln.Note,
		})
	}
	if err := storeutil.BulkInsert(ctx, s.DB, "acct_journal_line", rows); err != nil {
		return 0, false, fmt.Errorf("accounting: insert journal lines: %w", err)
	}
	return entryID, false, nil
}

// firstOfDayUTC formats an occurred_at moment as a UTC DATE string (occurred_at is a DATE column).
func firstOfDayUTC(t time.Time) string {
	return t.UTC().Format(dateLayout)
}

// ReverseJournalEntry posts the mirror of entryID (sides swapped) and links the two. See
// dependency.Accounting for the guards. It does not open a transaction — the caller wraps it.
func (s *Store) ReverseJournalEntry(ctx context.Context, entryID int, reason, adminUsername string) (int, error) {
	orig, err := s.GetJournalEntry(ctx, entryID)
	if err != nil {
		return 0, err
	}
	// Guard order matches dependency.Accounting: reject reversing a reversal first, then an already-
	// reversed entry.
	if orig.Entry.SourceType == entity.AcctSourceReversal {
		return 0, entity.ErrAcctCannotReverseReversal
	}
	if orig.Entry.ReversedBy.Valid {
		return 0, entity.ErrAcctAlreadyReversed
	}

	// occurred_at of the reversal: the original's date if that period is still open, else today (the
	// current open period). If today's period is somehow closed, CreateJournalEntry surfaces
	// entity.ErrAcctPeriodClosed.
	occurredAt := orig.Entry.OccurredAt
	origClosed, err := s.isPeriodClosed(ctx, occurredAt)
	if err != nil {
		return 0, err
	}
	if origClosed {
		occurredAt = s.Now().UTC()
	}

	lines := make([]entity.AcctJournalLineInsert, 0, len(orig.Lines))
	for _, l := range orig.Lines {
		side := entity.AcctSideCredit
		if l.Side == entity.AcctSideCredit {
			side = entity.AcctSideDebit
		}
		lines = append(lines, entity.AcctJournalLineInsert{
			AccountCode: l.AccountCode,
			Side:        side,
			Amount:      l.Amount,
			AmountSrc:   l.AmountSrc,
			CurrencySrc: l.CurrencySrc,
			Note:        l.Note,
		})
	}

	desc := fmt.Sprintf("Reversal of #%d", entryID)
	if strings.TrimSpace(reason) != "" {
		desc = fmt.Sprintf("%s: %s", desc, reason)
	}
	// A partial prior attempt (reversal inserted, links not yet set) re-selects the same reversal id
	// via idempotency, so the two link updates below are safe to (re-)run.
	newID, _, err := s.CreateJournalEntry(ctx, entity.AcctJournalEntryInsert{
		OccurredAt:  occurredAt,
		Description: desc,
		SourceType:  entity.AcctSourceReversal,
		SourceKey:   fmt.Sprintf("rev:%d", entryID),
		CreatedBy:   createdByOrSystem(adminUsername),
		Lines:       lines,
	})
	if err != nil {
		return 0, err
	}
	if err := storeutil.ExecNamed(ctx, s.DB,
		`UPDATE acct_journal_entry SET reversal_of = :orig WHERE id = :new AND reversal_of IS NULL`,
		map[string]any{"orig": entryID, "new": newID}); err != nil {
		return 0, fmt.Errorf("accounting: set reversal_of: %w", err)
	}
	if err := storeutil.ExecNamed(ctx, s.DB,
		`UPDATE acct_journal_entry SET reversed_by = :new WHERE id = :orig`,
		map[string]any{"new": newID, "orig": entryID}); err != nil {
		return 0, fmt.Errorf("accounting: set reversed_by: %w", err)
	}
	return newID, nil
}

// ListAccounts returns the chart of accounts ordered by code; archived accounts are included only
// when includeArchived is true.
func (s *Store) ListAccounts(ctx context.Context, includeArchived bool) ([]entity.AcctAccount, error) {
	where := ""
	if !includeArchived {
		where = "WHERE archived = FALSE"
	}
	accts, err := storeutil.QueryListNamed[entity.AcctAccount](ctx, s.DB, `
		SELECT id, code, name, section, statement, is_system, archived, created_at, updated_at
		FROM acct_account `+where+`
		ORDER BY code`, nil)
	if err != nil {
		return nil, fmt.Errorf("accounting: list accounts: %w", err)
	}
	return accts, nil
}

// CreateAccount inserts a custom (non-system, non-archived) account and returns its id.
func (s *Store) CreateAccount(ctx context.Context, in entity.AcctAccountInsert) (int, error) {
	id, err := storeutil.ExecNamedLastId(ctx, s.DB, `
		INSERT INTO acct_account (code, name, section, statement, is_system, archived)
		VALUES (:code, :name, :section, :statement, FALSE, FALSE)`,
		map[string]any{
			"code":      in.Code,
			"name":      in.Name,
			"section":   string(in.Section),
			"statement": in.Statement,
		})
	if err != nil {
		return 0, fmt.Errorf("accounting: create account %s: %w", in.Code, err)
	}
	return id, nil
}

// UpdateAccountName renames a custom account. Code and section are immutable; a system account
// cannot be renamed (entity.ErrAcctSystemAccount); an unknown code is entity.ErrAcctUnknownAccount.
func (s *Store) UpdateAccountName(ctx context.Context, code, name string) error {
	acc, err := s.getAccountByCode(ctx, code)
	if err != nil {
		return err
	}
	if acc.IsSystem {
		return fmt.Errorf("%w: %s", entity.ErrAcctSystemAccount, code)
	}
	if err := storeutil.ExecNamed(ctx, s.DB,
		`UPDATE acct_account SET name = :name WHERE code = :code`,
		map[string]any{"name": name, "code": code}); err != nil {
		return fmt.Errorf("accounting: update account name %s: %w", code, err)
	}
	return nil
}

// SetAccountArchived archives/unarchives a custom account; a system account cannot be archived
// (entity.ErrAcctSystemAccount); an unknown code is entity.ErrAcctUnknownAccount.
func (s *Store) SetAccountArchived(ctx context.Context, code string, archived bool) error {
	acc, err := s.getAccountByCode(ctx, code)
	if err != nil {
		return err
	}
	if acc.IsSystem {
		return fmt.Errorf("%w: %s", entity.ErrAcctSystemAccount, code)
	}
	if err := storeutil.ExecNamed(ctx, s.DB,
		`UPDATE acct_account SET archived = :archived WHERE code = :code`,
		map[string]any{"archived": archived, "code": code}); err != nil {
		return fmt.Errorf("accounting: set account archived %s: %w", code, err)
	}
	return nil
}
