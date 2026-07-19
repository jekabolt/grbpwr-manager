package accounting

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
)

// entryColumns is the acct_journal_entry projection shared by the list query.
const entryColumns = `e.id, e.occurred_at, e.description, e.source_type, e.source_key,
	e.reversal_of, e.reversed_by, e.created_by, e.has_caveat, e.caveat, e.created_at`

// ListJournalEntries returns a page of journal-entry headers matching the filter plus the total
// match count. From/To bound occurred_at as a half-open interval [From, To); AccountCode filters via
// a join through acct_journal_line; SourceType is optional. Newest first.
func (s *Store) ListJournalEntries(ctx context.Context, f entity.AcctEntryFilter) ([]entity.AcctJournalEntry, int, error) {
	from := f.From
	if from.IsZero() {
		from = time.Date(1, 1, 1, 0, 0, 0, 0, time.UTC)
	}
	to := f.To
	if to.IsZero() {
		to = time.Date(9999, 12, 31, 0, 0, 0, 0, time.UTC)
	}
	params := map[string]any{
		"from": from.UTC().Format(dateLayout),
		"to":   to.UTC().Format(dateLayout),
	}
	conds := []string{"e.occurred_at >= :from", "e.occurred_at < :to"}
	join := ""
	if strings.TrimSpace(f.AccountCode) != "" {
		join = " JOIN acct_journal_line l ON l.entry_id = e.id JOIN acct_account a ON a.id = l.account_id"
		conds = append(conds, "a.code = :code")
		params["code"] = f.AccountCode
	}
	if strings.TrimSpace(string(f.SourceType)) != "" {
		conds = append(conds, "e.source_type = :source_type")
		params["source_type"] = string(f.SourceType)
	}
	where := " WHERE " + strings.Join(conds, " AND ")

	total, err := storeutil.QueryCountNamed(ctx, s.DB,
		`SELECT COUNT(DISTINCT e.id) FROM acct_journal_entry e`+join+where, params)
	if err != nil {
		return nil, 0, fmt.Errorf("accounting: count journal entries: %w", err)
	}

	limit := f.Limit
	if limit <= 0 {
		limit = defaultListLimit
	}
	if limit > maxListLimit {
		limit = maxListLimit
	}
	offset := f.Offset
	if offset < 0 {
		offset = 0
	}
	params["limit"] = limit
	params["offset"] = offset

	entries, err := storeutil.QueryListNamed[entity.AcctJournalEntry](ctx, s.DB,
		`SELECT DISTINCT `+entryColumns+` FROM acct_journal_entry e`+join+where+`
		 ORDER BY e.occurred_at DESC, e.id DESC
		 LIMIT :limit OFFSET :offset`, params)
	if err != nil {
		return nil, 0, fmt.Errorf("accounting: list journal entries: %w", err)
	}
	return entries, total, nil
}

// GetJournalEntry returns one entry with its lines (each line joined to its account code/name).
// A missing entry surfaces as sql.ErrNoRows (wrapped) so callers can map it to NotFound.
func (s *Store) GetJournalEntry(ctx context.Context, id int) (*entity.AcctJournalEntryFull, error) {
	entry, err := storeutil.QueryNamedOne[entity.AcctJournalEntry](ctx, s.DB,
		`SELECT id, occurred_at, description, source_type, source_key,
		        reversal_of, reversed_by, created_by, has_caveat, caveat, created_at
		 FROM acct_journal_entry WHERE id = :id`, map[string]any{"id": id})
	if err != nil {
		return nil, fmt.Errorf("accounting: get journal entry %d: %w", id, err)
	}
	lines, err := storeutil.QueryListNamed[entity.AcctJournalLine](ctx, s.DB,
		`SELECT l.id, l.entry_id, l.account_id, l.side, l.amount, l.amount_src, l.currency_src, l.note,
		        a.code AS account_code, a.name AS account_name
		 FROM acct_journal_line l
		 JOIN acct_account a ON a.id = l.account_id
		 WHERE l.entry_id = :id
		 ORDER BY l.id`, map[string]any{"id": id})
	if err != nil {
		return nil, fmt.Errorf("accounting: get journal entry %d lines: %w", id, err)
	}
	return &entity.AcctJournalEntryFull{Entry: entry, Lines: lines}, nil
}
