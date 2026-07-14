package metrics

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/cache"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
	"github.com/shopspring/decimal"
)

// NF-08 OPEX v2 — the line-item journal (opex_line) + recurring templates (opex_recurring). This
// supersedes the single-aggregate-per-(month,category) opex_entry table: lines carry their own
// currency (folded to base on write) and a free-text label, so an operator can enter individual
// costs («Adobe — 60 USD», «зарплата швеи — 1200 EUR») instead of one lumped monthly figure. The
// dashboard operating result reads opex_line (see getOpexForPeriod). opex_entry is kept in sync by
// UpsertOpexEntries for rollback safety; its drop is a later migration.

// firstOfMonthUTC snaps t to the 1st of its month at 00:00 UTC — the canonical month key used
// throughout OPEX (matches the DATE column and the pro-rating math in getOpexForPeriod).
func firstOfMonthUTC(t time.Time) time.Time {
	u := t.UTC()
	return time.Date(u.Year(), u.Month(), 1, 0, 0, 0, 0, time.UTC)
}

// fxRateHistory is the full costing_fx_rate history grouped by UPPERCASE currency (rows in no
// particular order); rateAsOf picks the effective rate for a given month. Loaded once per
// materialisation so folding every backfilled month at its own month-end rate is a single query,
// not N+1 (nf08-04).
type fxRateHistory map[string][]entity.CostingFxRate

// loadFxRateHistory reads every manual costing FX rate (all effective dates). A load failure is
// returned to the caller so the materialisation tick fails and retries, rather than silently booking
// every non-base line uncosted (nf08-03) — «rates unavailable» must be distinguishable from «this
// currency has no rate».
func (s *Store) loadFxRateHistory(ctx context.Context) (fxRateHistory, error) {
	rows, err := storeutil.QueryListNamed[entity.CostingFxRate](ctx, s.DB,
		`SELECT currency, rate_to_base, valid_from FROM costing_fx_rate`, map[string]any{})
	if err != nil {
		return nil, fmt.Errorf("load costing fx history: %w", err)
	}
	h := make(fxRateHistory, len(rows))
	for _, r := range rows {
		cur := strings.ToUpper(r.Currency)
		h[cur] = append(h[cur], r)
	}
	return h, nil
}

// rateAsOf returns the latest rate for `currency` whose valid_from is on or before asOf (the plan's
// «last rate with valid_from ≤ month-end»), and whether one exists.
func (h fxRateHistory) rateAsOf(currency string, asOf time.Time) (decimal.Decimal, bool) {
	var best *entity.CostingFxRate
	for i := range h[strings.ToUpper(currency)] {
		r := &h[strings.ToUpper(currency)][i]
		if r.ValidFrom.After(asOf) {
			continue
		}
		if best == nil || r.ValidFrom.After(best.ValidFrom) {
			best = r
		}
	}
	if best == nil {
		return decimal.Zero, false
	}
	return best.RateToBase, true
}

// foldOpexToBaseAsOf converts amount in `currency` to the base currency using the rate effective at
// `asOf` (the folded month's last day). It returns an invalid NullDecimal — meaning "uncosted" —
// when no base currency is configured or the currency had no rate at that date; such a line is
// excluded from the operating result and flagged, mirroring the fold used for production/dev costs.
func foldOpexToBaseAsOf(amount decimal.Decimal, currency, base string, hist fxRateHistory, asOf time.Time) decimal.NullDecimal {
	if base == "" {
		return decimal.NullDecimal{}
	}
	if currency == "" || strings.EqualFold(currency, base) {
		return decimal.NullDecimal{Decimal: amount.Round(2), Valid: true}
	}
	r, ok := hist.rateAsOf(currency, asOf)
	if !ok {
		return decimal.NullDecimal{}
	}
	return decimal.NullDecimal{Decimal: amount.Mul(r).Round(2), Valid: true}
}

// lastDayOfMonth returns the last instant-of-day date of month `m` (given its 1st), used to pick the
// FX rate effective within that month.
func lastDayOfMonth(firstOfMonth time.Time) time.Time {
	return firstOfMonth.AddDate(0, 1, -1)
}

// UpsertOpexLines writes operating-expense line items, upserting on the UNIQUE (month, category,
// label) key so a re-send with the same key updates in place (used both by the admin editor and by
// the aggregate-compat path). Callers fold AmountBase before this point (it is left NULL only when
// the line's currency has no FX rate); category validity is enforced in dto. Month is normalised to
// the 1st of the month here defensively.
func (s *Store) UpsertOpexLines(ctx context.Context, rows []entity.OpexLineInsert) error {
	for _, r := range rows {
		if err := storeutil.ExecNamed(ctx, s.DB, `
			INSERT INTO opex_line (month, category, label, amount, currency, amount_base, recurring_id, note)
			VALUES (:month, :category, :label, :amount, :currency, :amount_base, :recurring_id, :note)
			ON DUPLICATE KEY UPDATE
				amount = VALUES(amount),
				currency = VALUES(currency),
				amount_base = VALUES(amount_base),
				recurring_id = VALUES(recurring_id),
				note = VALUES(note)`,
			map[string]any{
				"month":        firstOfMonthUTC(r.Month).Format("2006-01-02"),
				"category":     r.Category,
				"label":        r.Label,
				"amount":       r.Amount,
				"currency":     strings.ToUpper(r.Currency),
				"amount_base":  r.AmountBase,
				"recurring_id": r.RecurringId,
				"note":         r.Note,
			}); err != nil {
			return fmt.Errorf("upsert opex line %s/%s/%s: %w",
				r.Month.Format("2006-01"), r.Category, r.Label, err)
		}
	}
	return nil
}

// DeleteOpexLine removes a single MANUAL OPEX line by id. A materialised line (recurring_id set) is
// refused with entity.ErrOpexLineMaterialised: deleting it would only resurrect it on the worker's
// next tick (insert-only per (recurring_id, month)), and a deleted-then-re-entered month would
// double-count after that tick (g25-11) — archive the template or add a manual adjustment line
// instead. A missing id is a no-op (idempotent delete).
func (s *Store) DeleteOpexLine(ctx context.Context, id int) error {
	line, err := storeutil.QueryNamedOne[struct {
		RecurringId sql.NullInt32 `db:"recurring_id"`
	}](ctx, s.DB, `SELECT recurring_id FROM opex_line WHERE id = :id`, map[string]any{"id": id})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil // already gone — deletion is idempotent
		}
		return fmt.Errorf("load opex line %d: %w", id, err)
	}
	if line.RecurringId.Valid {
		return entity.ErrOpexLineMaterialised
	}
	return storeutil.ExecNamed(ctx, s.DB,
		`DELETE FROM opex_line WHERE id = :id`, map[string]any{"id": id})
}

// ListOpexLines returns OPEX lines within the (inclusive, 1st-of-month) month bounds, optionally
// filtered to one category, newest month first then by label for a stable order.
func (s *Store) ListOpexLines(ctx context.Context, f entity.OpexLineFilter) ([]entity.OpexLine, error) {
	params := map[string]any{
		"from": firstOfMonthUTC(f.MonthFrom).Format("2006-01-02"),
		"to":   firstOfMonthUTC(f.MonthTo).Format("2006-01-02"),
	}
	catClause := ""
	if strings.TrimSpace(f.Category) != "" {
		catClause = " AND category = :category"
		params["category"] = f.Category
	}
	rows, err := storeutil.QueryListNamed[entity.OpexLine](ctx, s.DB, `
		SELECT id, month, category, label, amount, currency, amount_base, recurring_id, note,
		       created_at, updated_at
		FROM opex_line
		WHERE month >= :from AND month <= :to`+catClause+`
		ORDER BY month DESC, category, label, id`, params)
	if err != nil {
		return nil, fmt.Errorf("list opex lines: %w", err)
	}
	return rows, nil
}

// UpsertOpexRecurring inserts a recurring OPEX template when id == 0, otherwise updates the template
// with that id, returning its id. ActiveFrom/ActiveTo are normalised to the 1st of the month.
// Editing an existing template affects only months not yet materialised — the worker never rewrites
// an already-booked month (see MaterializeOpexRecurring).
func (s *Store) UpsertOpexRecurring(ctx context.Context, ins entity.OpexRecurringInsert, id int) (int, error) {
	activeTo := sqlNullMonth(ins.ActiveTo)
	params := map[string]any{
		"label":       ins.Label,
		"category":    ins.Category,
		"amount":      ins.Amount,
		"currency":    strings.ToUpper(ins.Currency),
		"active_from": firstOfMonthUTC(ins.ActiveFrom).Format("2006-01-02"),
		"active_to":   activeTo,
		"note":        ins.Note,
		"employee_id": ins.EmployeeId,
	}
	if id == 0 {
		newID, err := storeutil.ExecNamedLastId(ctx, s.DB, `
			INSERT INTO opex_recurring (label, category, amount, currency, active_from, active_to, note, employee_id)
			VALUES (:label, :category, :amount, :currency, :active_from, :active_to, :note, :employee_id)`, params)
		if err != nil {
			return 0, fmt.Errorf("insert opex recurring: %w", err)
		}
		return newID, nil
	}
	params["id"] = id
	if err := storeutil.ExecNamed(ctx, s.DB, `
		UPDATE opex_recurring
		SET label = :label, category = :category, amount = :amount, currency = :currency,
		    active_from = :active_from, active_to = :active_to, note = :note, employee_id = :employee_id
		WHERE id = :id`, params); err != nil {
		return 0, fmt.Errorf("update opex recurring %d: %w", id, err)
	}
	return id, nil
}

// ArchiveOpexRecurring marks a template archived so the worker stops materialising future months.
// Already-booked lines are left in place (past fixed costs stay on the operating result).
func (s *Store) ArchiveOpexRecurring(ctx context.Context, id int) error {
	return storeutil.ExecNamed(ctx, s.DB,
		`UPDATE opex_recurring SET archived = TRUE WHERE id = :id`, map[string]any{"id": id})
}

// ListOpexRecurring returns recurring templates, active-only unless includeArchived, newest first.
func (s *Store) ListOpexRecurring(ctx context.Context, includeArchived bool) ([]entity.OpexRecurring, error) {
	where := "WHERE archived = FALSE"
	if includeArchived {
		where = ""
	}
	rows, err := storeutil.QueryListNamed[entity.OpexRecurring](ctx, s.DB, `
		SELECT id, label, category, amount, currency, active_from, active_to, note, employee_id, archived,
		       created_at, updated_at
		FROM opex_recurring `+where+`
		ORDER BY active_from DESC, id DESC`, map[string]any{})
	if err != nil {
		return nil, fmt.Errorf("list opex recurring: %w", err)
	}
	return rows, nil
}

// MaterializeOpexRecurring books each active template into a monthly opex_line for every month from
// its ActiveFrom to min(upTo, ActiveTo), folding each month's amount at the FX rate effective THAT
// month (nf08-04 — a backfilled month is reproducible regardless of when the worker first ran, not
// stamped with today's rate). Dedup is on (recurring_id, month), so re-materialising a month is a
// no-op AND renaming a template's label/category no longer double-books past months (nf08-01), and
// two templates sharing (category, label) both book (nf08-02). The ON DUPLICATE clause recosts ONLY
// rows whose amount_base is still NULL — an already-costed month is frozen (amount/label/currency
// as-booked stay immutable) while a month left uncosted by a transient FX outage is repaired on the
// next healthy tick (nf08-03). Returns the number of lines newly created (recosts don't count).
// `upTo` is the current time (worker) or a fixed month (tests); it is snapped to the 1st of its month.
func (s *Store) MaterializeOpexRecurring(ctx context.Context, upTo time.Time) (int, error) {
	base := strings.ToUpper(cache.GetBaseCurrency())
	upToMonth := firstOfMonthUTC(upTo)

	// Fail the whole tick if the rate history can't be loaded — booking every non-base line uncosted
	// on a transient DB blip would be permanent (insert-only past), so retry instead (nf08-03).
	hist, err := s.loadFxRateHistory(ctx)
	if err != nil {
		return 0, fmt.Errorf("materialize opex: %w", err)
	}

	templates, err := s.ListOpexRecurring(ctx, false)
	if err != nil {
		return 0, fmt.Errorf("materialize opex: load templates: %w", err)
	}

	created := 0
	for _, t := range templates {
		end := upToMonth
		if t.ActiveTo.Valid {
			if to := firstOfMonthUTC(t.ActiveTo.Time); to.Before(end) {
				end = to
			}
		}
		for m := firstOfMonthUTC(t.ActiveFrom); !m.After(end); m = m.AddDate(0, 1, 0) {
			amountBase := foldOpexToBaseAsOf(t.Amount, t.Currency, base, hist, lastDayOfMonth(m))
			n, err := storeutil.ExecNamedRows(ctx, s.DB, `
				INSERT INTO opex_line (month, category, label, amount, currency, amount_base, recurring_id, note)
				VALUES (:month, :category, :label, :amount, :currency, :amount_base, :recurring_id, :note)
				ON DUPLICATE KEY UPDATE
					amount_base = IF(amount_base IS NULL, VALUES(amount_base), amount_base)`,
				map[string]any{
					"month":        m.Format("2006-01-02"),
					"category":     t.Category,
					"label":        t.Label,
					"amount":       t.Amount,
					"currency":     strings.ToUpper(t.Currency),
					"amount_base":  amountBase,
					"recurring_id": t.Id,
					"note":         t.Note,
				})
			if err != nil {
				return created, fmt.Errorf("materialize opex %s/%s (%s): %w",
					m.Format("2006-01"), t.Label, t.Category, err)
			}
			// ExecNamedRows returns rows-affected: 1 for a fresh insert, 2 for an ON DUPLICATE update
			// that changed a row (a NULL→value recost), 0 for a no-op. Count only genuine inserts.
			if n == 1 {
				created++
			}
		}
	}
	return created, nil
}

// sqlNullMonth normalises an optional ActiveTo bound to the 1st of the month for storage.
func sqlNullMonth(t sql.NullTime) any {
	if !t.Valid {
		return nil
	}
	return firstOfMonthUTC(t.Time).Format("2006-01-02")
}
