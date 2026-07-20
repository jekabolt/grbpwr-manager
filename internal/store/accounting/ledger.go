package accounting

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
	"github.com/shopspring/decimal"
)

// entryColumns is the acct_journal_entry projection shared by the list query.
const entryColumns = `e.id, e.occurred_at, e.description, e.source_type, e.source_key,
	e.reversal_of, e.reversed_by, e.created_by, e.has_caveat, e.caveat, e.created_at`

// EntryExistsBySource reports whether a journal entry with the given (source_type, source_key)
// exists. It is an O(1) lookup on the uniq_acct_entry_source unique index — the point lookup the
// refund worker's S1 ("has the sale been posted?") check uses instead of paging ListJournalEntries.
func (s *Store) EntryExistsBySource(ctx context.Context, sourceType entity.AcctSourceType, sourceKey string) (bool, error) {
	n, err := storeutil.QueryCountNamed(ctx, s.DB,
		`SELECT COUNT(*) FROM acct_journal_entry WHERE source_type = :st AND source_key = :sk`,
		map[string]any{"st": string(sourceType), "sk": sourceKey})
	if err != nil {
		return false, fmt.Errorf("accounting: entry exists by source %s/%s: %w", sourceType, sourceKey, err)
	}
	return n > 0, nil
}

// GetOrderPostingState reports the ledger's current posting state for one order (phase 2, wave 2):
// which delivered-chain entries exist and the exact outstanding 2090 / 1140 balances the delivered
// sale must drain. The order's entries are matched by source_key — exactly the UUID for
// prepayment/transit/delivered_sale, or "uuid:seq" for a refund — via an index-friendly (= OR LIKE)
// predicate on uniq_acct_entry_source, not a non-sargable SUBSTRING_INDEX. Remaining2090 / Remaining1140
// are signed to each account's normal side (2090 credit−debit, 1140 debit−credit). DeliveredEvent (an
// enqueued order_delivered event) says the order is operationally delivered even before its sale entry
// posts — the refund defer-guard (synthesis D8). An order with no entries yields the zero state (the
// no-GROUP-BY aggregate always returns one row), never sql.ErrNoRows.
func (s *Store) GetOrderPostingState(ctx context.Context, orderUUID string) (entity.AcctOrderPostingState, error) {
	row, err := storeutil.QueryNamedOne[struct {
		LegacySale    int             `db:"legacy_sale"`
		Prepayment    int             `db:"prepayment"`
		Transit       int             `db:"transit"`
		DeliveredSale int             `db:"delivered_sale"`
		Remaining2090 decimal.Decimal `db:"remaining_2090"`
		Remaining1140 decimal.Decimal `db:"remaining_1140"`
	}](ctx, s.DB, `
		SELECT
		    COALESCE(MAX(e.source_type = 'order_sale'), 0)           AS legacy_sale,
		    COALESCE(MAX(e.source_type = 'order_prepayment'), 0)     AS prepayment,
		    COALESCE(MAX(e.source_type = 'order_transit'), 0)        AS transit,
		    COALESCE(MAX(e.source_type = 'order_delivered_sale'), 0) AS delivered_sale,
		    COALESCE(SUM(CASE WHEN a.code = '2090'
		        THEN CASE WHEN l.side = 'credit' THEN l.amount ELSE -l.amount END ELSE 0 END), 0) AS remaining_2090,
		    COALESCE(SUM(CASE WHEN a.code = '1140'
		        THEN CASE WHEN l.side = 'debit' THEN l.amount ELSE -l.amount END ELSE 0 END), 0) AS remaining_1140
		FROM acct_journal_entry e
		LEFT JOIN acct_journal_line l ON l.entry_id = e.id
		LEFT JOIN acct_account a ON a.id = l.account_id
		WHERE (e.source_key = :uuid OR e.source_key LIKE :uuid_prefix)
		  AND e.source_type IN ('order_sale','order_prepayment','order_transit','order_delivered_sale','order_refund')`,
		map[string]any{"uuid": orderUUID, "uuid_prefix": orderUUID + ":%"})
	if err != nil {
		return entity.AcctOrderPostingState{}, fmt.Errorf("accounting: get order posting state %s: %w", orderUUID, err)
	}

	deliveredEvents, err := storeutil.QueryCountNamed(ctx, s.DB,
		`SELECT COUNT(*) FROM acct_event WHERE event_type = 'order_delivered' AND source_key = :uuid`,
		map[string]any{"uuid": orderUUID})
	if err != nil {
		return entity.AcctOrderPostingState{}, fmt.Errorf("accounting: get order delivered event %s: %w", orderUUID, err)
	}

	return entity.AcctOrderPostingState{
		LegacySale:     row.LegacySale > 0,
		Prepayment:     row.Prepayment > 0,
		Transit:        row.Transit > 0,
		DeliveredSale:  row.DeliveredSale > 0,
		DeliveredEvent: deliveredEvents > 0,
		Remaining2090:  row.Remaining2090,
		Remaining1140:  row.Remaining1140,
	}, nil
}

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
